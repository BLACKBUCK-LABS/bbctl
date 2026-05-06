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
	"github.com/spf13/cobra"
)

var runTicket string
var runAccount string

var runCmd = &cobra.Command{
	Use:   "run [instance-id] -- <command> [args...]",
	Short: "Run a single command on an EC2 instance",
	Args:  cobra.ArbitraryArgs,
	RunE:  runRun,
}

func init() {
	runCmd.Flags().StringVar(&runTicket, "ticket", "", "Jira ticket ID for restricted commands")
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

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)

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
		fmt.Printf("→ %s (%s)\n", selected.Name, selected.InstanceID)
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

	return runCommandDirect(ctx, instanceID, accountID, command, runTicket, c)
}

func runCommandDirect(ctx context.Context, instanceID, accountID, command, ticketID string, c *client.Client) error {
	resp, err := c.RunCommand(ctx, client.CommandRequest{
		InstanceID:   instanceID,
		AccountID:    accountID,
		Command:      command,
		JiraTicketID: ticketID,
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

	if resp.TicketKey != "" {
		fmt.Fprintf(os.Stdout, "Jira ticket created: %s\n", resp.TicketKey)
		fmt.Fprintf(os.Stdout, "   %s\n\n", resp.TicketURL)
		fmt.Fprintln(os.Stdout, "Waiting for manager approval.")
		fmt.Fprintf(os.Stdout, "   Once approved, run:\n     bbctl run %s --ticket %s -- %s\n",
			instanceID, resp.TicketKey, command)
		return nil
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

// handleAPIError prints a human-friendly message and exits for well-known HTTP errors.
func handleAPIError(err *client.APIError) {
	switch err.HTTPStatus {
	case 402:
		fmt.Fprintln(os.Stderr, "⚠️  This command requires manager approval.")
		fmt.Fprintln(os.Stderr, "   Create a Jira ticket in PRODACCESS project, then run:")
		fmt.Fprintln(os.Stderr, "   bbctl run <instance-id> --ticket PRODACCESS-XXXX -- <command>")
		os.Exit(1)
	case 401:
		fmt.Fprintln(os.Stderr, "Not authenticated. Run: bbctl login")
		os.Exit(1)
	case 429:
		fmt.Fprintln(os.Stderr, "Rate limit exceeded — slow down or wait a minute.")
		os.Exit(1)
	case 504:
		fmt.Fprintln(os.Stderr, "⏱  Command timed out — the instance may be overloaded or the SSM agent may be unresponsive.")
		os.Exit(1)
	default:
		if err.Message != "" || err.Reason != "" {
			fmt.Fprintln(os.Stderr, "Error:", err.Error())
		}
		os.Exit(1)
	}
}
