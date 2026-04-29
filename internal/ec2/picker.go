package ec2

import (
	"fmt"

	"github.com/ktr0731/go-fuzzyfinder"
)

// Pick opens an interactive fuzzy picker and returns the selected instance.
// Returns nil, nil if user cancels (ESC).
func Pick(instances []Instance) (*Instance, error) {
	if len(instances) == 0 {
		return nil, fmt.Errorf("no instances available")
	}

	idx, err := fuzzyfinder.Find(
		instances,
		func(i int) string {
			inst := instances[i]
			name := inst.Name
			if name == "" {
				name = "(no name)"
			}
			return fmt.Sprintf("%-45s %-22s %-10s %-16s %s",
				truncate(name, 45),
				inst.InstanceID,
				inst.AccountLabel,
				inst.PrivateIP,
				inst.State)
		},
		fuzzyfinder.WithHeader(fmt.Sprintf(
			"%-45s %-22s %-10s %-16s %s",
			"Name", "Instance ID", "Account", "Private IP", "State")),
		fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
			if i == -1 {
				return ""
			}
			inst := instances[i]
			name := inst.Name
			if name == "" {
				name = "(no name)"
			}
			return fmt.Sprintf(
				"Name:      %s\n"+
					"ID:        %s\n"+
					"Account:   %s (%s)\n"+
					"Private:   %s\n"+
					"Public:    %s\n"+
					"Type:      %s\n"+
					"State:     %s\n"+
					"AZ:        %s",
				name,
				inst.InstanceID,
				inst.AccountLabel, inst.AccountID,
				inst.PrivateIP,
				inst.PublicIP,
				inst.InstanceType,
				inst.State,
				inst.AZ)
		}),
	)

	if err != nil {
		if err == fuzzyfinder.ErrAbort {
			return nil, nil // ESC pressed
		}
		return nil, err
	}
	return &instances[idx], nil
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
