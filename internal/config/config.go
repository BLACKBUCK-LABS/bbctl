package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrNotLoggedIn is returned when no token file exists.
var ErrNotLoggedIn = errors.New("not logged in — run: bbctl login")

// Config holds all user-facing configuration.
type Config struct {
	BackendURL         string `yaml:"backend_url"`
	AuthMode           string `yaml:"auth_mode"`           // jwt | sigv4
	OIDCIssuer         string `yaml:"oidc_issuer"`
	OIDCClientID       string `yaml:"oidc_client_id"`
	OIDCAuthEndpoint   string `yaml:"oidc_auth_endpoint"`
	OIDCTokenEndpoint  string `yaml:"oidc_token_endpoint"`
	// OIDCClientSecret is never written to config file — always read from
	// BBCTL_OIDC_CLIENT_SECRET env var.
	OIDCClientSecret   string `yaml:"-"`
	DefaultTimeoutSecs int    `yaml:"default_timeout_secs"`
	DefaultAccountID   string `yaml:"default_account_id"` // AWS account ID used when --account is omitted
}

// DefaultConfigDir returns ~/.bbctl.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bbctl"), nil
}

// LoadOrDefault loads config.yaml from configDir, or returns sensible defaults if absent.
func LoadOrDefault(configDir string) (*Config, error) {
	cfg := &Config{
		AuthMode:           "jwt",
		DefaultTimeoutSecs: 30,
	}
	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.DefaultTimeoutSecs == 0 {
		cfg.DefaultTimeoutSecs = 30
	}
	// Client secret is never stored in config.yaml — always read from env.
	cfg.OIDCClientSecret = os.Getenv("BBCTL_OIDC_CLIENT_SECRET")
	return cfg, nil
}
