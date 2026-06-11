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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

const (
	relayMsgInput     byte = 0x01
	relayMsgOutput    byte = 0x02
	relayMsgResize    byte = 0x04
	relayMsgClose     byte = 0x06
	relayMsgKeepalive byte = 0x07

	relayFrameVersion byte = 0x01
	relayHeaderSize        = 54
)

// encodeRelayFrame builds the binary V1 frame.
//
// Layout: version(1) + msgType(1) + seqNo(8) + sessionID(36) + payloadLen(4) + crc32(4) + payload(N)
func encodeRelayFrame(msgType byte, seqNo uint64, sessionID string, payload []byte) []byte {
	buf := make([]byte, relayHeaderSize+len(payload))
	buf[0] = relayFrameVersion
	buf[1] = msgType
	binary.BigEndian.PutUint64(buf[2:10], seqNo)
	sid := fmt.Sprintf("%-36s", sessionID) // space-pad to 36 bytes
	copy(buf[10:46], sid)
	binary.BigEndian.PutUint32(buf[46:50], uint32(len(payload)))
	binary.BigEndian.PutUint32(buf[50:54], crc32.ChecksumIEEE(payload))
	copy(buf[54:], payload)
	return buf
}

type relayFrame struct {
	MsgType   byte
	SeqNo     uint64
	SessionID string
	Payload   []byte
}

func decodeRelayFrame(data []byte) (*relayFrame, error) {
	if len(data) < relayHeaderSize {
		return nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}
	if data[0] != relayFrameVersion {
		return nil, fmt.Errorf("unknown frame version: 0x%02x", data[0])
	}
	payloadLen := binary.BigEndian.Uint32(data[46:50])
	if uint32(len(data)) < uint32(relayHeaderSize)+payloadLen {
		return nil, fmt.Errorf("frame truncated")
	}
	payload := make([]byte, payloadLen)
	copy(payload, data[relayHeaderSize:relayHeaderSize+payloadLen])
	if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(data[50:54]) {
		return nil, fmt.Errorf("CRC mismatch")
	}
	return &relayFrame{
		MsgType:   data[1],
		SeqNo:     binary.BigEndian.Uint64(data[2:10]),
		SessionID: strings.TrimRight(string(data[10:46]), " "),
		Payload:   payload,
	}, nil
}

// lineTracker keeps track of what is visible on the current terminal line
// by interpreting the raw VT100 bytes that the relay echoes back as OUTPUT_DATA.
// This is necessary because history (up-arrow), tab completion, and backspace
// all rewrite the line via escape sequences — we cannot track just what the user typed.
// lineTracker maintains the visible terminal line by parsing VT100 output bytes.
// termWidth is required for cursor-up/down support when long commands wrap physical lines.
type lineTracker struct {
	buf       []byte
	cursor    int
	termWidth int
}

func newLineTracker(termWidth int) *lineTracker {
	if termWidth <= 0 {
		termWidth = 80
	}
	return &lineTracker{termWidth: termWidth}
}

func (t *lineTracker) feed(data []byte) {
	i := 0
	for i < len(data) {
		b := data[i]

		// CSI escape sequence: ESC [
		if b == 0x1b && i+1 < len(data) && data[i+1] == '[' {
			j := i + 2
			paramStart := j
			for j < len(data) && !isCSIFinalByte(data[j]) {
				j++
			}
			if j < len(data) {
				t.applyCSI(string(data[paramStart:j]), data[j])
				i = j + 1
			} else {
				i = j
			}
			continue
		}

		switch b {
		case '\r': // cursor to column 0
			t.cursor = 0
		case '\n': // new line — bash prompt is on a fresh line, reset
			t.buf = t.buf[:0]
			t.cursor = 0
		case '\b', 0x7f: // non-destructive cursor-left; deletion is encoded as overwrite+space+reposition
			if t.cursor > 0 {
				t.cursor--
			}
		default:
			if b >= 0x20 { // printable
				if t.cursor >= len(t.buf) {
					t.buf = append(t.buf, b)
				} else {
					t.buf[t.cursor] = b
				}
				t.cursor++
			}
		}
		i++
	}
}

// isCSIFinalByte returns true for the full VT100 CSI final-byte range 0x40–0x7E.
// This includes letters AND ~, ^, _, `, {, |, }, etc.
// Previously only A-Z/a-z were checked, so sequences ending in ~ (e.g. ESC[200~
// bracketed paste, ESC[3~ Delete key) never terminated — the parser consumed all
// following text, which was the root cause of spaces and characters disappearing
// after copy-paste operations.
func isCSIFinalByte(b byte) bool {
	return b >= 0x40 && b <= 0x7e
}

