package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	ec2picker "github.com/blackbuck/bbctl/internal/ec2"
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
	{Key: "run", Icon: "▶ ", Label: "Run command", Preview: "Execute a one-shot command on the selected instance"},
	{Key: "upload", Icon: "↑ ", Label: "Upload file", Preview: "Upload a file from your machine to the instance"},
	{Key: "download", Icon: "↓ ", Label: "Download file", Preview: "Download a file from the instance to your machine"},
	{Key: "details", Icon: "ℹ ", Label: "Instance details", Preview: "Display detailed information about the instance"},
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

	instances, err := ec2picker.LoadAll(cmd.Context(), c, cfg, cfgDir, forceRefresh)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "401") || strings.Contains(errLower, "unauthorized") {
			fmt.Println("Session expired. Logging in again...")
			if loginErr := runLogin(cmd, []string{}); loginErr != nil {
				return loginErr
			}
			token, err = config.LoadToken(cfgDir)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
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

	return executeAction(cmd.Context(), actionKey, selected, c, cfg, token)
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

func executeAction(ctx context.Context, actionKey string, inst *ec2picker.Instance, c *client.Client, cfg *config.Config, token string) error {
	scanner := bufio.NewScanner(os.Stdin)
	switch actionKey {
	case "shell":
		return runShellDirect(inst.InstanceID, inst.AccountID, cfg, token)

	case "run":
		fmt.Print("Command: ")
		if !scanner.Scan() {
			return nil
		}
		command := strings.TrimSpace(scanner.Text())
		if command == "" {
			return nil
		}
		return runCommandDirect(ctx, inst.InstanceID, inst.AccountID, command, "", c)

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
		return runUploadDirect(ctx, inst.InstanceID, inst.AccountID, localPath, remotePath, "", c)

	case "download":
		fmt.Print("Remote path:              ")
		if !scanner.Scan() {
			return nil
		}
		remotePath := strings.TrimSpace(scanner.Text())
		fmt.Print("Local path (- for stdout): ")
		if !scanner.Scan() {
			return nil
		}
		localPath := strings.TrimSpace(scanner.Text())
		if remotePath == "" || localPath == "" {
			return nil
		}
		return runDownloadDirect(ctx, inst.InstanceID, inst.AccountID, remotePath, localPath, "", c)

	case "details":
		printInstanceDetails(inst)
		return nil

	default:
		return nil
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
