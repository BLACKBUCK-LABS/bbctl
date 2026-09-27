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

// fakeJWTNoExp mirrors the real BOLT session token shape: a valid JWT with
// no "exp" claim at all (only "iat"/"randomizer").
func fakeJWTNoExp(t *testing.T) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload, err := json.Marshal(map[string]any{"iat": time.Now().Unix(), "randomizer": "abc-123"})
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

	// Real BOLT token shape: valid JWT, no "exp" claim — must NOT be treated
	// as expired (this is the actual production shape, regression for the
	// exp==0-means-epoch bug).
	noExp := fakeJWTNoExp(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte(noExp), 0600))
	assert.False(t, config.IsBoltTokenExpired(dir, "dev"))
}

// TestIsBoltTokenExpired_PayloadLengthIndependent regresses the base64
// padding bug: decodeJWTExp used to append "=" padding and then decode with
// base64.RawURLEncoding (the *unpadded* variant, which rejects any "="),
// failing whenever the payload's length wasn't already a multiple of 4 —
// i.e. depending on incidental claim-value lengths like a randomizer UUID.
// Varying the randomizer's length exercises every payload-length%4 case.
func TestIsBoltTokenExpired_PayloadLengthIndependent(t *testing.T) {
	dir := t.TempDir()
	randomizers := []string{"a", "ab", "abc", "abcd", "abcde", "abcdef", "abcdefg"}
	for _, r := range randomizers {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
		payload, err := json.Marshal(map[string]any{"iat": time.Now().Unix(), "randomizer": r})
		require.NoError(t, err)
		body := base64.RawURLEncoding.EncodeToString(payload)
		token := fmt.Sprintf("%s.%s.sig", header, body)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte(token), 0600))
		assert.False(t, config.IsBoltTokenExpired(dir, "dev"), "randomizer=%q (payload len %% 4 = %d)", r, len(body)%4)
	}
}
