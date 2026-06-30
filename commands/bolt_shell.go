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
	"strconv"
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

// boltLine reconstructs the current visible prompt line from the remote's output
// stream. History is recorded from this (ground truth) rather than from typed
// input, so tab-completion and remote-side line edits are captured correctly.
//
// It models a single visible line: \n starts a fresh line (and forgets where the
// prompt ended), \r and \b and the common CSI cursor/erase sequences move or trim
// within it. markPrompt() records the column where user input begins (called when
// the user first types on a fresh prompt); command() returns everything after it.
type boltLine struct {
	buf          []byte
	cursor       int
	promptLen    int
	promptMarked bool
	insertMode   bool // IRM (CSI 4h) — printable chars insert instead of overwrite
}

func (t *boltLine) feed(data []byte) {
	i := 0
	for i < len(data) {
		b := data[i]
		if b == 0x1b && i+1 < len(data) && data[i+1] == '[' {
			j := i + 2
			for j < len(data) && !(data[j] >= 0x40 && data[j] <= 0x7e) {
				j++
			}
			if j < len(data) {
				t.applyCSI(string(data[i+2:j]), data[j])
				i = j + 1
			} else {
				i = j
			}
			continue
		}
		switch b {
		case '\r':
			t.cursor = 0
		case '\n':
			t.buf = t.buf[:0]
			t.cursor = 0
			t.promptLen = 0
			t.promptMarked = false
		case '\b':
			if t.cursor > 0 {
				t.cursor--
			}
		default:
			if b >= 0x20 {
				if t.cursor >= len(t.buf) {
					t.buf = append(t.buf, b)
				} else if t.insertMode {
					// IRM active — shift right and insert.
					t.buf = append(t.buf, 0)
					copy(t.buf[t.cursor+1:], t.buf[t.cursor:])
					t.buf[t.cursor] = b
				} else {
					t.buf[t.cursor] = b
				}
				t.cursor++
			}
		}
		i++
	}
}

func (t *boltLine) applyCSI(params string, final byte) {
	switch final {
	case 'K': // erase in line
		switch csiNum(params, 0) {
		case 0: // cursor → end
			if t.cursor < len(t.buf) {
				t.buf = t.buf[:t.cursor]
			}
		case 2: // whole line
			t.buf = t.buf[:0]
			t.cursor = 0
		}
	case 'D': // cursor left
		t.cursor -= csiNum(params, 1)
		if t.cursor < 0 {
			t.cursor = 0
		}
	case 'C': // cursor right
		t.cursor += csiNum(params, 1)
		if t.cursor > len(t.buf) {
			t.cursor = len(t.buf)
		}
	case 'G': // cursor to absolute column (1-indexed)
		t.cursor = csiNum(params, 1) - 1
		if t.cursor < 0 {
			t.cursor = 0
		}
		if t.cursor > len(t.buf) {
			t.cursor = len(t.buf)
		}
	case '@': // ICH — insert N blanks at the cursor (tail shifts right)
		n := csiNum(params, 1)
		if t.cursor <= len(t.buf) {
			blanks := make([]byte, n)
			for k := range blanks {
				blanks[k] = ' '
			}
			t.buf = append(t.buf[:t.cursor], append(blanks, t.buf[t.cursor:]...)...)
		}
	case 'P': // DCH — delete N chars at the cursor (tail shifts left)
		n := csiNum(params, 1)
		if t.cursor < len(t.buf) {
			end := t.cursor + n
			if end > len(t.buf) {
				end = len(t.buf)
			}
			t.buf = append(t.buf[:t.cursor], t.buf[end:]...)
		}
	case 'h': // SM — set mode; 4 = IRM (insert mode)
		if params == "4" {
			t.insertMode = true
		}
	case 'l': // RM — reset mode
		if params == "4" {
			t.insertMode = false
		}
	}
}

// markPrompt records where the command starts (idempotent until the next \n).
func (t *boltLine) markPrompt() {
	if !t.promptMarked {
		t.promptLen = len(t.buf)
		t.promptMarked = true
	}
}

