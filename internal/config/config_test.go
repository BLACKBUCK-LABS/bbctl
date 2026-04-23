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
