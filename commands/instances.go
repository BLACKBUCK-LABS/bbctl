package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	ec2picker "github.com/blackbuck/bbctl/internal/ec2"
	"github.com/spf13/cobra"
)

var (
	instancesRefresh bool
	instancesAccount string
)

var instancesCmd = &cobra.Command{
	Use:   "instances",
	Short: "List EC2 instances across all accounts",
	Example: `  bbctl instances
  bbctl instances --refresh
  bbctl instances -a zinka`,
	RunE: runInstances,
}

func init() {
	instancesCmd.Flags().BoolVarP(&instancesRefresh, "refresh", "r", false, "Force refresh instance cache")
	instancesCmd.Flags().StringVarP(&instancesAccount, "account", "a", "", "Filter by account name or ID")
	rootCmd.AddCommand(instancesCmd)
}

func runInstances(cmd *cobra.Command, args []string) error {
	cfgDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(cfgDir)
	if err != nil {
		return err
	}
	token, err := config.LoadToken(cfgDir)
	if err != nil {
		return fmt.Errorf("not logged in — run: bbctl login")
	}

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)

	instances, err := ec2picker.LoadAll(cmd.Context(), c, cfg, cfgDir, instancesRefresh)
	if err != nil {
		return err
	}

	if instancesAccount != "" {
		accountID := cfg.ResolveAccount(instancesAccount)
		var filtered []ec2picker.Instance
		for _, inst := range instances {
			if inst.AccountID == accountID ||
				strings.EqualFold(inst.AccountLabel, instancesAccount) {
				filtered = append(filtered, inst)
			}
		}
		instances = filtered
	}

	if len(instances) == 0 {
		fmt.Fprintln(os.Stdout, "No instances found.")
		return nil
	}

	fmt.Printf("%-45s %-22s %-10s %-16s %-14s %s\n",
		"Name", "Instance ID", "Account", "Private IP", "Type", "State")
	fmt.Println(strings.Repeat("─", 115))
	for _, inst := range instances {
		name := inst.Name
		if name == "" {
			name = "(no name)"
		}
		fmt.Printf("%-45s %-22s %-10s %-16s %-14s %s\n",
			instanceTruncate(name, 45),
			inst.InstanceID,
			inst.AccountLabel,
			inst.PrivateIP,
			inst.InstanceType,
			inst.State)
	}
	fmt.Printf("\nTotal: %d instances\n", len(instances))
	return nil
}

func instanceTruncate(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