// command returns the text typed after the prompt, or "" if no prompt was marked.
func (t *boltLine) command() string {
	if !t.promptMarked || t.promptLen > len(t.buf) {
		return ""
	}
	return string(t.buf[t.promptLen:])
}

func csiNum(params string, def int) int {
	if params == "" {
		return def
	}
	if idx := strings.IndexByte(params, ';'); idx >= 0 {
		params = params[:idx]
	}
	if n, err := strconv.Atoi(params); err == nil && n >= 0 {
		return n
	}
	return def
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

	// Shared state guarded by stateMu: the output-driven line tracker (source of
	// truth for history) and the terminal-mode flags. While a full-screen app
	// (vim/less/top/nano) or application-cursor mode is active we must not record
	// history, join pastes, or hijack arrow keys.
	var stateMu sync.Mutex
	var altScreen, appCursorMode bool
	tracker := &boltLine{}

	// stdin → relay INPUT
	//
	// Input is forwarded verbatim EXCEPT:
	//   • pastes — joined into a single line so the PTY never runs partial lines.
	//   • up/down at the shell prompt — handled locally for history.
	// History is NOT derived from typed bytes — it is read from the output line
	// tracker (what the remote actually echoed), so tab-completion and remote-side
	// edits are captured correctly. Enter still just forwards \r.
	go func() {
		buf := make([]byte, 4096)
		var collectingPaste bool
		var pasteBuf []byte
		startMarker := []byte("\x1b[200~")
		endMarker := []byte("\x1b[201~")
		histIdx := len(history)

		markPrompt := func() {
			stateMu.Lock()
			tracker.markPrompt()
			stateMu.Unlock()
		}

		// replaceLine clears the remote prompt line and types s in its place.
		// Ctrl-E (to end) + Ctrl-U (kill to start) clears regardless of cursor.
		replaceLine := func(s string) {
			markPrompt()
			payload := append([]byte{0x05, 0x15}, []byte(s)...)
			sendFrame(boltMsgInput, payload) //nolint:errcheck
		}

		// recordHistory reads the visible command from the tracker and appends it.
		recordHistory := func() {
			stateMu.Lock()
			cmd := strings.TrimSpace(tracker.command())
			stateMu.Unlock()
			if cmd != "" && (len(history) == 0 || history[len(history)-1] != cmd) {
				history = append(history, cmd)
				if histFile != nil {
					histFile.WriteString(cmd + "\n") //nolint:errcheck
				}
			}
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
			stateMu.Lock()
			inFullScreen := altScreen
			stateMu.Unlock()
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
						markPrompt()
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
					markPrompt()
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
						markPrompt()
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
					markPrompt()
					if err := sendFrame(boltMsgInput, []byte(joined)); err != nil {
						errCh <- err
						return
					}
				}
				os.Stdout.Write([]byte("\x1b[?2004h")) //nolint:errcheck
				if endsWithCR {
					time.Sleep(25 * time.Millisecond) // let the echo settle into the tracker
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
				stateMu.Lock()
				inApp := altScreen || appCursorMode
				stateMu.Unlock()
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

			// Enter: record the visible command from the tracker, then forward \r.
			if data[n-1] == 0x0d {
				if n > 1 {
					markPrompt()
					if err := sendFrame(boltMsgInput, data[:n-1]); err != nil {
						errCh <- err
						return
					}
				}
				time.Sleep(25 * time.Millisecond) // let the echo settle into the tracker
				recordHistory()
				if err := sendFrame(boltMsgInput, []byte{0x0d}); err != nil {
					errCh <- err
					return
				}
				continue
			}

			// Everything else: mark the prompt (first keystroke) and forward.
			markPrompt()
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
				// Feed the line tracker (history source of truth) and track
				// terminal modes (so we don't hijack arrows in full-screen apps).
				stateMu.Lock()
				tracker.feed(f.Payload)
				stateMu.Unlock()
				updateTerminalModes(f.Payload, &stateMu, &altScreen, &appCursorMode)
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
