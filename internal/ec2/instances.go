package ec2

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func CachePath(configDir, accountID string) string {
	return filepath.Join(configDir, "cache", accountID+".json")
}

func LoadCache(configDir, accountID string) ([]Instance, error) {
	data, err := os.ReadFile(CachePath(configDir, accountID))
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

func SaveCache(configDir, accountID string, instances []Instance) error {
	dir := filepath.Join(configDir, "cache")
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
	return os.WriteFile(CachePath(configDir, accountID), data, 0600)
}

func ClearCache(configDir, accountID string) error {
	if accountID == "" {
		return os.RemoveAll(filepath.Join(configDir, "cache"))
	}
	return os.Remove(CachePath(configDir, accountID))
}
