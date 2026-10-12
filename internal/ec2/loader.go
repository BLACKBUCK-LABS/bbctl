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

	return loadAccounts(ctx, c, cfg, configDir, forceRefresh, "",
		func(ctx context.Context, c *client.Client, accountID string) ([]client.InstanceInfo, error) {
			return c.ListInstances(ctx, accountID)
		})
}

// LoadWindows fetches instances for Windows clients from every configured
// account (same fan-out as LoadAll), via the backend's v2 endpoint — which
// the backend itself pre-filters to an allowlist before responding, so an
// instance from any onboarded account (not just one hardcoded account) shows
// up as soon as it's added to the backend's WINDOWS_INSTANCE_IDS allowlist.
func LoadWindows(ctx context.Context,
	c *client.Client,
	cfg *config.Config,
	configDir string,
	forceRefresh bool) ([]Instance, error) {

	// Cache key suffix keeps this separate from LoadAll's cache entries for
	// the same account — v2 responses are allowlist-filtered, v1 are not,
	// and must never be read back through each other's cache file.
	return loadAccounts(ctx, c, cfg, configDir, forceRefresh, "-windows",
		func(ctx context.Context, c *client.Client, accountID string) ([]client.InstanceInfo, error) {
			return c.ListInstancesV2(ctx, accountID)
		})
}

// loadAccounts fans fetchFn out across every configured account concurrently,
// using the cache first unless forceRefresh. Shared by LoadAll and
// LoadWindows — only the per-account backend call (and cache namespace, via
// cacheKeySuffix) differs between them.
func loadAccounts(ctx context.Context,
	c *client.Client,
	cfg *config.Config,
	configDir string,
	forceRefresh bool,
	cacheKeySuffix string,
	fetchFn func(ctx context.Context, c *client.Client, accountID string) ([]client.InstanceInfo, error),
) ([]Instance, error) {

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
		cacheKey := accountID + cacheKeySuffix
		wg.Add(1)
		go func() {
			defer wg.Done()

			backendURL := c.BaseURL()
			if !forceRefresh {
				if cached, _ := LoadCache(configDir, backendURL, cacheKey); cached != nil {
					mu.Lock()
					all = append(all, cached...)
					mu.Unlock()
					return
				}
			}

			infos, err := fetchFn(ctx, c, accountID)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", label, err))
				mu.Unlock()
				return
			}

			instances := toInstances(infos, accountID, capitalize(label))
			_ = SaveCache(configDir, backendURL, cacheKey, instances)

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

func toInstances(infos []client.InstanceInfo, accountID, accountLabel string) []Instance {
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
	return result
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
