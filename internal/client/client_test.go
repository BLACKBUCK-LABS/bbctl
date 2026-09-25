package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPostJSON_SendsBoltTokenHeaderWhenSet(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Bolt-Token")
		json.NewEncoder(w).Encode(client.CommandResponse{Status: "success"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	_, err := c.RunCommand(context.Background(), client.CommandRequest{InstanceID: "i-abc", Command: "ls"})
	require.NoError(t, err)
	assert.Empty(t, gotHeader, "X-Bolt-Token should be absent when not set")

	c.SetBoltToken("bolt-tok-123")
	_, err = c.RunCommand(context.Background(), client.CommandRequest{InstanceID: "i-abc", Command: "ls"})
	require.NoError(t, err)
	assert.Equal(t, "bolt-tok-123", gotHeader)
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

func TestInitUpload_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/upload/init", r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "i-abc", body["instance_id"])
		assert.Equal(t, float64(11), body["size_bytes"])
		json.NewEncoder(w).Encode(client.InitUploadResponse{
			UploadID: "up-1", PresignedPutURL: "https://s3.example/x",
			ChecksumSHA256B64: "abc=", ExpiresInSeconds: 3600,
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	resp, err := c.InitUpload(context.Background(), client.InitUploadRequest{
		InstanceID: "i-abc", AccountID: "acct", DestPath: "/tmp/f", Filename: "f",
		SizeBytes: 11, SHA256: "deadbeef",
	})
	require.NoError(t, err)
	assert.Equal(t, "up-1", resp.UploadID)
	assert.Equal(t, 3600, resp.ExpiresInSeconds)
}

func TestSubmitUpload_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/upload/submit", r.URL.Path)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "up-1", body["upload_id"])
		json.NewEncoder(w).Encode(client.SubmitUploadResponse{RequestID: "42", PortalURL: "https://portal/42"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	resp, err := c.SubmitUpload(context.Background(), "up-1")
	require.NoError(t, err)
	assert.Equal(t, "42", resp.RequestID)
	assert.Equal(t, "https://portal/42", resp.PortalURL)
}

func TestRetryUpload_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/upload/retry", r.URL.Path)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "42", body["request_id"])
		json.NewEncoder(w).Encode(client.RetryUploadResponse{RequestID: "42", Status: "EXECUTING"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "tok", "ec2ctl/test")
	resp, err := c.RetryUpload(context.Background(), "42")
	require.NoError(t, err)
	assert.Equal(t, "EXECUTING", resp.Status)
}

func TestPutPresigned_SendsChecksumHeaderAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "abc=", r.Header.Get("x-amz-checksum-sha256"))
		body, _ := io.ReadAll(r.Body)
		assert.Equal(t, "hello world", string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New("http://unused", "tok", "ec2ctl/test")
	err := c.PutPresigned(context.Background(), srv.URL, strings.NewReader("hello world"), 11, "abc=")
	require.NoError(t, err)
}

func TestPutPresigned_403ReturnsExpiredSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	}))
	defer srv.Close()

	c := client.New("http://unused", "tok", "ec2ctl/test")
	err := c.PutPresigned(context.Background(), srv.URL, strings.NewReader("x"), 1, "abc=")
	require.Error(t, err)
	assert.ErrorIs(t, err, client.ErrPresignedURLExpired)
}

func TestPutPresigned_OtherErrorIsNotExpiredSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := client.New("http://unused", "tok", "ec2ctl/test")
	err := c.PutPresigned(context.Background(), srv.URL, strings.NewReader("x"), 1, "abc=")
	require.Error(t, err)
	assert.NotErrorIs(t, err, client.ErrPresignedURLExpired)
}
