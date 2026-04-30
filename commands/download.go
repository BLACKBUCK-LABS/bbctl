package commands

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/spf13/cobra"
)

var downloadTicket string
var downloadAccount string

var downloadCmd = &cobra.Command{
	Use:   "download <instance-id> <remote-path> <local-path>",
	Short: "Download a file from an EC2 instance to local machine",
	Example: `  bbctl download i-0abc123 /var/log/app.log ./app.log
  bbctl download i-0abc123 /var/log/app.log -    # stream to stdout
  bbctl download i-0abc123 -a divum /tmp/heap.hprof ./heap.hprof --ticket REQ-456`,
	Args: cobra.ExactArgs(3),
	RunE: runDownload,
}

func init() {
	downloadCmd.Flags().StringVar(&downloadTicket, "ticket", "", "Jira ticket ID (required for restricted paths)")
	downloadCmd.Flags().StringVarP(&downloadAccount, "account", "a", "", "AWS account name or ID")
	rootCmd.AddCommand(downloadCmd)
}

func runDownload(cmd *cobra.Command, args []string) error {
	instanceID := args[0]
	remotePath := args[1]
	localPath := args[2]

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

	accountID := downloadAccount
	if accountID == "" {
		accountID = cfg.DefaultAccountID
	}
	accountID = cfg.ResolveAccount(accountID)
	if accountID == "" {
		return fmt.Errorf("AWS account ID is required: pass --account or set default_account_id in config")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	return runDownloadDirect(context.Background(), instanceID, accountID, remotePath, localPath, downloadTicket, c)
}

func runDownloadDirect(ctx context.Context, instanceID, accountID, remotePath, localPath, ticketID string, c *client.Client) error {
	resp, err := c.Download(ctx, client.DownloadRequest{
		InstanceID: instanceID,
		AccountID:  accountID,
		SrcPath:    remotePath,
		TicketID:   ticketID,
	})
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			handleAPIError(apiErr)
		}
		return err
	}

	if resp.TicketKey != "" {
		fmt.Fprintf(os.Stdout, "\nJira ticket created: %s\n", resp.TicketKey)
		fmt.Fprintf(os.Stdout, "   %s\n\n", resp.TicketURL)
		fmt.Fprintln(os.Stdout, "Waiting for manager approval.")
		fmt.Fprintln(os.Stdout, "   Once approved, run:")
		fmt.Fprintf(os.Stdout, "     bbctl download %s --ticket %s %s %s\n\n",
			instanceID, resp.TicketKey, remotePath, localPath)
		return nil
	}

	if localPath == "-" {
		if resp.PresignedURL != "" {
			httpResp, err := http.Get(resp.PresignedURL) //nolint:gosec,noctx
			if err != nil {
				return fmt.Errorf("fetch presigned URL: %w", err)
			}
			defer httpResp.Body.Close()
			_, err = io.Copy(os.Stdout, httpResp.Body)
			return err
		}
		data, err := base64.StdEncoding.DecodeString(resp.ContentB64)
		if err != nil {
			return fmt.Errorf("decode file content: %w", err)
		}
		_, err = os.Stdout.Write(data)
		return err
	}

	if resp.PresignedURL != "" {
		if err := downloadFromURL(resp.PresignedURL, localPath); err != nil {
			return err
		}
	} else {
		data, err := base64.StdEncoding.DecodeString(resp.ContentB64)
		if err != nil {
			return fmt.Errorf("decode file content: %w", err)
		}
		if err := os.WriteFile(localPath, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", localPath, err)
		}
	}

	fmt.Fprintf(os.Stdout, "Downloaded %s:%s → %s (%d bytes)\n",
		instanceID, remotePath, localPath, resp.SizeBytes)
	return nil
}

func downloadFromURL(url, localPath string) error {
	resp, err := http.Get(url) //nolint:gosec,noctx
	if err != nil {
		return fmt.Errorf("fetch presigned URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("presigned download returned %d", resp.StatusCode)
	}
	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", localPath, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", localPath, err)
	}
	return nil
}
