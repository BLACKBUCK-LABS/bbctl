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

	// Full-width single-table layout — no side preview. go-fuzzyfinder fixes
	// the preview pane at ~50% of the screen with no way to shrink it, which
	// left a large empty right panel. Dropping it hands the list the full
	// width; the preview's extra fields (Type, AZ) are folded into the row so
	// nothing is lost.
	idx, err := fuzzyfinder.Find(
		instances,
		func(i int) string {
			inst := instances[i]
			name := inst.Name
			if name == "" {
				name = "(no name)"
			}
			return fmt.Sprintf("%-42s   %-21s   %-9s   %-16s   %-13s   %-10s   %s",
				truncate(name, 42),
				inst.InstanceID,
				inst.AccountLabel,
				inst.PrivateIP,
				inst.InstanceType,
				inst.State,
				inst.AZ)
		},
		fuzzyfinder.WithHeader(fmt.Sprintf(
			"%-42s   %-21s   %-9s   %-16s   %-13s   %-10s   %s",
			"Name", "Instance ID", "Account", "Private IP", "Type", "State", "AZ")),
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
