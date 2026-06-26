package commands

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

// ─── BOLT relay frame protocol (binary V1) ──────────────────────────────────
//
// Layout: version(1) + msgType(1) + seqNo(8) + sessionID(36) + payloadLen(4) + crc32(4) + payload(N)
const (
	boltMsgInput     byte = 0x01
	boltMsgOutput    byte = 0x02
	boltMsgResize    byte = 0x04
	boltMsgClose     byte = 0x06
	boltMsgKeepalive byte = 0x07

	boltFrameVersion byte = 0x01
	boltHeaderSize        = 54
)

func encodeBoltFrame(msgType byte, seqNo uint64, sessionID string, payload []byte) []byte {
	buf := make([]byte, boltHeaderSize+len(payload))
	buf[0] = boltFrameVersion
	buf[1] = msgType
	binary.BigEndian.PutUint64(buf[2:10], seqNo)
	copy(buf[10:46], fmt.Sprintf("%-36s", sessionID)) // space-pad to 36 bytes
	binary.BigEndian.PutUint32(buf[46:50], uint32(len(payload)))
	binary.BigEndian.PutUint32(buf[50:54], crc32.ChecksumIEEE(payload))
	copy(buf[54:], payload)
	return buf
}

type boltFrame struct {
	MsgType   byte
	SeqNo     uint64
	SessionID string
	Payload   []byte
}

func decodeBoltFrame(data []byte) (*boltFrame, error) {
	if len(data) < boltHeaderSize {
		return nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}
	if data[0] != boltFrameVersion {
		return nil, fmt.Errorf("unknown frame version: 0x%02x", data[0])
	}
	payloadLen := binary.BigEndian.Uint32(data[46:50])
	if uint32(len(data)) < uint32(boltHeaderSize)+payloadLen {
		return nil, fmt.Errorf("frame truncated")
	}
	payload := make([]byte, payloadLen)
	copy(payload, data[boltHeaderSize:boltHeaderSize+payloadLen])
	if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(data[50:54]) {
		return nil, fmt.Errorf("CRC mismatch")
	}
	return &boltFrame{
		MsgType:   data[1],
		SeqNo:     binary.BigEndian.Uint64(data[2:10]),
		SessionID: strings.TrimRight(string(data[10:46]), " "),
		Payload:   payload,
	}, nil
}

