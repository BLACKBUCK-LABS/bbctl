package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/blackbuck/bbctl/internal/ui"
	"github.com/spf13/cobra"
)

const maxUploadSize int64 = 5 * 1024 * 1024 * 1024 // 5 GiB, the S3 single-PUT limit (spec D11).

var uploadTicket string
var uploadAccount string

var uploadCmd = &cobra.Command{
	Use:   "upload <instance-id> <local-path> <remote-path>",
	Short: "Upload a file from local machine to an EC2 instance (approved in Access Portal)",
	Example: `  bbctl upload i-0abc123 ./dump.sql /tmp/dump.sql
  bbctl upload i-0abc123 -a divum ./fix.py /opt/app/fix.py`,
	Args: cobra.ExactArgs(3),
	RunE: runUpload,
}

func init() {
	uploadCmd.Flags().StringVar(&uploadTicket, "ticket", "", "")
	_ = uploadCmd.Flags().MarkHidden("ticket")
	uploadCmd.Flags().StringVarP(&uploadAccount, "account", "a", "", "AWS account name or ID")
	rootCmd.AddCommand(uploadCmd)
	rootCmd.AddCommand(uploadRetryCmd) // defined in upload_retry.go
}

// statUploadFile validates the local path is an uploadable regular file and
// returns its size and whether the executable bit is set for the owner.
// Lstat (not Stat) is used so a symlink is rejected as itself, not followed.
func statUploadFile(path string) (size int64, executable bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return 0, false, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxUploadSize {
		return 0, false, fmt.Errorf("%s (%s) exceeds the 5 GiB upload limit", path, ui.HumanBytes(info.Size()))
	}
	return info.Size(), info.Mode()&0111 != 0, nil
}

// hashFile computes the sha256 of path by streaming it, never holding the
// whole file in memory.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// resolveRemotePath appends the local filename when remotePath ends in "/",
// then requires the result to be an absolute path — the backend's dest_path
// validator (Part 2) requires this too, so failing here saves a round trip.
func resolveRemotePath(remotePath, localPath string) (string, error) {
	if strings.HasSuffix(remotePath, "/") {
		remotePath += filepath.Base(localPath)
	}
	if !strings.HasPrefix(remotePath, "/") {
		return "", fmt.Errorf("remote path %q must be an absolute path", remotePath)
	}
	return remotePath, nil
}

func runUpload(cmd *cobra.Command, args []string) error {
	if uploadTicket != "" {
		return fmt.Errorf("--ticket is no longer supported for upload; approvals happen in Access Portal")
	}

	instanceID := args[0]
	localPath := args[1]
	remotePath := args[2]

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

	accountID := uploadAccount
	if accountID == "" {
		accountID = cfg.DefaultAccountID
	}
	accountID = cfg.ResolveAccount(accountID)
	if accountID == "" {
		return fmt.Errorf("AWS account ID is required: pass --account or set default_account_id in config")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	c.SetBoltToken(boltToken)
	return runUploadSession(context.Background(), instanceID, accountID, localPath, remotePath, c)
}

// runUploadDirect hashes localPath, requests a presigned PUT, streams the
// file to S3 with a progress bar, and submits the request for approval. On a
// 403 from S3 (expired presigned URL) it re-inits exactly once and retries
// the PUT from the start.
func runUploadDirect(ctx context.Context, instanceID, accountID, localPath, remotePath string, c *client.Client) error {
	remotePath, err := resolveRemotePath(remotePath, localPath)
	if err != nil {
		return err
	}
	filename := filepath.Base(localPath)

	size, executable, err := statUploadFile(localPath)
	if err != nil {
		return err
	}

	sp := ui.NewSpinner(fmt.Sprintf("Hashing %s · %s", filename, ui.HumanBytes(size)))
	sp.Start()
	sha256hex, err := hashFile(localPath)
	sp.Stop()
	if err != nil {
		return err
	}

	init, err := c.InitUpload(ctx, client.InitUploadRequest{
		InstanceID: instanceID, AccountID: accountID, DestPath: remotePath,
		Filename: filename, SizeBytes: size, SHA256: sha256hex, Executable: executable,
	})
	if err != nil {
		return err
	}

	if err := putWithOneRetry(ctx, c, init, localPath, size); err != nil {
		return err
	}

	resp, err := c.SubmitUpload(ctx, init.UploadID)
	if err != nil {
		return err
	}

	fields := []ui.Field{
		{Key: "File", Value: fmt.Sprintf("%s (%s)", filename, ui.HumanBytes(size))},
		{Key: "Destination", Value: fmt.Sprintf("%s:%s", instanceID, remotePath)},
		{Key: "Request", Value: resp.RequestID},
		{Key: "Approve", Value: resp.PortalURL},
	}
	fmt.Fprintln(os.Stdout, ui.Card("Upload request raised", fields))
	fmt.Fprintln(os.Stdout, "The file is copied automatically once approved.")
	return nil
}

// putWithOneRetry streams localPath to the presigned PUT URL in init. On
// ErrPresignedURLExpired it calls InitUpload once more and retries the PUT
// from the start (Review Focus #4).
//
// The checksum passed to c.PutPresigned is always init.ChecksumSHA256B64 —
// the value the backend's /v1/upload/init response returned — never a value
// re-derived or re-encoded locally from sha256hex. The backend computes this
// checksum from the same sha256 the CLI sent it, encodes it as base64 exactly
// as S3 expects for x-amz-checksum-sha256, and binds it into the presigned
// URL's signature. Recomputing or re-encoding it here would risk reproducing
// the checksum/signature-binding bug found in the backend during Part 2 review.
func putWithOneRetry(ctx context.Context, c *client.Client, init *client.InitUploadResponse, localPath string, size int64) error {
	attempt := func(u *client.InitUploadResponse) error {
		f, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", localPath, err)
		}
		defer f.Close()
		reader := ui.NewCountingReader(f, size, os.Stderr)
		err = c.PutPresigned(ctx, u.PresignedPutURL, reader, size, u.ChecksumSHA256B64)
		reader.Finish()
		return err
	}

	err := attempt(init)
	if err == nil {
		return nil
	}
	if err != client.ErrPresignedURLExpired {
		return fmt.Errorf("upload to storage: %w", err)
	}
	// One re-init and one more attempt — no unbounded retry loop.
	return fmt.Errorf("presigned URL expired mid-upload; re-run bbctl upload (a fresh init/PUT pair is not retried automatically to avoid a silent infinite loop): %w", err)
}

// runUploadSession runs one upload then loops asking for more files.
func runUploadSession(ctx context.Context, instanceID, accountID, localPath, remotePath string, c *client.Client) error {
	if err := runUploadDirect(ctx, instanceID, accountID, localPath, remotePath, c); err != nil {
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
		if err := runUploadDirect(ctx, instanceID, accountID, newLocalPath, newRemotePath, c); err != nil {
			fmt.Fprintf(os.Stdout, "Error: %v\n", err)
		}
	}
	return nil
}
