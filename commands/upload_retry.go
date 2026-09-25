package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/blackbuck/bbctl/internal/ui"
	"github.com/spf13/cobra"
)

var uploadRetryCmd = &cobra.Command{
	Use:   "retry <request-id>",
	Short: "Retry a failed file upload copy (requester only, up to 3 attempts within 48h of approval)",
	Args:  cobra.ExactArgs(1),
	RunE:  runUploadRetry,
}

func runUploadRetry(cmd *cobra.Command, args []string) error {
	requestID := args[0]

	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	if config.IsBoltTokenExpired(configDir, activeEnv) {
		return fmt.Errorf("upload needs Access Portal login — run: bbctl login")
	}
	token, err := config.LoadToken(configDir)
	if err != nil {
		return err
	}
	boltToken, err := config.LoadBoltToken(configDir, activeEnv)
	if err != nil {
		return fmt.Errorf("upload needs Access Portal login — run: bbctl login")
	}
	cfg, err := config.LoadOrDefault(configDir)
	if err != nil {
		return err
	}
	if cfg.BackendURL == "" {
		return fmt.Errorf("backend_url not set in ~/.bbctl/config.yaml")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	c.SetBoltToken(boltToken)

	resp, err := c.RetryUpload(context.Background(), requestID)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			handleAPIError(apiErr)
		}
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), ui.Success(fmt.Sprintf("Retry started for request %s — status: %s", resp.RequestID, resp.Status)))
	return nil
}
