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
// Uses cache when available, namespaced by backend URL to avoid dev/prod collisions.
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

			backendURL := c.BaseURL()
			if !forceRefresh {
				cached, _ := LoadCache(configDir, backendURL, accountID)
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

			_ = SaveCache(configDir, backendURL, accountID, instances)

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

// LoadWindows fetches instances for Windows clients: a single account
// ("zinka", from cfg.AccountAliases — not hardcoded) via the backend's
// v2 endpoint, which the backend itself pre-filters to an allowlist before
// responding. Unlike LoadAll, it never fans out to every configured
// account — only the one account the allowlisted instances live in.
func LoadWindows(ctx context.Context,
	c *client.Client,
	cfg *config.Config,
	configDir string,
	forceRefresh bool) ([]Instance, error) {

	accountID, ok := cfg.AccountAliases["zinka"]
	if !ok {
		return nil, fmt.Errorf(
			"no \"zinka\" entry in account_aliases in ~/.bbctl/config.yaml — " +
				"required for the Windows instance list")
	}

	backendURL := c.BaseURL()
	if !forceRefresh {
		if cached, _ := LoadCache(configDir, backendURL, accountID); cached != nil {
			return cached, nil
		}
	}

	infos, err := c.ListInstancesV2(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("zinka: %w", err)
	}

	instances := make([]Instance, len(infos))
	for i, info := range infos {
		instances[i] = Instance{
			Name:         info.Name,
			InstanceID:   info.InstanceID,
			AccountID:    accountID,
			AccountLabel: "Zinka",
			PrivateIP:    info.PrivateIP,
			PublicIP:     info.PublicIP,
			InstanceType: info.InstanceType,
			State:        info.State,
			AZ:           info.AZ,
		}
	}

	_ = SaveCache(configDir, backendURL, accountID, instances)

	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })

	return instances, nil
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
