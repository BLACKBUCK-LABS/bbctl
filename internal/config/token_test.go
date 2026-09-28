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

// fakeJWTNoExp builds a JWT payload with no "exp" claim at all, matching
// real BOLT tokens (iat/randomizer/... but never exp).
func fakeJWTNoExp(t *testing.T, randomizer string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{
		"iat": time.Now().Unix(), "randomizer": randomizer,
	})
	require.NoError(t, err)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s.%s.sig", header, body)
}

func TestIsBoltTokenExpired_NoExpClaimIsNotExpired(t *testing.T) {
	dir := t.TempDir()
	tok := fakeJWTNoExp(t, "b0e3d086-5593-48e6-bb1c-8d2547c1f4e9")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte(tok), 0600))
	assert.False(t, config.IsBoltTokenExpired(dir, "dev"))
}

// TestIsBoltTokenExpired_PayloadLengthIndependent exercises every
// len(payload)%4 case a real randomizer UUID can produce — the previous
// pad-before-decode logic broke RawURLEncoding (which rejects any padding)
// whenever the base64 payload wasn't already a multiple of 4.
func TestIsBoltTokenExpired_PayloadLengthIndependent(t *testing.T) {
	dir := t.TempDir()
	randomizers := []string{
		"a", "ab", "abc", "abcd", "abcde",
		"b0e3d086-5593-48e6-bb1c-8d2547c1f4e9",
		"b0e3d086-5593-48e6-bb1c-8d2547c1f4e91",
	}
	for _, r := range randomizers {
		tok := fakeJWTNoExp(t, r)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte(tok), 0600))
		assert.False(t, config.IsBoltTokenExpired(dir, "dev"), "randomizer=%q", r)
	}
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
