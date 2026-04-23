package config

import (
	"os"
	"path/filepath"
	"strings"
)

// SaveToken writes the JWT to configDir/token with 0600 perms.
func SaveToken(configDir string, token string) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "token"), []byte(token), 0600)
}

// LoadToken reads the JWT from configDir/token.
// Returns ErrNotLoggedIn if the file does not exist.
func LoadToken(configDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "token"))
	if os.IsNotExist(err) {
		return "", ErrNotLoggedIn
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// DeleteToken removes the token file. Safe to call if already absent.
func DeleteToken(configDir string) error {
	err := os.Remove(filepath.Join(configDir, "token"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
