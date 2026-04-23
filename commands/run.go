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
	"github.com/spf13/cobra"
)

var runTicket string
var runAccount string

var runCmd = &cobra.Command{
	Use:   "run <instance-id> -- <command> [args...]",
	Short: "Run a single command on an EC2 instance",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runRun,
}

func init() {
	runCmd.Flags().StringVar(&runTicket, "ticket", "", "Jira ticket ID for restricted commands")
	runCmd.Flags().StringVar(&runAccount, "account", "", "AWS account ID (overrides default_account_id in config)")
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	instanceID := args[0]
	command := strings.Join(args[1:], " ")
	if command == "" {
		return fmt.Errorf("usage: bbctl run <instance-id> -- <command> [args...]")
	}

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

	accountID := runAccount
	if accountID == "" {
		accountID = cfg.DefaultAccountID
	}
	if accountID == "" {
		return fmt.Errorf("AWS account ID is required: pass --account 123456789012 or set default_account_id in ~/.bbctl/config.yaml")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var requestID string

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
		if requestID != "" {
			_ = c.CancelCommand(context.Background(), requestID)
		}
		os.Exit(130)
	}()

	resp, err := c.RunCommand(ctx, client.CommandRequest{
		InstanceID:   instanceID,
		AccountID:    accountID,
		Command:      command,
		JiraTicketID: runTicket,
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
	requestID = resp.RequestID

	// Backend auto-created a Jira ticket — restricted command, no --ticket flag.
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
	}
}
