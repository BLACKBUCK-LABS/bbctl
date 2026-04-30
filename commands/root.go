package commands

import (
	"fmt"
	"os"

	"github.com/blackbuck/bbctl/internal/shell"
	"github.com/spf13/cobra"
)

// Build metadata — injected via ldflags at release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

var refreshCache bool

var rootCmd = &cobra.Command{
	Use:   "bbctl",
	Short: "Gated terminal access to prod EC2 instances via SSM",
	Long: `bbctl — gated terminal access to production EC2 instances via AWS SSM.

Commands are classified into three tiers:
  safe       — run immediately, no approval needed
  restricted — require a Jira ticket (auto-created on first run)
  denied     — never executed

` + shell.SafeCommandsTable + `

Restricted commands (curl, systemctl, kill, etc.) auto-create a Jira ticket
in the REQ project on first use. Once a manager approves the ticket, re-run
the same command with the ticket ID to execute it.

Use 'bbctl shell <instance-id>' for an interactive session.
Use 'bbctl run <instance-id> -- <command>' for a single command.`,
	Version: Version,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInteractive(cmd, refreshCache)
	},
}

// Execute is the entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolVarP(&refreshCache, "refresh", "r", false, "Force refresh instance cache")
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}
