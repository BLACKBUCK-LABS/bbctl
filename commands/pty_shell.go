package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

const (
	typeInput  = "input"
	typeOutput = "output"
	typeResize = "resize"
	typeExit   = "exit"
)

type shellFrame struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

type inputPayload struct {
	Data []byte `json:"data"`
}

type outputPayload struct {
	Data   []byte `json:"data"`
	Stream string `json:"stream"`
}

type resizePayload struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func runPTYShell(cfg *config.Config, token, instanceID, accountID string) error {
	cols, rows := uint16(220), uint16(50)
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols = uint16(w)
		rows = uint16(h)
	}

	wsURL := strings.Replace(cfg.BackendURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	u, err := url.Parse(wsURL + "/v1/shell")
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	q.Set("instance_id", instanceID)
	q.Set("account_id", accountID)
	q.Set("cols", fmt.Sprintf("%d", cols))
	q.Set("rows", fmt.Sprintf("%d", rows))
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.Dial(u.String(), http.Header{
		"Authorization": {"Bearer " + token},
	})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return fmt.Errorf("unauthorized. Run: bbctl login")
			case http.StatusServiceUnavailable:
				return fmt.Errorf("no agent connected to %s. Is bbshell-agent installed?", instanceID)
			case http.StatusBadRequest:
				return fmt.Errorf("bad request: check instance_id and account_id")
			}
		}
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	var writeMu sync.Mutex
	writeFrame := func(f shellFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(f)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("PTY shell requires an interactive terminal. Cannot use --pty in a non-TTY environment.")
	}

	fmt.Fprintf(os.Stderr, "Connecting to %s... (Ctrl+] to detach)\n", instanceID)

	// CRITICAL: restore terminal on any exit path.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 3)

	// stdin → TypeInput frames
	go func() {
		buf := make([]byte, 256)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := os.Stdin.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			if n == 0 {
				continue
			}
			for i := 0; i < n; i++ {
				if buf[i] == 0x1d { // Ctrl+] — detach
					errCh <- nil
					return
				}
			}
			payload, _ := json.Marshal(inputPayload{Data: buf[:n]})
			frame := shellFrame{Type: typeInput, Payload: payload}
			if err := writeFrame(frame); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// TypeOutput frames → stdout
	go func() {
		for {
			var frame shellFrame
			if err := conn.ReadJSON(&frame); err != nil {
				if websocket.IsCloseError(err,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway) {
					errCh <- nil
				} else {
					errCh <- err
				}
				return
			}
			switch frame.Type {
			case typeOutput:
				var p outputPayload
				if err := json.Unmarshal(frame.Payload, &p); err != nil {
					continue
				}
				os.Stdout.Write(p.Data) //nolint:errcheck
			case typeExit:
				errCh <- nil
				return
			case "error":
				var ep struct {
					Message string `json:"message"`
					Code    string `json:"code"`
				}
				if err := json.Unmarshal(frame.Payload, &ep); err == nil {
					fmt.Fprintf(os.Stderr, "\r\n⛔ %s\r\n", ep.Message)
				}
				errCh <- fmt.Errorf("shell error")
				return
			}
		}
	}()

	// SIGWINCH → TypeResize frames
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				w, h, err := term.GetSize(int(os.Stdout.Fd()))
				if err != nil {
					continue
				}
				payload, _ := json.Marshal(resizePayload{Cols: uint16(w), Rows: uint16(h)})
				frame := shellFrame{Type: typeResize, Payload: payload}
				writeFrame(frame) //nolint:errcheck — best effort
			}
		}
	}()

	err = <-errCh
	fmt.Fprintf(os.Stdout, "\r\n")
	if err != nil && !websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway) {
		fmt.Fprintf(os.Stderr, "Shell disconnected: %v\r\n", err)
		return nil
	}
	return nil
}
