package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
)

func TestNewBackendClient_SendsSavedBoltToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".bbctl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveBoltToken(dir, "dev", "bolt-jwt\n"); err != nil {
		t.Fatal(err)
	}

	var gotBolt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBolt = r.Header.Get("X-Bolt-Token")
		json.NewEncoder(w).Encode(client.CommandResponse{Status: "success"})
	}))
	defer srv.Close()

	c := newBackendClient(&config.Config{BackendURL: srv.URL}, "google-token")
	if _, err := c.RunCommand(context.Background(), client.CommandRequest{InstanceID: "i-1", Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	if gotBolt != "bolt-jwt" {
		t.Fatalf("X-Bolt-Token = %q, want bolt-jwt", gotBolt)
	}
}

func TestNewBackendClient_NoBoltLogin_NoHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Bolt-Token"]
		json.NewEncoder(w).Encode(client.CommandResponse{Status: "success"})
	}))
	defer srv.Close()

	c := newBackendClient(&config.Config{BackendURL: srv.URL}, "google-token")
	if _, err := c.RunCommand(context.Background(), client.CommandRequest{InstanceID: "i-1", Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("X-Bolt-Token must not be sent without a BOLT login")
	}
}
