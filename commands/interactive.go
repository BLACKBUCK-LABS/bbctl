package commands

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	ec2picker "github.com/blackbuck/bbctl/internal/ec2"
	"github.com/blackbuck/bbctl/internal/shell"
	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
)

type action struct {
	Key     string
	Icon    string
	Label   string
	Preview string
}

var actions = []action{
	{Key: "shell", Icon: "🖥 ", Label: "Open shell", Preview: "Start an interactive shell session on the selected instance"},
	{Key: "bolt", Icon: "⚡ ", Label: "BOLT", Preview: "Open a fast relay PTY session on the selected instance"},
	{Key: "run", Icon: "▶ ", Label: "Run command", Preview: "Execute a one-shot command on the selected instance"},
	{Key: "upload", Icon: "↑ ", Label: "Upload file", Preview: "Upload a file from your machine to the instance"},
	{Key: "download", Icon: "↓ ", Label: "Download file", Preview: "Download a file from the instance to your machine"},
	{Key: "details", Icon: "ℹ ", Label: "Instance details", Preview: "Display detailed information about the instance"},
}

type resourceOption struct {
	Key   string
	Label string
	Desc  string
}

var resourceOptions = []resourceOption{
	{Key: "ec2", Label: "EC2  Instances", Desc: "Open shell · run commands · upload/download files on EC2 instances"},
	{Key: "rds", Label: "RDS  Databases", Desc: "Connect to a governed MySQL REPL on RDS instances"},
}

type rdsItem struct {
	Identifier   string
	Endpoint     string
	Port         int32
	Engine       string
	Status       string
	AccountLabel string // lowercase, matches Secrets Manager path and backend lookup
}

func runInteractive(cmd *cobra.Command, forceRefresh bool) error {
	cfgDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	token, err := config.LoadToken(cfgDir)
	if err != nil {
		return fmt.Errorf("not logged in — run: bbctl login")
	}
	cfg, err := config.LoadOrDefault(cfgDir)
	if err != nil {
		return err
	}
	if cfg.BackendURL == "" {
		return fmt.Errorf("backend_url not set in ~/.bbctl/config.yaml")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)

	fmt.Print("\033[2J\033[H") // clear screen
	shell.PrintWelcome(shell.WelcomeInfo{
		Email:   emailFromToken(token),
		Version: Version,
	})

	resKey, err := pickResourceType()
	if err != nil {
		return err
	}
	if resKey == "" {
		fmt.Println("Cancelled.")
		return nil
	}

	switch resKey {
	case "ec2":
		return runInteractiveEC2(cmd, c, cfg, cfgDir, token, forceRefresh)
	case "rds":
		return runInteractiveRDS(cmd.Context(), c, cfg, token)
	}
	return nil
}

func pickResourceType() (string, error) {
	idx, err := fuzzyfinder.Find(
		resourceOptions,
		func(i int) string { return resourceOptions[i].Label },
		fuzzyfinder.WithHeader("Select resource type  ·  ESC to cancel"),
		fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
			if i < 0 {
				return ""
			}
			return resourceOptions[i].Desc
		}),
	)
	if err != nil {
		if err == fuzzyfinder.ErrAbort {
			return "", nil
		}
		return "", err
	}
	return resourceOptions[idx].Key, nil
}