func (t *lineTracker) applyCSI(params string, final byte) {
	switch final {
	case 'K': // erase in line
		switch csiParam(params, 0) {
		case 0: // cursor to end
			if t.cursor < len(t.buf) {
				t.buf = t.buf[:t.cursor]
			}
		case 1: // start to cursor
			for i := 0; i < t.cursor && i < len(t.buf); i++ {
				t.buf[i] = ' '
			}
		case 2: // entire line
			t.buf = t.buf[:0]
			t.cursor = 0
		}
	case 'D': // cursor left n
		n := csiParam(params, 1)
		if t.cursor > n {
			t.cursor -= n
		} else {
			t.cursor = 0
		}
	case 'C': // cursor right n
		n := csiParam(params, 1)
		t.cursor += n
		if t.cursor > len(t.buf) {
			t.cursor = len(t.buf)
		}
	case 'A': // cursor up n lines — readline uses this when a command wraps physical lines
		n := csiParam(params, 1)
		t.cursor -= n * t.termWidth
		if t.cursor < 0 {
			t.cursor = 0
		}
	case 'B': // cursor down n lines
		n := csiParam(params, 1)
		t.cursor += n * t.termWidth
		if t.cursor > len(t.buf) {
			t.cursor = len(t.buf)
		}
	case 'G': // cursor to column n (1-indexed) — readline uses this on wrapped lines
		col := csiParam(params, 1) - 1
		row := t.cursor / t.termWidth
		t.cursor = row*t.termWidth + col
		if t.cursor < 0 {
			t.cursor = 0
		}
		if t.cursor > len(t.buf) {
			t.cursor = len(t.buf)
		}
	}
	// All other sequences (color 'm', clear screen 'J', etc.) are ignored —
	// they don't affect the text content of the current line.
}

