package config_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeJWT(t *testing.T, exp int64) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]int64{"exp": exp})
	require.NoError(t, err)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s.%s.sig", header, body)
}

func TestIsBoltTokenExpired(t *testing.T) {
	dir := t.TempDir()

	// No file at all: expired (ErrNotLoggedIn).
	assert.True(t, config.IsBoltTokenExpired(dir, "dev"))

	// Valid, far-future token: not expired.
	future := fakeJWT(t, time.Now().Add(24*time.Hour).Unix())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte(future), 0600))
	assert.False(t, config.IsBoltTokenExpired(dir, "dev"))

	// Already-expired token: expired.
	past := fakeJWT(t, time.Now().Add(-time.Hour).Unix())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte(past), 0600))
	assert.True(t, config.IsBoltTokenExpired(dir, "dev"))

	// Different env file untouched: still "no file" → expired.
	assert.True(t, config.IsBoltTokenExpired(dir, "prod"))

	// Malformed token (not 3 dot-separated parts): treated as expired.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte("not-a-jwt"), 0600))
	assert.True(t, config.IsBoltTokenExpired(dir, "dev"))
}
