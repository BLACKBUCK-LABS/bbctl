package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SaveToken writes the id_token and optionally the refresh_token to configDir.
func SaveToken(configDir, idToken, refreshToken string) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "token"), []byte(idToken), 0600); err != nil {
		return err
	}
	if refreshToken != "" {
		return os.WriteFile(filepath.Join(configDir, "refresh_token"), []byte(refreshToken), 0600)
	}
	return nil
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

// LoadRefreshToken reads the refresh_token from configDir/refresh_token.
func LoadRefreshToken(configDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "refresh_token"))
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

// IsTokenExpired returns true if the stored JWT is missing, unparseable,
// or within 60 seconds of expiry.
func IsTokenExpired(configDir string) bool {
	token, err := LoadToken(configDir)
	if err != nil || token == "" {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return true
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return true
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return true
	}
	return time.Now().Add(60 * time.Second).Unix() > claims.Exp
}

// RefreshToken exchanges the stored refresh_token for a new id_token,
// saves both, and returns the new id_token.
func RefreshToken(configDir string, cfg *Config) (string, error) {
	refreshToken, err := LoadRefreshToken(configDir)
	if err != nil || refreshToken == "" {
		return "", fmt.Errorf("no refresh token stored — run: bbctl login")
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {cfg.OIDCClientID},
		"client_secret": {cfg.OIDCClientSecret},
	}
	resp, err := http.PostForm(cfg.OIDCTokenEndpoint, data)
	if err != nil {
		return "", fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh token failed: status %d", resp.StatusCode)
	}

	var result struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode refresh response: %w", err)
	}
	if result.IDToken == "" {
		return "", fmt.Errorf("no id_token in refresh response")
	}

	newRefresh := refreshToken
	if result.RefreshToken != "" {
		newRefresh = result.RefreshToken
	}
	if err := SaveToken(configDir, result.IDToken, newRefresh); err != nil {
		return "", fmt.Errorf("save refreshed token: %w", err)
	}
	return result.IDToken, nil
}
