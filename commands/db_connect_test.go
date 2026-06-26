package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/gorilla/websocket"
)

func TestStartDBConnect_SendsBoltTokenHeader(t *testing.T) {
	var gotAuth, gotBolt string
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBolt = r.Header.Get("X-Bolt-Token")
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Send an error frame so startDBConnect returns promptly.
		c.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","message":"stop"}`)) //nolint:errcheck
		c.Close()
	}))
	defer srv.Close()

	cfg := &config.Config{BackendURL: "http" + strings.TrimPrefix(srv.URL, "http")}
	_ = startDBConnect("orders-mysql", "zinka", cfg, "idtok", "bolttok")

	if gotAuth != "Bearer idtok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBolt != "bolttok" {
		t.Errorf("X-Bolt-Token = %q", gotBolt)
	}
}
