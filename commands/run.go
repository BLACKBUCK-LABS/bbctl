package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	ec2picker "github.com/blackbuck/bbctl/internal/ec2"
	"github.com/blackbuck/bbctl/internal/ui"
	"github.com/spf13/cobra"
)

var runAccount string

var runCmd = &cobra.Command{
	Use:   "run [instance-id] -- <command> [args...]",
	Short: "Run a single command on an EC2 instance",
	Args:  cobra.ArbitraryArgs,
	RunE:  runRun,
}

func init() {
	runCmd.Flags().StringVarP(&runAccount, "account", "a", "", "AWS account name or ID (overrides default_account_id in config)")
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	token, err := config.LoadToken(configDir)
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(configDir)
	if err != nil {
		return err
	}
	if cfg.BackendURL == "" {
		return fmt.Errorf("backend_url not set in ~/.bbctl/config.yaml")
	}

	// Split args into optional instance ID + command.
	// If first arg starts with "i-" it's an instance ID; otherwise show picker.
	var instanceID string
	cmdArgs := args
	if len(args) > 0 && strings.HasPrefix(args[0], "i-") {
		instanceID = args[0]
		cmdArgs = args[1:]
	}

	command := strings.Join(cmdArgs, " ")
	if command == "" {
		return fmt.Errorf("usage: bbctl run [instance-id] -- <command> [args...]")
	}

	accountID := runAccount
	if accountID == "" {
		accountID = cfg.DefaultAccountID
	}
	accountID = cfg.ResolveAccount(accountID)

	c := newBackendClient(cfg, token)

	if instanceID == "" {
		instances, err := ec2picker.LoadAll(cmd.Context(), c, cfg, configDir, false)
		if err != nil {
			return fmt.Errorf("load instances: %w", err)
		}
		selected, err := ec2picker.Pick(instances)
		if err != nil {
			return err
		}
		if selected == nil {
			fmt.Println("Cancelled.")
			return nil
		}
		instanceID = selected.InstanceID
		if runAccount == "" {
			accountID = selected.AccountID
		}
		fmt.Println(ui.Arrow(fmt.Sprintf("%s (%s)", selected.Name, selected.InstanceID)))
	}

	if accountID == "" {
		return fmt.Errorf("AWS account ID is required: pass --account 123456789012 or set default_account_id in ~/.bbctl/config.yaml")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
		os.Exit(130)
	}()

	return runCommandDirect(ctx, instanceID, accountID, command, "", c)
}

// loadRunBoltToken returns the BOLT session token for env ("dev" or "prod"), or
// an error telling the user how to log in. `bbctl run` needs it because the
// backend authorizes every command through Dwarpal.
func loadRunBoltToken(configDir, env string) (string, error) {
	token, err := config.LoadBoltToken(configDir, env)
	if err != nil || strings.TrimSpace(token) == "" {
		login := "bbctl login"
		if env == "prod" {
			login = "bbctl prod login"
		}
		return "", fmt.Errorf("not logged in to BOLT — run: %s", login)
	}
	return strings.TrimSpace(token), nil
}

// newBackendClient builds the backend client and, when the user is logged in to
// BOLT, attaches the BOLT token so POST /v1/commands can authorize every command
// through Dwarpal. Without a BOLT login the backend answers 401.
func newBackendClient(cfg *config.Config, token string) *client.Client {
	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	if dir, err := config.DefaultConfigDir(); err == nil {
		if boltToken, err := loadRunBoltToken(dir, activeEnv); err == nil {
			c.SetBoltToken(boltToken)
		}
	}
	return c
}

func runCommandDirect(ctx context.Context, instanceID, accountID, command, privateIP string, c *client.Client) error {
	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	boltToken, err := loadRunBoltToken(configDir, activeEnv)
	if err != nil {
		return err
	}

	c.SetBoltToken(boltToken)
	resp, err := c.RunCommand(ctx, client.CommandRequest{
		InstanceID: instanceID,
		AccountID:  accountID,
		Command:    command,
		PrivateIP:  privateIP,
	})
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			handleAPIError(apiErr)
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cancelled")
			os.Exit(130)
		}
		return err
	}

	if resp.Stdout != "" {
		fmt.Fprint(os.Stdout, resp.Stdout)
		if !strings.HasSuffix(resp.Stdout, "\n") {
			fmt.Fprintln(os.Stdout)
		}
	}
	if resp.Stderr != "" {
		fmt.Fprint(os.Stderr, resp.Stderr)
		if !strings.HasSuffix(resp.Stderr, "\n") {
			fmt.Fprintln(os.Stderr)
		}
	}
	if resp.Truncated {
		fmt.Fprintln(os.Stderr, "[output truncated at 10MB — use tail/head to narrow]")
	}

	if resp.ExitCode != nil && *resp.ExitCode != 0 {
		os.Exit(*resp.ExitCode)
	}
	return nil
}

// apiErrorText returns a styled human-friendly message for well-known HTTP errors,
// or "" when the default (bare Error()) path applies. Pure function — no I/O, no exit.
func apiErrorText(err *client.APIError) string {
	switch err.HTTPStatus {
	case 402:
		return ui.Warn("This command requires manager approval.") + "\n" +
			"   Create a Jira ticket in PRODACCESS, then run:\n" +
			"   bbctl run <instance-id> --ticket PRODACCESS-XXXX -- <command>"
	case 401:
		return ui.Err("Not authenticated. Run: bbctl login")
	case 403, 409:
		// Run-command denials ("Permission Denied by BBCTL") and the one-at-a-time
		// message are the server's own words; show them as-is.
		if err.Message != "" {
			return ui.Err(err.Message)
		}
		return ui.Err("Error: " + err.Error())
	case 429:
		return ui.Warn("Rate limit exceeded — slow down or wait a minute.")
	case 504:
		return ui.Err("Command timed out — instance overloaded or SSM agent unresponsive.")
	default:
		if err.Message != "" || err.Reason != "" {
			return ui.Err("Error: " + err.Error())
		}
		return ""
	}
}

// handleAPIError prints a human-friendly message and exits for well-known HTTP errors.
func handleAPIError(err *client.APIError) {
	if msg := apiErrorText(err); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(1)
}
