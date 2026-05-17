package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/spf13/cobra"
)

var mcpTokenCmd = &cobra.Command{
	Use:   "mcp-token",
	Short: "Generate a long-lived token for Claude Code MCP integration",
	RunE:  runMCPToken,
}

func init() {
	rootCmd.AddCommand(mcpTokenCmd)
}

func runMCPToken(cmd *cobra.Command, args []string) error {
	cfgDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(cfgDir)
	if err != nil {
		return err
	}
	token, err := config.LoadToken(cfgDir)
	if err != nil || token == "" {
		return fmt.Errorf("not logged in — run: bbctl login")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	resp, err := c.GenerateMCPToken(cmd.Context())
	if err != nil {
		return fmt.Errorf("generate MCP token: %w", err)
	}

	fmt.Printf("\n✅ MCP token generated for %s\n", resp.Email)
	fmt.Printf("   Expires: %s\n\n", resp.ExpiresAt.Format("2006-01-02"))
	fmt.Printf("Run this command to add bbctl to Claude Code:\n\n")
	fmt.Printf("  claude mcp add --transport http bbctl --scope user %s/mcp \\\n",
		cfg.BackendURL)
	fmt.Printf("    --header \"Authorization: Bearer %s\"\n\n", resp.Token)

	fmt.Printf("Then restart Claude Code.\n\n")

	tokenPath := filepath.Join(cfgDir, "mcp_token")
	if err := os.WriteFile(tokenPath, []byte(resp.Token), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save token to %s: %v\n", tokenPath, err)
	} else {
		fmt.Printf("Token also saved to: %s\n", tokenPath)
	}
	return nil
}
