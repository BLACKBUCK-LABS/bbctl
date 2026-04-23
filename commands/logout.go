package commands

import (
	"fmt"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove the local token",
	RunE:  runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	if err := config.DeleteToken(configDir); err != nil {
		return err
	}
	fmt.Println("✓ Logged out.")
	return nil
}