// runInteractiveEC2 is the EC2 flow: load instances → fuzzy-pick → action.
func runInteractiveEC2(cmd *cobra.Command, c *client.Client, cfg *config.Config, cfgDir, token string, forceRefresh bool) error {
	instances, err := ec2picker.LoadAll(cmd.Context(), c, cfg, cfgDir, forceRefresh)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "401") || strings.Contains(errLower, "unauthorized") {
			fmt.Println("Session expired. Logging in again...")
			if loginErr := runLogin(cmd, []string{}); loginErr != nil {
				return loginErr
			}
			var loadErr error
			token, loadErr = config.LoadToken(cfgDir)
			if loadErr != nil {
				return fmt.Errorf("login failed: %w", loadErr)
			}
			c = client.New(cfg.BackendURL, token, "bbctl/"+Version)
			fmt.Print("Loading instances...")
			instances, err = ec2picker.LoadAll(cmd.Context(), c, cfg, cfgDir, forceRefresh)
			if err != nil {
				fmt.Println()
				return fmt.Errorf("load instances: %w", err)
			}
			fmt.Printf("\r%-30s\r", "")
		} else {
			return fmt.Errorf("load instances: %w", err)
		}
	}
	if len(instances) == 0 {
		fmt.Fprintln(os.Stdout, "No instances found.")
		return nil
	}

	// Ensure token is current — may have been refreshed during 401 re-login.
	if latestToken, lerr := config.LoadToken(cfgDir); lerr == nil {
		token = latestToken
	}

	selected, err := ec2picker.Pick(instances)
	if err != nil {
		return err
	}
	if selected == nil {
		fmt.Println("Cancelled.")
		return nil
	}
	fmt.Printf("→ %s (%s)\n", selected.Name, selected.InstanceID)

	actionKey, err := pickAction(selected)
	if err != nil {
		return err
	}
	if actionKey == "" {
		fmt.Println("Cancelled.")
		return nil
	}

	return executeAction(cmd.Context(), actionKey, selected, c, cfg, cfgDir, token)
}

// runInteractiveRDS loads RDS instances from all accounts, lets the user pick
// one, then opens a governed MySQL REPL session.
func runInteractiveRDS(ctx context.Context, c *client.Client, cfg *config.Config, token string) error {
	if len(cfg.AccountAliases) == 0 {
		return fmt.Errorf("no account_aliases in ~/.bbctl/config.yaml")
	}

	type result struct {
		items []rdsItem
		err   error
	}

	labels := make([]string, 0, len(cfg.AccountAliases))
	for label := range cfg.AccountAliases {
		labels = append(labels, label)
	}

	results := make([]result, len(labels))
	var wg sync.WaitGroup
	for i, label := range labels {
		i, label := i, label
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.ListDatabases(ctx, label, "")
			if err != nil {
				results[i] = result{err: fmt.Errorf("%s: %w", label, err)}
				return
			}
			items := make([]rdsItem, len(resp.Databases))
			for j, db := range resp.Databases {
				items[j] = rdsItem{
					Identifier:   db.Identifier,
					Endpoint:     db.Endpoint,
					Port:         db.Port,
					Engine:       db.Engine,
					Status:       db.Status,
					AccountLabel: label,
				}
			}
			results[i] = result{items: items}
		}()
	}
	wg.Wait()

	var all []rdsItem
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err.Error())
			continue
		}
		all = append(all, r.items...)
	}

	if len(all) == 0 {
		if len(errs) > 0 {
			return fmt.Errorf("failed to load databases: %s", strings.Join(errs, "; "))
		}
		fmt.Println("No databases found.")
		return nil
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].AccountLabel != all[j].AccountLabel {
			return all[i].AccountLabel < all[j].AccountLabel
		}
		return all[i].Identifier < all[j].Identifier
	})

	selected, err := pickRDS(all)
	if err != nil {
		return err
	}
	if selected == nil {
		fmt.Println("Cancelled.")
		return nil
	}
	fmt.Printf("→ %s (%s)\n", selected.Identifier, selected.AccountLabel)

	cfgDir, err := config.DefaultConfigDir()
	if err != nil {
		cfgDir = ""
	}
	boltToken, _ := config.LoadBoltToken(cfgDir, activeEnv)
	return startDBConnect(selected.Identifier, selected.AccountLabel, cfg, token, boltToken)
}

func pickRDS(items []rdsItem) (*rdsItem, error) {
	idx, err := fuzzyfinder.Find(
		items,
		func(i int) string {
			it := items[i]
			return fmt.Sprintf("%-45s %-12s %-12s %s",
				instanceTruncate(it.Identifier, 45), it.Engine, it.Status, it.AccountLabel)
		},
		fuzzyfinder.WithHeader(fmt.Sprintf("%-45s %-12s %-12s %s",
			"Identifier", "Engine", "Status", "Account")),
		fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
			if i < 0 {
				return ""
			}
			it := items[i]
			return fmt.Sprintf(
				"Identifier: %s\nEngine:     %s\nStatus:     %s\nEndpoint:   %s:%d\nAccount:    %s",
				it.Identifier, it.Engine, it.Status, it.Endpoint, it.Port, it.AccountLabel)
		}),
	)
	if err != nil {
		if err == fuzzyfinder.ErrAbort {
			return nil, nil
		}
		return nil, err
	}
	return &items[idx], nil
}

