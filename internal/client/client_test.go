package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int { return &i }

func TestRunCommand_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/commands", r.URL.Path)
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(client.CommandResponse{
			RequestID: "req-1", Status: "success", ExitCode: intPtr(0),
			Stdout: "hello", Stderr: "", Truncated: false, DurationMs: 42,
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	resp, err := c.RunCommand(context.Background(), client.CommandRequest{
		InstanceID: "i-abc", Command: "ls",
	})
	require.NoError(t, err)
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, "hello", resp.Stdout)
	assert.Equal(t, int64(42), resp.DurationMs)
}

func TestRunCommand_SetsClientVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "ec2ctl/test", body["client_version"])
		json.NewEncoder(w).Encode(client.CommandResponse{Status: "success"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	_, err := c.RunCommand(context.Background(), client.CommandRequest{InstanceID: "i-abc", Command: "ls"})
	require.NoError(t, err)
}

func TestClassify_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/classify", r.URL.Path)
		json.NewEncoder(w).Encode(client.ClassifyResponse{Tier: "safe"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	resp, err := c.Classify(context.Background(), "ls -la", "i-abc")
	require.NoError(t, err)
	assert.Equal(t, "safe", resp.Tier)
}

func TestRunCommand_APIError_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "denied", "reason": "command denied"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	_, err := c.RunCommand(context.Background(), client.CommandRequest{InstanceID: "i-abc", Command: "rm -rf /"})
	require.Error(t, err)
	var apiErr *client.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.HTTPStatus)
	assert.Equal(t, "command denied", apiErr.Reason)
}

func TestCancelCommand_Success(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "req-1")
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	require.NoError(t, c.CancelCommand(context.Background(), "req-1"))
	assert.True(t, called)
}

func TestClient_NoAuthToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(client.ClassifyResponse{Tier: "safe"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "", "ec2ctl/test") // no token
	_, err := c.Classify(context.Background(), "ls", "i-abc")
	require.NoError(t, err)
}