// csiParam parses the first numeric parameter from a CSI params string,
// returning def if absent or unparseable.
func csiParam(params string, def int) int {
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

// visibleCommand strips the shell prompt from the buffered line and returns
// the command text the user can currently see.
func (t *lineTracker) visibleCommand() string {
	line := string(t.buf)
	for _, marker := range []string{"$ ", "# ", "% "} {
		if idx := strings.LastIndex(line, marker); idx >= 0 {
			return line[idx+len(marker):]
		}
	}
	return line
}

func (t *lineTracker) reset() {
	t.buf = t.buf[:0]
	t.cursor = 0
}

// addToTypedBuf feeds raw input bytes into the typed-input tracker.
// ESC (0x1b) and TAB (0x09) set dirty=true so Enter falls back to the output
// tracker (history/completion may have rewritten the line).
// Backspace removes the last char; printable ASCII is appended.
func addToTypedBuf(data []byte, buf *[]byte, dirty *bool) {
	if *dirty {
		return
	}
	if len(data) > 0 && data[0] == 0x1b {
		*dirty = true
		return
	}
	for _, b := range data {
		switch {
		case b == 0x1b || b == 0x09:
			*dirty = true
			return
		case b == '\b' || b == 0x7f:
			if len(*buf) > 0 {
				*buf = (*buf)[:len(*buf)-1]
			}
		case b >= 0x20:
			*buf = append(*buf, b)
		}
	}
}

// joinPastedCommand converts a multi-line pasted bash command into a single
// executable line suitable for sending to the relay.
//
// Algorithm (line-by-line to handle trailing spaces before \):
//  1. Normalise \r\n and bare \r to \n.
//  2. Split on \n.  For each line, trim trailing spaces/tabs, then if the line
//     ends with \ it is a bash continuation — strip the \ and merge with the
//     next line.  This handles "cmd \ \n" (space before \) as well as "cmd \\n".
//  3. Join surviving parts with a single space.
//  4. Trim leading/trailing whitespace.
func joinPastedCommand(buf []byte) string {
	s := string(buf)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	var parts []string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			// bash line continuation — strip the trailing \ and merge
			parts = append(parts, trimmed[:len(trimmed)-1])
		} else if len(parts) > 0 || line != "" {
			parts = append(parts, line)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// --- session start ----------------------------------------------------------

type relaySessionResp struct {
	SessionID string `json:"session_id"`
	WSURL     string `json:"ws_url"`
}

func startRelaySession(ctx context.Context, relayURL, token, instanceID string) (*relaySessionResp, error) {
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
	var result relaySessionResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("session/start: parse response: %w", err)
	}
	if result.SessionID == "" || result.WSURL == "" {
		return nil, fmt.Errorf("session/start: missing session_id or ws_url in response")
	}
	return &result, nil
}

// --- main entry point -------------------------------------------------------

func runRelayShell(cfg *config.Config, token, instanceID string) error {
	if cfg.RelayURL == "" {
		return fmt.Errorf("relay_url not set in ~/.bbctl/config.yaml")
	}

	fmt.Fprintf(os.Stderr, "Starting relay session for %s...\n", instanceID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	session, err := startRelaySession(ctx, cfg.RelayURL, token, instanceID)
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
		return fmt.Errorf("--relay requires an interactive terminal")
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
	// Enable bracketed paste mode: the local terminal wraps pastes in
	// ESC[200~...ESC[201~, so embedded \r chars inside pasted text are
	// never mistaken for the user pressing Enter.
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
			encodeRelayFrame(msgType, seqNo, session.SessionID, payload))
	}

	// Send initial RESIZE so the agent PTY has the right dimensions.
	resizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint16(resizeBuf[0:2], rows)
	binary.BigEndian.PutUint16(resizeBuf[2:4], cols)
	if err := sendFrame(relayMsgResize, resizeBuf); err != nil {
		return fmt.Errorf("send initial resize: %w", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	errCh := make(chan error, 3)
	tracker := newLineTracker(int(cols))
	var trackerMu sync.Mutex

	// stdin → relay INPUT_DATA
	go func() {
		// 4 KB handles an entire bracketed paste in one read for most commands.
		buf := make([]byte, 4096)
		// typedBuf tracks what the user typed directly (avoids echo round-trip).
		// typedDirty=true after ESC/TAB — fall back to output tracker on Enter.
		var typedBuf []byte
		typedDirty := false

		// Bracketed-paste collection.  While collectingPaste is true we swallow
		// all input into pasteBuf without forwarding anything to the relay.
		// When ESC[201~ arrives, joinPastedCommand() flattens the multi-line
		// content to a single line and forwards THAT to the relay — no embedded
		// \r ever reaches the PTY until the user deliberately presses Enter.
		var collectingPaste bool
		var pasteBuf []byte
		pasteStartMarker := []byte("\x1b[200~")
		pasteEndMarker := []byte("\x1b[201~")

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

			// ── Bracketed-paste collection ────────────────────────────────────
			if collectingPaste {
				if endIdx := bytes.Index(data, pasteEndMarker); endIdx >= 0 {
					pasteBuf = append(pasteBuf, data[:endIdx]...)
					collectingPaste = false
					joined := joinPastedCommand(pasteBuf)
					pasteBuf = pasteBuf[:0]
					typedBuf = append(typedBuf, []byte(joined)...)
					typedDirty = false
					// Forward joined single-line command so PTY echoes it to screen.
					if len(joined) > 0 {
						if err := sendFrame(relayMsgInput, []byte(joined)); err != nil {
							errCh <- err
							return
						}
					}
				} else {
					pasteBuf = append(pasteBuf, data...)
				}
				continue
			}

			if startIdx := bytes.Index(data, pasteStartMarker); startIdx >= 0 {
				// Handle any chars typed before the paste marker.
				if startIdx > 0 {
					pre := data[:startIdx]
					addToTypedBuf(pre, &typedBuf, &typedDirty)
					if err := sendFrame(relayMsgInput, pre); err != nil {
						errCh <- err
						return
					}
				}
				collectingPaste = true
				typedDirty = false
				pasteBuf = pasteBuf[:0]
				after := data[startIdx+len(pasteStartMarker):]
				// Both markers may arrive in the same read (small paste).
				if endIdx := bytes.Index(after, pasteEndMarker); endIdx >= 0 {
					pasteBuf = append(pasteBuf, after[:endIdx]...)
					collectingPaste = false
					joined := joinPastedCommand(pasteBuf)
					pasteBuf = pasteBuf[:0]
					typedBuf = append(typedBuf, []byte(joined)...)
					typedDirty = false
					if len(joined) > 0 {
						if err := sendFrame(relayMsgInput, []byte(joined)); err != nil {
							errCh <- err
							return
						}
					}
				} else {
					pasteBuf = append(pasteBuf, after...)
				}
				continue
			}
			// ── End bracketed-paste collection ───────────────────────────────

			// ── Non-bracketed paste detection ────────────────────────────────
			// When the inner shell (e.g. "kubectl exec -it pod bash") disables
			// bracketed-paste mode via ESC[?2004l, macOS Terminal sends paste
			// bytes raw without ESC[200~/ESC[201~ wrappers.  The tell-tale sign
			// is \r appearing BEFORE the last byte: regular typing either has no
			// \r (mid-word) or \r only as the final byte (Enter).
			// We handle this the same way as bracketed paste: join lines then
			// forward the single-line command.  We also immediately re-enable
			// bracketed paste on our local terminal (see output handler below).
			if n > 1 {
				if hasEmbeddedCR := bytes.IndexByte(data[:n-1], 0x0d) >= 0; hasEmbeddedCR {
					endsWithCR := data[n-1] == 0x0d
					content := data
					if endsWithCR {
						content = data[:n-1]
					}
					joined := joinPastedCommand(content)
					if joined != "" {
						typedBuf = append(typedBuf, []byte(joined)...)
						typedDirty = false
						if err := sendFrame(relayMsgInput, []byte(joined)); err != nil {
							errCh <- err
							return
						}
					}
					// Re-enable bracketed paste on our terminal so the next paste
					// is handled cleanly (inner bash may have turned it off).
					os.Stdout.Write([]byte("\x1b[?2004h")) //nolint:errcheck
					if endsWithCR {
						// Paste ended with a newline — treat as implicit Enter.
						var cmd string
						trackerMu.Lock()
						if typedDirty {
							time.Sleep(50 * time.Millisecond)
							cmd = tracker.visibleCommand()
						} else {
							cmd = string(typedBuf)
						}
						tracker.reset()
						trackerMu.Unlock()
						typedBuf = typedBuf[:0]
						typedDirty = false
						if err := sendFrame(relayMsgInput, append([]byte(cmd), 0x0d)); err != nil {
							errCh <- err
						}
					}
					continue
				}
			}
			// ── End non-bracketed paste detection ────────────────────────────

			// Enter detection: \r must be the LAST byte of the read.
			// • Enter alone:            n=1, data=[0x0d]           → Enter ✓
			// • Fast typing "ppi\r":    last byte is 0x0d          → Enter ✓
			// • Paste \r:               caught by embedded-CR check above ✓
			if data[n-1] == 0x0d {
				if n > 1 {
					pre := data[:n-1]
					addToTypedBuf(pre, &typedBuf, &typedDirty)
					if err := sendFrame(relayMsgInput, pre); err != nil {
						errCh <- err
						return
					}
				}
				if typedDirty {
					time.Sleep(50 * time.Millisecond)
				}
				var cmd string
				trackerMu.Lock()
				if typedDirty {
					cmd = tracker.visibleCommand()
				} else {
					cmd = string(typedBuf)
				}
				tracker.reset()
				trackerMu.Unlock()
				typedBuf = typedBuf[:0]
				typedDirty = false
				if err := sendFrame(relayMsgInput, append([]byte(cmd), 0x0d)); err != nil {
					errCh <- err
				}
				continue
			}

			// Regular keystrokes — track and forward.
			addToTypedBuf(data, &typedBuf, &typedDirty)
			if err := sendFrame(relayMsgInput, append([]byte(nil), data...)); err != nil {
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
			f, err := decodeRelayFrame(data)
			if err != nil {
				continue
			}
			switch f.MsgType {
			case relayMsgOutput:
				trackerMu.Lock()
				tracker.feed(f.Payload)
				trackerMu.Unlock()
				os.Stdout.Write(f.Payload) //nolint:errcheck
				// If the inner shell (e.g. kubectl exec bash) disables bracketed
				// paste mode by writing ESC[?2004l, immediately re-enable it so
				// our paste-join logic continues to work.
				if bytes.Contains(f.Payload, []byte("\x1b[?2004l")) {
					os.Stdout.Write([]byte("\x1b[?2004h")) //nolint:errcheck
				}
			case relayMsgClose:
				reason := strings.TrimSpace(string(f.Payload))
				if reason != "" {
					fmt.Fprintf(os.Stdout, "\r\n[session closed: %s]\r\n", reason)
				}
				errCh <- nil
				return
			case relayMsgKeepalive:
				// intentionally ignored
			}
		}
	}()

	// SIGWINCH → RESIZE
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
				sendFrame(relayMsgResize, p) //nolint:errcheck
			}
		}
	}()

	<-errCh
	fmt.Fprintf(os.Stdout, "\r\n")
	return nil
}
