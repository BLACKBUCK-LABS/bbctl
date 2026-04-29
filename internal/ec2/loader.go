package ec2

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
)

// LoadAll fetches instances from all configured accounts concurrently.
// Uses cache when available. Skips accounts that fail.
func LoadAll(ctx context.Context,
	c *client.Client,
	cfg *config.Config,
	configDir string,
	forceRefresh bool) ([]Instance, error) {

	if len(cfg.AccountAliases) == 0 {
		return nil, fmt.Errorf(
			"no account_aliases in ~/.bbctl/config.yaml — " +
				"add account aliases to enable instance picker")
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		all  []Instance
		errs []string
	)

	for label, accountID := range cfg.AccountAliases {
		label, accountID := label, accountID
		wg.Add(1)
		go func() {
			defer wg.Done()

			if !forceRefresh {
				cached, _ := LoadCache(configDir, accountID)
				if cached != nil {
					mu.Lock()
					all = append(all, cached...)
					mu.Unlock()
					return
				}
			}

			instances, err := fetchFromBackend(ctx, c, accountID, capitalize(label))
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", label, err))
				mu.Unlock()
				return
			}

			_ = SaveCache(configDir, accountID, instances)

			mu.Lock()
			all = append(all, instances...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(all) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("failed to load instances: %s",
			strings.Join(errs, "; "))
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].AccountLabel != all[j].AccountLabel {
			return all[i].AccountLabel < all[j].AccountLabel
		}
		return all[i].Name < all[j].Name
	})

	return all, nil
}

func fetchFromBackend(ctx context.Context,
	c *client.Client,
	accountID, accountLabel string) ([]Instance, error) {

	infos, err := c.ListInstances(ctx, accountID)
	if err != nil {
		return nil, err
	}

	result := make([]Instance, len(infos))
	for i, info := range infos {
		result[i] = Instance{
			Name:         info.Name,
			InstanceID:   info.InstanceID,
			AccountID:    accountID,
			AccountLabel: accountLabel,
			PrivateIP:    info.PrivateIP,
			PublicIP:     info.PublicIP,
			InstanceType: info.InstanceType,
			State:        info.State,
			AZ:           info.AZ,
		}
	}
	return result, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
