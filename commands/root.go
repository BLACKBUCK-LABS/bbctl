package commands

import (
	"fmt"
	"os"
	"runtime"

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
	// Runtime failures (auth, network, a dropped DB session) should print a
	// clean error — not the full command usage/help. Cobra should only surface
	// usage for argument/flag parsing errors. Execute() prints the error once.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInteractive(cmd, refreshCache)
	},
}

// Execute is the entry point called from main.
// Default (no prefix): hits dev backend (bbctl-dev.blackbuck.com).
// "bbctl prod <rest>" strips "prod" and forces the prod backend URL.
// Windows always forces prod — the Windows instance allowlist only exists
// in the prod VPC, so the dev/prod split doesn't apply there.
func Execute() {
	ui.Init()
	if runtime.GOOS == "windows" {
		activeEnv = "prod"
		forceProdBackendURL()
		// Already forced to prod above — "bbctl prod ..." is a harmless no-op
		// on Windows, just strip the arg so it doesn't reach cobra as a command.
		if len(os.Args) > 1 && os.Args[1] == "prod" {
			os.Args = append(os.Args[:1], os.Args[2:]...)
		}
	} else if len(os.Args) > 1 && os.Args[1] == "prod" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
		activeEnv = "prod"
		forceProdBackendURL()
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// forceProdBackendURL sets BBCTL_BACKEND_URL to the prod backend, unless
// already set (e.g. for local testing). Resolution order: existing env var
// (no-op) > prod_backend_url in config.yaml > config.DefaultProdBackendURL.
func forceProdBackendURL() {
	if os.Getenv("BBCTL_BACKEND_URL") != "" {
		return
	}
	prodURL := config.DefaultProdBackendURL
	if configDir, err := config.DefaultConfigDir(); err == nil {
		if cfg, err := config.LoadOrDefault(configDir); err == nil && cfg.ProdBackendURL != "" {
			prodURL = cfg.ProdBackendURL
		}
	}
	os.Setenv("BBCTL_BACKEND_URL", prodURL) //nolint:errcheck
}

func init() {
	rootCmd.Flags().BoolVarP(&refreshCache, "refresh", "r", false, "Force refresh instance cache")
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}
