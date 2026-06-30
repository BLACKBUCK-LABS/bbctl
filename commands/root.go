package commands

import (
	"fmt"
	"os"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/blackbuck/bbctl/internal/shell"
	"github.com/blackbuck/bbctl/internal/ui"
	"github.com/spf13/cobra"
)

// Build metadata — injected via ldflags at release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

var refreshCache bool

// activeEnv is "dev" by default and set to "prod" when the user invokes
// "bbctl prod ...". It is the authoritative signal for which environment's
// BOLT token/relay to use — more robust than comparing backend URLs.
var activeEnv = "dev"

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
// Default (no prefix): hits dev backend (bbctl-dev.blackbuck.com).
// "bbctl prod <rest>" strips "prod" and forces the prod backend URL.
func Execute() {
	ui.Init()
	if len(os.Args) > 1 && os.Args[1] == "prod" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
		activeEnv = "prod"
		if os.Getenv("BBCTL_BACKEND_URL") == "" {
			prodURL := "https://bbctl.blackbuck.com" // default
			if configDir, err := config.DefaultConfigDir(); err == nil {
				if cfg, err := config.LoadOrDefault(configDir); err == nil && cfg.ProdBackendURL != "" {
					prodURL = cfg.ProdBackendURL
				}
			}
			os.Setenv("BBCTL_BACKEND_URL", prodURL) //nolint:errcheck
		}
	}
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
