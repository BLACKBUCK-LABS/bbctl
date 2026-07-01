package commands

import (
	"errors"
	"testing"

	"github.com/gorilla/websocket"
)

func TestInterruptExits(t *testing.T) {
	cases := []struct {
		name         string
		midStatement bool
		line         string
		want         bool
	}{
		// The reported bug: Ctrl+C on the continuation prompt of a multi-line
		// statement (buffer active, current physical line empty) must NOT exit.
		{"mid-statement empty line cancels", true, "", false},
		{"mid-statement with text cancels", true, "select 1", false},
		// Fresh prompt, nothing typed and nothing buffered → Ctrl+C exits.
		{"fresh empty line exits", false, "", true},
		// Something typed but not yet a statement → cancel, don't exit.
		{"fresh partial line cancels", false, "sel", false},
		{"fresh whitespace-only line exits", false, "   ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := interruptExits(c.midStatement, c.line); got != c.want {
				t.Errorf("interruptExits(%v, %q) = %v, want %v", c.midStatement, c.line, got, c.want)
			}
		})
	}
}

func TestIsConnClosed(t *testing.T) {
	if !isConnClosed(&websocket.CloseError{Code: websocket.CloseAbnormalClosure}) {
		t.Error("abnormal-closure CloseError should be treated as connection closed")
	}
	if !isConnClosed(errors.New("websocket: close 1006 (abnormal closure): unexpected EOF")) {
		t.Error("1006 error string should be treated as connection closed")
	}
	if !isConnClosed(errors.New("write tcp: use of closed network connection")) {
		t.Error("closed network connection should be treated as connection closed")
	}
	if isConnClosed(nil) {
		t.Error("nil error is not a closed connection")
	}
	if isConnClosed(errors.New("readline init failed")) {
		t.Error("unrelated error must NOT be treated as connection closed")
	}
}
