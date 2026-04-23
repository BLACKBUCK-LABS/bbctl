package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	assert.Equal(t, "jwt", cfg.AuthMode)
	assert.Equal(t, 30, cfg.DefaultTimeoutSecs)
	assert.Equal(t, "https://bbctl.blackbuck.com", cfg.BackendURL)
	assert.Equal(t, "https://accounts.google.com", cfg.OIDCIssuer)
	assert.Equal(t, "396628175360-g90ptoadcl2coqrtk09oa2625a0k4ppf.apps.googleusercontent.com", cfg.OIDCClientID)
	assert.Equal(t, "https://accounts.google.com/o/oauth2/v2/auth", cfg.OIDCAuthEndpoint)
	assert.Equal(t, "https://oauth2.googleapis.com/token", cfg.OIDCTokenEndpoint)
	assert.Equal(t, "735317561518", cfg.DefaultAccountID)
}

func TestLoadConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(""), 0600))
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	// All production defaults must still be populated when config.yaml is empty.
	assert.Equal(t, "https://bbctl.blackbuck.com", cfg.BackendURL)
	assert.Equal(t, "https://accounts.google.com", cfg.OIDCIssuer)
	assert.Equal(t, "396628175360-g90ptoadcl2coqrtk09oa2625a0k4ppf.apps.googleusercontent.com", cfg.OIDCClientID)
	assert.Equal(t, "735317561518", cfg.DefaultAccountID)
}

func TestLoadConfig_BackendURLEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BBCTL_BACKEND_URL", "https://bbctl-staging.blackbuck.com")
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	// Env var beats both default and config file.
	assert.Equal(t, "https://bbctl-staging.blackbuck.com", cfg.BackendURL)
}

func TestLoadConfig_BackendURLEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	yaml := "backend_url: https://custom-backend\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0600))
	t.Setenv("BBCTL_BACKEND_URL", "https://bbctl-staging.blackbuck.com")
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	// Env var beats config file value.
	assert.Equal(t, "https://bbctl-staging.blackbuck.com", cfg.BackendURL)
}

func TestLoadConfig_ClientSecretDefault(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	// In local/test builds defaultOIDCClientSecret is "" (injected by ldflags in
	// release builds). Assert the field holds whatever the var was set to — the
	// key invariant is that config file and env var don't pollute it unexpectedly.
	assert.Equal(t, "", cfg.OIDCClientSecret)
}

func TestLoadConfig_ClientSecretEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BBCTL_OIDC_CLIENT_SECRET", "custom-secret-value")
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	// Env var beats the embedded default.
	assert.Equal(t, "custom-secret-value", cfg.OIDCClientSecret)
}

func TestLoadConfig_ClientSecretConfigFileIgnored(t *testing.T) {
	dir := t.TempDir()
	// Even if a malicious or buggy config.yaml tries to set the secret,
	// the yaml:"-" tag must keep it out. The field must never equal the file value.
	yaml := "oidc_client_secret: from-file-attempt\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0600))
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	assert.NotEqual(t, "from-file-attempt", cfg.OIDCClientSecret)
}

func TestLoadConfig_File(t *testing.T) {
	dir := t.TempDir()
	yaml := "backend_url: https://my-backend\nauth_mode: sigv4\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0600))
	cfg, err := config.LoadOrDefault(dir)
	require.NoError(t, err)
	assert.Equal(t, "https://my-backend", cfg.BackendURL)
	assert.Equal(t, "sigv4", cfg.AuthMode)
}

func TestTokenRoundtrip(t *testing.T) {
	dir := t.TempDir()
	tok := "eyJ.test.token"
	require.NoError(t, config.SaveToken(dir, tok))

	// File should exist with 0600 perms.
	info, err := os.Stat(filepath.Join(dir, "token"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	loaded, err := config.LoadToken(dir)
	require.NoError(t, err)
	assert.Equal(t, tok, loaded)
}

func TestLoadToken_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := config.LoadToken(dir)
	assert.ErrorIs(t, err, config.ErrNotLoggedIn)
}

func TestDeleteToken(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.SaveToken(dir, "tok"))
	require.NoError(t, config.DeleteToken(dir))
	_, err := config.LoadToken(dir)
	assert.ErrorIs(t, err, config.ErrNotLoggedIn)
}

func TestDeleteToken_AlreadyAbsent(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, config.DeleteToken(dir)) // must not error
}
