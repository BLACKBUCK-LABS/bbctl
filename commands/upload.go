package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/blackbuck/bbctl/internal/ui"
	"github.com/spf13/cobra"
)

var uploadTicket string
var uploadAccount string

var uploadCmd = &cobra.Command{
	Use:   "upload <instance-id> <local-path> <remote-path>",
	Short: "Upload a file from local machine to an EC2 instance",
	Example: `  bbctl upload i-0abc123 ./dump.sql /tmp/dump.sql
  bbctl upload i-0abc123 -a divum ./fix.py /opt/app/fix.py --ticket REQ-456`,
	Args: cobra.ExactArgs(3),
	RunE: runUpload,
}

func init() {
	uploadCmd.Flags().StringVar(&uploadTicket, "ticket", "", "Access request ID (required for restricted paths)")
	uploadCmd.Flags().StringVarP(&uploadAccount, "account", "a", "", "AWS account name or ID")
	rootCmd.AddCommand(uploadCmd)
}

func runUpload(cmd *cobra.Command, args []string) error {
	instanceID := args[0]
	localPath := args[1]
	remotePath := args[2]

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

	accountID := uploadAccount
	if accountID == "" {
		accountID = cfg.DefaultAccountID
	}
	accountID = cfg.ResolveAccount(accountID)
	if accountID == "" {
		return fmt.Errorf("AWS account ID is required: pass --account or set default_account_id in config")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	return runUploadSession(context.Background(), instanceID, accountID, localPath, remotePath, uploadTicket, c)
}

func runUploadDirect(ctx context.Context, instanceID, accountID, localPath, remotePath, ticketID string, c *client.Client) error {
	filename := filepath.Base(localPath)
	if strings.HasSuffix(remotePath, "/") {
		remotePath = remotePath + filename
	}

	content, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", localPath, err)
	}

	sum := sha256.Sum256(content)
	sha256hex := fmt.Sprintf("%x", sum)
	contentB64 := base64.StdEncoding.EncodeToString(content)

	sp := ui.NewSpinner(fmt.Sprintf("Uploading %s · %s", filename, ui.HumanBytes(int64(len(content)))))
	sp.Start()
	resp, err := c.Upload(ctx, client.UploadRequest{
		InstanceID: instanceID,
		AccountID:  accountID,
		DestPath:   remotePath,
		Filename:   filename,
		ContentB64: contentB64,
		SHA256:     sha256hex,
		TicketID:   ticketID,
	})
	if err != nil {
		sp.StopErr("Upload failed")
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			handleAPIError(apiErr)
		}
		return err
	}
	sp.Stop()

	if resp.TicketKey != "" {
		rerun := fmt.Sprintf("bbctl upload %s -a %s %s %s --ticket %s",
			instanceID, accountID, localPath, remotePath, resp.TicketKey)
		fmt.Fprintln(os.Stdout, ticketCard(resp.TicketKey, resp.TicketURL, rerun))
		return nil
	}

	fmt.Fprintln(os.Stdout, ui.Success(fmt.Sprintf("Uploaded %s → %s:%s", localPath, instanceID, remotePath)))
	return nil
}

// runUploadSession runs one upload then loops asking for more files.
func runUploadSession(ctx context.Context, instanceID, accountID, localPath, remotePath, ticketID string, c *client.Client) error {
	if err := runUploadDirect(ctx, instanceID, accountID, localPath, remotePath, ticketID, c); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stdout, "\nUpload another file? [y/N]: ")
		if !scanner.Scan() {
			break
		}
		if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
			break
		}
		fmt.Fprint(os.Stdout, "Local path: ")
		if !scanner.Scan() {
			break
		}
		newLocalPath := strings.TrimSpace(scanner.Text())
		fmt.Fprint(os.Stdout, "Remote path: ")
		if !scanner.Scan() {
			break
		}
		newRemotePath := strings.TrimSpace(scanner.Text())
		if newLocalPath == "" || newRemotePath == "" {
			fmt.Fprintln(os.Stdout, "Paths cannot be empty.")
			continue
		}
		if err := runUploadDirect(ctx, instanceID, accountID, newLocalPath, newRemotePath, "", c); err != nil {
			fmt.Fprintf(os.Stdout, "Error: %v\n", err)
		}
	}
	return nil
}