// joinBoltPaste flattens a multi-line pasted bash command to a single line.
// Trailing-space-before-backslash continuations ("cmd \ \n") are handled.
func joinBoltPaste(buf []byte) string {
	s := string(buf)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	var parts []string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			parts = append(parts, trimmed[:len(trimmed)-1])
		} else if len(parts) > 0 || line != "" {
			parts = append(parts, line)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// ─── session start ──────────────────────────────────────────────────────────

type boltSessionResp struct {
	SessionID string `json:"session_id"`
	WSURL     string `json:"ws_url"`
}

func startBoltSession(ctx context.Context, relayURL, token, instanceID string) (*boltSessionResp, error) {
	body, _ := json.Marshal(map[string]string{"instance_id": instanceID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		relayURL+"/session/start", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("session/start: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("session/start returned HTTP %d", resp.StatusCode)
	}
	var result boltSessionResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("session/start: parse response: %w", err)
	}
	if result.SessionID == "" || result.WSURL == "" {
		return nil, fmt.Errorf("session/start: missing session_id or ws_url in response")
	}
	return &result, nil
}

// ─── main entry point ───────────────────────────────────────────────────────

// runBoltShell opens a BOLT relay PTY session against instanceID.
// relayURL is the relay base (default: the dev backend URL); token is the
// environment's BOLT JWT (bolt_token_dev / bolt_token_prod).
func runBoltShell(relayURL, token, instanceID string) error {
	if relayURL == "" {
		return fmt.Errorf("relay URL not configured")
	}

	fmt.Fprintf(os.Stderr, "⚡ Starting BOLT session for %s...\n", instanceID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	session, err := startBoltSession(ctx, relayURL, token, instanceID)
	cancel()
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, wsResp, err := dialer.Dial(session.WSURL, http.Header{
		"Authorization": {"Token " + token},
	})
	if err != nil {
		if wsResp != nil {
			switch wsResp.StatusCode {
			case http.StatusConflict:
				return fmt.Errorf("session %s already has an active client (409)", session.SessionID)
			case http.StatusUnauthorized:
				return fmt.Errorf("unauthorized — run: bbctl login")
			}
		}
		return fmt.Errorf("connect relay: %w", err)
	}
	defer conn.Close()

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("BOLT requires an interactive terminal")
	}

	cols, rows := uint16(220), uint16(50)
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = uint16(w), uint16(h)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	// Enable bracketed paste mode locally so pasted multi-line text is wrapped in
	// ESC[200~...ESC[201~ and embedded \r is never mistaken for Enter.
	os.Stdout.Write([]byte("\x1b[?2004h")) //nolint:errcheck
	defer func() {
		os.Stdout.Write([]byte("\x1b[?2004l")) //nolint:errcheck
		term.Restore(fd, oldState)
	}()

	var seqNo uint64
	var writeMu sync.Mutex
	sendFrame := func(msgType byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		seqNo++
		return conn.WriteMessage(websocket.BinaryMessage,
			encodeBoltFrame(msgType, seqNo, session.SessionID, payload))
	}

	// Initial resize so the remote PTY matches our terminal.
	resizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint16(resizeBuf[0:2], rows)
	binary.BigEndian.PutUint16(resizeBuf[2:4], cols)
	if err := sendFrame(boltMsgResize, resizeBuf); err != nil {
		return fmt.Errorf("send initial resize: %w", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	errCh := make(chan error, 3)

	// ── Local command history (client-side up/down) ───────────────────────────
	// The remote agent does not act on arrow keys, so we keep history locally and
	// rewrite the prompt line ourselves. History persists across sessions.
	history, histPath := loadBoltHistory()
	var histFile *os.File
	if histPath != "" {
		histFile, _ = os.OpenFile(histPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if histFile != nil {
			defer histFile.Close()
		}
	}

	// modeMu guards the terminal-mode flags written by the output goroutine and
	// read by the input goroutine. While a full-screen app (vim/less/top) or
	// application-cursor mode is active, we must NOT intercept arrow keys.
	var modeMu sync.Mutex
	var altScreen, appCursorMode bool

	// stdin → relay INPUT
	//
	// All input is forwarded verbatim EXCEPT:
	//   • pastes — joined into a single line so the PTY never runs partial lines.
	//   • up/down at the shell prompt — handled locally for history (see above).
	// We mirror typed bytes into lineBuf only to record history on Enter and to
	// know what to clear when recalling. Enter still just forwards \r — the
	// remote bash owns its readline buffer, so we never re-send the command.
	go func() {
		buf := make([]byte, 4096)
		var collectingPaste bool
		var pasteBuf []byte
		startMarker := []byte("\x1b[200~")
		endMarker := []byte("\x1b[201~")

		var lineBuf []byte
		cursor := 0 // insertion point within lineBuf — tracks the remote cursor
		histIdx := len(history)

		// mirror updates lineBuf + cursor from forwarded bytes so the recorded
		// history matches what is actually on the remote line — including
		// mid-line edits via arrows, Home/End, Delete, and the readline controls.
		mirror := func(d []byte) {
			i := 0
			for i < len(d) {
				b := d[i]
				if b == 0x1b { // ESC — parse a CSI / SS3 sequence
					if i+1 < len(d) && (d[i+1] == '[' || d[i+1] == 'O') {
						j := i + 2
						for j < len(d) && !(d[j] >= 0x40 && d[j] <= 0x7e) {
							j++
						}
						if j >= len(d) {
							i = j
							continue
						}
						final := d[j]
						params := string(d[i+2 : j])
						switch final {
						case 'D': // left
							if cursor > 0 {
								cursor--
							}
						case 'C': // right
							if cursor < len(lineBuf) {
								cursor++
							}
						case 'H': // Home
							cursor = 0
						case 'F': // End
							cursor = len(lineBuf)
						case '~':
							switch params {
							case "1", "7": // Home
								cursor = 0
							case "4", "8": // End
								cursor = len(lineBuf)
							case "3": // Delete (forward)
								if cursor < len(lineBuf) {
									lineBuf = append(lineBuf[:cursor], lineBuf[cursor+1:]...)
								}
							}
						}
						i = j + 1
						continue
					}
					i++
					continue
				}
				switch {
				case b == 0x08 || b == 0x7f: // backspace
					if cursor > 0 {
						lineBuf = append(lineBuf[:cursor-1], lineBuf[cursor:]...)
						cursor--
					}
				case b == 0x01: // Ctrl-A — start of line
					cursor = 0
				case b == 0x05: // Ctrl-E — end of line
					cursor = len(lineBuf)
				case b == 0x15: // Ctrl-U — kill from cursor to start
					lineBuf = append([]byte(nil), lineBuf[cursor:]...)
					cursor = 0
				case b == 0x0b: // Ctrl-K — kill from cursor to end
					lineBuf = lineBuf[:cursor]
				case b == 0x03: // Ctrl-C — abandon the line
					lineBuf = lineBuf[:0]
					cursor = 0
				case b >= 0x20: // printable — insert at cursor
					lineBuf = append(lineBuf, 0)
					copy(lineBuf[cursor+1:], lineBuf[cursor:])
					lineBuf[cursor] = b
					cursor++
				}
				i++
			}
		}

		// setLine replaces the local buffer (used for paste/history recall).
		setLine := func(s string) {
			lineBuf = append(lineBuf[:0], []byte(s)...)
			cursor = len(lineBuf)
		}

		// replaceLine clears the remote prompt line and types s in its place.
		// Ctrl-E (to end) + Ctrl-U (kill to start) clears regardless of cursor.
		replaceLine := func(s string) {
			payload := append([]byte{0x05, 0x15}, []byte(s)...)
			sendFrame(boltMsgInput, payload) //nolint:errcheck
			setLine(s)
		}

		recordHistory := func() {
			cmd := strings.TrimSpace(string(lineBuf))
			if cmd != "" && (len(history) == 0 || history[len(history)-1] != cmd) {
				history = append(history, cmd)
				if histFile != nil {
					histFile.WriteString(cmd + "\n") //nolint:errcheck
				}
			}
			lineBuf = lineBuf[:0]
			cursor = 0
			histIdx = len(history)
		}

		for {
			select {
			case <-runCtx.Done():
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
			data := buf[:n]

			// Full-screen interactive app (nano, vim, less, top, …) is active:
			// behave like a plain pass-through terminal. Forward raw bytes — do
			// NOT join pastes (newlines must survive in an editor), do NOT record
			// history, do NOT hijack arrows. The remote app owns the screen.
			modeMu.Lock()
			inFullScreen := altScreen
			modeMu.Unlock()
			if inFullScreen {
				if err := sendFrame(boltMsgInput, append([]byte(nil), data...)); err != nil {
					errCh <- err
					return
				}
				continue
			}

			// Bracketed-paste collection in progress.
			if collectingPaste {
				if endIdx := bytes.Index(data, endMarker); endIdx >= 0 {
					pasteBuf = append(pasteBuf, data[:endIdx]...)
					collectingPaste = false
					joined := joinBoltPaste(pasteBuf)
					pasteBuf = pasteBuf[:0]
					if joined != "" {
						lineBuf = append(lineBuf, []byte(joined)...)
						cursor = len(lineBuf)
						histIdx = len(history)
						if err := sendFrame(boltMsgInput, []byte(joined)); err != nil {
							errCh <- err
							return
						}
					}
					if rest := data[endIdx+len(endMarker):]; len(rest) > 0 {
						if err := sendFrame(boltMsgInput, rest); err != nil {
							errCh <- err
							return
						}
					}
				} else {
					pasteBuf = append(pasteBuf, data...)
				}
				continue
			}

			// Start of a bracketed paste.
			if startIdx := bytes.Index(data, startMarker); startIdx >= 0 {
				if startIdx > 0 {
					mirror(data[:startIdx])
					if err := sendFrame(boltMsgInput, data[:startIdx]); err != nil {
						errCh <- err
						return
					}
				}
				collectingPaste = true
				pasteBuf = pasteBuf[:0]
				after := data[startIdx+len(startMarker):]
				if endIdx := bytes.Index(after, endMarker); endIdx >= 0 {
					pasteBuf = append(pasteBuf[:0], after[:endIdx]...)
					collectingPaste = false
					joined := joinBoltPaste(pasteBuf)
					pasteBuf = pasteBuf[:0]
					if joined != "" {
						lineBuf = append(lineBuf, []byte(joined)...)
						cursor = len(lineBuf)
						histIdx = len(history)
						if err := sendFrame(boltMsgInput, []byte(joined)); err != nil {
							errCh <- err
							return
						}
					}
					if rest := after[endIdx+len(endMarker):]; len(rest) > 0 {
						if err := sendFrame(boltMsgInput, rest); err != nil {
							errCh <- err
							return
						}
					}
				} else {
					pasteBuf = append(pasteBuf, after...)
				}
				continue
			}

			// Non-bracketed paste: a \r before the last byte is the tell-tale
			// (inner shells like kubectl exec disable bracketed paste mode).
			if n > 1 && bytes.IndexByte(data[:n-1], 0x0d) >= 0 {
				endsWithCR := data[n-1] == 0x0d
				content := data
				if endsWithCR {
					content = data[:n-1]
				}
				joined := joinBoltPaste(content)
				if joined != "" {
					lineBuf = append(lineBuf, []byte(joined)...)
						cursor = len(lineBuf)
					if err := sendFrame(boltMsgInput, []byte(joined)); err != nil {
						errCh <- err
						return
					}
				}
				os.Stdout.Write([]byte("\x1b[?2004h")) //nolint:errcheck
				if endsWithCR {
					recordHistory()
					if err := sendFrame(boltMsgInput, []byte{0x0d}); err != nil {
						errCh <- err
						return
					}
				}
				continue
			}

			// History navigation: up/down arrow (CSI form) at the shell prompt.
			if n == 3 && data[0] == 0x1b && data[1] == '[' && (data[2] == 'A' || data[2] == 'B') {
				modeMu.Lock()
				inApp := altScreen || appCursorMode
				modeMu.Unlock()
				if !inApp && len(history) > 0 {
					if data[2] == 'A' { // up
						if histIdx > 0 {
							histIdx--
							replaceLine(history[histIdx])
						}
					} else { // down
						if histIdx < len(history) {
							histIdx++
							if histIdx == len(history) {
								replaceLine("")
							} else {
								replaceLine(history[histIdx])
							}
						}
					}
					continue
				}
				// In a full-screen app — forward the arrow untouched.
				if err := sendFrame(boltMsgInput, append([]byte(nil), data...)); err != nil {
					errCh <- err
					return
				}
				continue
			}

			// Enter: record the line into history, then forward verbatim.
			if data[n-1] == 0x0d {
				if n > 1 {
					mirror(data[:n-1])
				}
				recordHistory()
				if err := sendFrame(boltMsgInput, append([]byte(nil), data...)); err != nil {
					errCh <- err
					return
				}
				continue
			}

			// Everything else: mirror into lineBuf and forward verbatim.
			mirror(data)
			if err := sendFrame(boltMsgInput, append([]byte(nil), data...)); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// relay → stdout
	go func() {
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway) {
					errCh <- nil
				} else {
					errCh <- err
				}
				return
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			f, err := decodeBoltFrame(data)
			if err != nil {
				continue
			}
			switch f.MsgType {
			case boltMsgOutput:
				os.Stdout.Write(f.Payload) //nolint:errcheck
				// Track terminal modes so the input goroutine knows when NOT to
				// hijack arrow keys (full-screen apps, application-cursor mode).
				updateTerminalModes(f.Payload, &modeMu, &altScreen, &appCursorMode)
				// Re-enable bracketed paste if an inner shell disabled it.
				if bytes.Contains(f.Payload, []byte("\x1b[?2004l")) {
					os.Stdout.Write([]byte("\x1b[?2004h")) //nolint:errcheck
				}
			case boltMsgClose:
				reason := strings.TrimSpace(string(f.Payload))
				if reason != "" {
					fmt.Fprintf(os.Stdout, "\r\n[session closed: %s]\r\n", reason)
				}
				errCh <- nil
				return
			case boltMsgKeepalive:
				// ignored
			}
		}
	}()

	// SIGWINCH → resize
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-sigCh:
				w, h, err := term.GetSize(int(os.Stdout.Fd()))
				if err != nil {
					continue
				}
				p := make([]byte, 4)
				binary.BigEndian.PutUint16(p[0:2], uint16(h))
				binary.BigEndian.PutUint16(p[2:4], uint16(w))
				sendFrame(boltMsgResize, p) //nolint:errcheck
			}
		}
	}()

	<-errCh
	fmt.Fprintf(os.Stdout, "\r\n")
	return nil
}

// loadBoltHistory reads ~/.bbctl_bolt_history into a slice (oldest first) and
// returns the slice and the file path (empty if the home dir is unavailable).
func loadBoltHistory() ([]string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ""
	}
	path := filepath.Join(home, ".bbctl_bolt_history")
	data, _ := os.ReadFile(path)
	var hist []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			hist = append(hist, line)
		}
	}
	return hist, path
}