func pickAction(inst *ec2picker.Instance) (string, error) {
	idx, err := fuzzyfinder.Find(
		actions,
		func(i int) string {
			return actions[i].Icon + " " + actions[i].Label
		},
		fuzzyfinder.WithHeader(fmt.Sprintf("Action for %s (%s)", inst.Name, inst.InstanceID)),
		fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
			if i < 0 {
				return ""
			}
			return actions[i].Preview
		}),
	)
	if err != nil {
		if err == fuzzyfinder.ErrAbort {
			return "", nil
		}
		return "", err
	}
	return actions[idx].Key, nil
}

func executeAction(ctx context.Context, actionKey string, inst *ec2picker.Instance, c *client.Client, cfg *config.Config, cfgDir, token string) error {
	scanner := bufio.NewScanner(os.Stdin)
	switch actionKey {
	case "shell":
		return runShellDirect(inst.InstanceID, inst.AccountID, cfg, cfgDir, token, inst.PrivateIP)

	case "bolt":
		relayURL, boltToken, err := boltEnvAndToken(cfg, cfgDir)
		if err != nil {
			return err
		}
		return runBoltShell(relayURL, boltToken, inst.InstanceID)

	case "run":
		fmt.Print("Command: ")
		if !scanner.Scan() {
			return nil
		}
		command := strings.TrimSpace(scanner.Text())
		if command == "" {
			return nil
		}
		return runCommandDirect(ctx, inst.InstanceID, inst.AccountID, command, "", inst.PrivateIP, c)

	case "upload":
		fmt.Print("Local path:  ")
		if !scanner.Scan() {
			return nil
		}
		localPath := strings.TrimSpace(scanner.Text())
		fmt.Print("Remote path: ")
		if !scanner.Scan() {
			return nil
		}
		remotePath := strings.TrimSpace(scanner.Text())
		if localPath == "" || remotePath == "" {
			return nil
		}
		return runUploadSession(ctx, inst.InstanceID, inst.AccountID, localPath, remotePath, "", c)

	case "download":
		fmt.Print("Remote file path: ")
		if !scanner.Scan() {
			return nil
		}
		remotePath := strings.TrimSpace(scanner.Text())
		fmt.Print("Local path (or - for stdout): ")
		if !scanner.Scan() {
			return nil
		}
		localPath := strings.TrimSpace(scanner.Text())
		if remotePath == "" || localPath == "" {
			return nil
		}
		return runDownloadSession(ctx, inst.InstanceID, inst.AccountID, remotePath, localPath, c)

	case "details":
		printInstanceDetails(inst)
		return nil

	default:
		return nil
	}
}

// emailFromToken extracts the email claim from a JWT without verifying the
// signature — used only for display in the welcome screen.
func emailFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	// base64.RawURLEncoding expects no padding — strip any that exists.
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return ""
	}
	return claims.Email
}

// cacheAgeStr returns a human-readable age of the newest instance cache file.
func cacheAgeStr(cfgDir string, cfg *config.Config) string {
	var newest time.Time
	for _, accountID := range cfg.AccountAliases {
		info, err := os.Stat(ec2picker.CachePath(cfgDir, cfg.BackendURL, accountID))
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return "fresh"
	}
	age := time.Since(newest)
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
}

func printInstanceDetails(inst *ec2picker.Instance) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", inst.Name)
	fmt.Fprintf(w, "Instance ID:\t%s\n", inst.InstanceID)
	fmt.Fprintf(w, "Account:\t%s (%s)\n", inst.AccountLabel, inst.AccountID)
	fmt.Fprintf(w, "Private IP:\t%s\n", inst.PrivateIP)
	fmt.Fprintf(w, "Type:\t%s\n", inst.InstanceType)
	fmt.Fprintf(w, "State:\t%s\n", inst.State)
	w.Flush()
}
