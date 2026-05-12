package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/spf13/cobra"
)

var downloadAccount string

var downloadCmd = &cobra.Command{
	Use:   "download <instance-id> <remote-path> <local-path>",
	Short: "Download a file from an EC2 instance to local machine",
	Example: `  bbctl download i-0abc123 /var/log/app.log ./app.log
  bbctl download i-0abc123 /var/log/app.log -    # stream to stdout
  bbctl download i-0abc123 -a divum /tmp/heap.hprof ./heap.hprof`,
	Args: cobra.ExactArgs(3),
	RunE: runDownload,
}

func init() {
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
	return runDownloadSession(context.Background(), instanceID, accountID, remotePath, localPath, c)
}

func runDownloadDirect(ctx context.Context, instanceID, accountID, remotePath, localPath string, c *client.Client) error {
	if localPath == "." || strings.HasSuffix(localPath, "/") {
		localPath = filepath.Join(localPath, filepath.Base(remotePath))
	}
	resp, err := c.Download(ctx, client.DownloadRequest{
		InstanceID: instanceID,
		AccountID:  accountID,
		SrcPath:    remotePath,
	})
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			handleAPIError(apiErr)
		}
		return err
	}

	if resp.PresignedURL == "" {
		return fmt.Errorf("no download URL in response")
	}

	if localPath == "-" {
		httpResp, err := http.Get(resp.PresignedURL) //nolint:gosec,noctx
		if err != nil {
			return fmt.Errorf("fetch presigned URL: %w", err)
		}
		defer httpResp.Body.Close()
		_, err = io.Copy(os.Stdout, httpResp.Body)
		return err
	}

	fmt.Fprintf(os.Stdout, "Downloading %s...\n", resp.Filename)
	if err := downloadFromURL(resp.PresignedURL, localPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Downloaded %s:%s → %s\n", instanceID, remotePath, localPath)
	return nil
}

// runDownloadSession runs one download then loops asking for more files.
func runDownloadSession(ctx context.Context, instanceID, accountID, remotePath, localPath string, c *client.Client) error {
	if err := runDownloadDirect(ctx, instanceID, accountID, remotePath, localPath, c); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stdout, "\nDownload another file? [y/N]: ")
		if !scanner.Scan() {
			break
		}
		if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
			break
		}
		fmt.Fprint(os.Stdout, "Remote path: ")
		if !scanner.Scan() {
			break
		}
		newRemotePath := strings.TrimSpace(scanner.Text())
		fmt.Fprint(os.Stdout, "Local path [. for current dir]: ")
		if !scanner.Scan() {
			break
		}
		newLocalPath := strings.TrimSpace(scanner.Text())
		if newLocalPath == "" {
			newLocalPath = "."
		}
		if newRemotePath == "" {
			fmt.Fprintln(os.Stdout, "Remote path cannot be empty.")
			continue
		}
		if err := runDownloadDirect(ctx, instanceID, accountID, newRemotePath, newLocalPath, c); err != nil {
			fmt.Fprintf(os.Stdout, "Error: %v\n", err)
		}
	}
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