// updateTerminalModes scans remote output for mode-change sequences and updates
// the alt-screen / application-cursor flags. These tell the input goroutine when
// a full-screen app is active so it does NOT hijack arrow keys for history.
func updateTerminalModes(payload []byte, mu *sync.Mutex, altScreen, appCursor *bool) {
	mu.Lock()
	defer mu.Unlock()
	for _, seq := range [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")} {
		if bytes.Contains(payload, seq) {
			*altScreen = true
		}
	}
	for _, seq := range [][]byte{[]byte("\x1b[?1049l"), []byte("\x1b[?1047l"), []byte("\x1b[?47l")} {
		if bytes.Contains(payload, seq) {
			*altScreen = false
		}
	}
	if bytes.Contains(payload, []byte("\x1b[?1h")) {
		*appCursor = true
	}
	if bytes.Contains(payload, []byte("\x1b[?1l")) {
		*appCursor = false
	}
}

// boltEnvAndToken resolves which environment (dev/prod) is active based on the
// resolved backend URL, and loads that environment's BOLT token. The relay base
// is the backend URL itself ("relay on the bbctl-dev url").
func boltEnvAndToken(cfg *config.Config, configDir string) (relayURL, token string, err error) {
	env := activeEnv // "dev" by default; "prod" when invoked as "bbctl prod ..."
	tok, err := config.LoadBoltToken(configDir, env)
	if err != nil {
		return "", "", fmt.Errorf("no BOLT %s token — run: bbctl login", env)
	}
	// Relay base: explicit relay_url override (e.g. ngrok for testing) wins,
	// otherwise fall back to the environment's backend URL.
	relay := cfg.BackendURL
	if env == "prod" {
		if cfg.ProdRelayURL != "" {
			relay = cfg.ProdRelayURL
		}
	} else if cfg.RelayURL != "" {
		relay = cfg.RelayURL
	}
	return relay, tok, nil
}
