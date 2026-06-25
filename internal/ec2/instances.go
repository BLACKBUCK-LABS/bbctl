package ec2

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const CacheTTL = time.Hour

type Instance struct {
	Name         string    `json:"name"`
	InstanceID   string    `json:"instance_id"`
	AccountID    string    `json:"account_id"`
	AccountLabel string    `json:"account_label"`
	PrivateIP    string    `json:"private_ip"`
	PublicIP     string    `json:"public_ip"`
	InstanceType string    `json:"instance_type"`
	State        string    `json:"state"`
	AZ           string    `json:"az"`
	FetchedAt    time.Time `json:"fetched_at"`
}

type accountCache struct {
	Instances []Instance `json:"instances"`
	FetchedAt time.Time  `json:"fetched_at"`
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// backendSlug derives a short filesystem-safe slug from a backend URL hostname
// so that dev and prod caches never collide (e.g. "bbctl-dev-blackbuck-com").
func backendSlug(backendURL string) string {
	u, err := url.Parse(backendURL)
	if err != nil || u.Host == "" {
		return "default"
	}
	return nonAlnum.ReplaceAllString(u.Host, "-")
}

func CachePath(configDir, backendURL, accountID string) string {
	return filepath.Join(configDir, "cache", backendSlug(backendURL), accountID+".json")
}

func LoadCache(configDir, backendURL, accountID string) ([]Instance, error) {
	data, err := os.ReadFile(CachePath(configDir, backendURL, accountID))
	if err != nil {
		return nil, nil // cache miss
	}
	var c accountCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, nil // corrupt cache
	}
	if time.Since(c.FetchedAt) > CacheTTL {
		return nil, nil // expired
	}
	return c.Instances, nil
}

func SaveCache(configDir, backendURL, accountID string, instances []Instance) error {
	dir := filepath.Join(configDir, "cache", backendSlug(backendURL))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(accountCache{
		Instances: instances,
		FetchedAt: time.Now(),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(CachePath(configDir, backendURL, accountID), data, 0600)
}

func ClearCache(configDir, backendURL, accountID string) error {
	if accountID == "" {
		if backendURL == "" {
			return os.RemoveAll(filepath.Join(configDir, "cache"))
		}
		return os.RemoveAll(filepath.Join(configDir, "cache", backendSlug(backendURL)))
	}
	return os.Remove(CachePath(configDir, backendURL, accountID))
}
