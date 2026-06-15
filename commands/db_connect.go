package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/chzyer/readline"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// ─── flags ────────────────────────────────────────────────────────────────────

var dbAccount string
var dbHost    string
var dbUser    string
var dbPort    int

// ─── cobra wiring ─────────────────────────────────────────────────────────────

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Database access commands",
}

func init() {
	rootCmd.AddCommand(dbCmd)
}

var dbConnectCmd = &cobra.Command{
	Use:   "connect [alias]",
	Short: "Open a governed MySQL REPL",
	Long: `Connect to a governed RDS instance via the bbctl backend.
All queries are classified and audited. Restricted queries (DELETE, DROP, etc.)
require Jira ticket approval before execution.

Examples:
  bbctl db connect prod-karma-mysql -a zinka
  bbctl db connect -a zinka --host rds.internal --user admin`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDBConnect,
}

func init() {
	dbCmd.AddCommand(dbConnectCmd)
	dbConnectCmd.Flags().StringVarP(&dbAccount, "account", "a", "", "AWS account name")
	dbConnectCmd.Flags().StringVar(&dbHost, "host", "", "RDS host (overrides alias)")
	dbConnectCmd.Flags().StringVar(&dbUser, "user", "", "Database username (skip prompt)")
	dbConnectCmd.Flags().IntVar(&dbPort, "port", 3306, "RDS port")
}

// ─── protocol types (mirror backend JSON) ────────────────────────────────────

type dbConnectMsg struct {
	Type     string `json:"type"`
	Alias    string `json:"alias,omitempty"`
	Account  string `json:"account,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type dbQueryMsg struct {
	Type string `json:"type"`
	SQL  string `json:"sql"`
}

type dbTicketMsg struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

type dbBaseMsg struct {
	Type string `json:"type"`
}

type dbConnectedMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Host      string `json:"host"`
	Version   string `json:"version"`
}

type dbResultSet struct {
	Type       string     `json:"type"`
	Columns    []string   `json:"columns"`
	Rows       [][]*string `json:"rows"`
	DurationMs int64      `json:"duration_ms"`
}

type dbOKMsg struct {
	Type         string `json:"type"`
	RowsAffected int64  `json:"rows_affected"`
	LastInsertID int64  `json:"last_insert_id"`
	DurationMs   int64  `json:"duration_ms"`
}

type dbRestrictedMsg struct {
	Type      string `json:"type"`
	TicketKey string `json:"ticket_key"`
	TicketURL string `json:"ticket_url"`
	Message   string `json:"message"`
	SQL       string `json:"sql"`
}

type dbErrorMsg struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ─── runDBConnect ─────────────────────────────────────────────────────────────

func runDBConnect(cmd *cobra.Command, args []string) error {
	var alias string
	if len(args) == 1 {
		alias = args[0]
	}
	if alias == "" && dbHost == "" {
		return fmt.Errorf("provide an alias argument or --host")
	}

	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return fmt.Errorf("config dir: %w", err)
	}
	token, err := config.LoadToken(configDir)
	if err != nil {
		return fmt.Errorf("not authenticated. Run: bbctl login")
	}
	cfg, err := config.LoadOrDefault(configDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.BackendURL == "" {
		return fmt.Errorf("backend_url not set in ~/.bbctl/config.yaml")
	}

	// Credentials.
	username := dbUser
	if username == "" {
		fmt.Print("Database username: ")
		reader := bufio.NewReader(os.Stdin)
		username, _ = reader.ReadString('\n')
		username = strings.TrimSpace(username)
	}
	fmt.Print("Database password: ")
	passBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	password := string(passBytes)

	// Dial WebSocket.
	wsURL := strings.Replace(cfg.BackendURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	u, err := url.Parse(wsURL + "/v1/db/query")
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	wsConn, _, err := dialer.Dial(u.String(), http.Header{
		"Authorization": {"Bearer " + token},
	})
	if err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	defer wsConn.Close() //nolint:errcheck

	// Send connect message.
	connectMsg := dbConnectMsg{
		Type:     "connect",
		Alias:    alias,
		Account:  dbAccount,
		Host:     dbHost,
		Port:     dbPort,
		Username: username,
		Password: password,
	}
	raw, _ := json.Marshal(connectMsg)
	if err := wsConn.WriteMessage(websocket.TextMessage, raw); err != nil {
		return fmt.Errorf("send connect: %w", err)
	}

	// Wait for "connected" or "error".
	wsConn.SetReadDeadline(time.Now().Add(15 * time.Second)) //nolint:errcheck
	_, msg, err := wsConn.ReadMessage()
	wsConn.SetReadDeadline(time.Time{}) //nolint:errcheck
	if err != nil {
		return fmt.Errorf("waiting for connection: %w", err)
	}
	var base dbBaseMsg
	json.Unmarshal(msg, &base) //nolint:errcheck
	if base.Type == "error" {
		var e dbErrorMsg
		json.Unmarshal(msg, &e) //nolint:errcheck
		return fmt.Errorf("backend: %s", e.Message)
	}
	if base.Type != "connected" {
		return fmt.Errorf("unexpected response type: %s", base.Type)
	}
	var connected dbConnectedMsg
	json.Unmarshal(msg, &connected) //nolint:errcheck

	fmt.Printf("\nConnected to %s  (%s)\n", connected.Host, connected.Version)
	fmt.Printf("Session: %s\n", connected.SessionID)
	fmt.Printf("Type SQL terminated with ; — Ctrl+C or Ctrl+D to exit.\n\n")

	// SIGTERM: clean exit (readline handles SIGINT/Ctrl+C).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nDisconnecting...")
		wsConn.Close() //nolint:errcheck
		os.Exit(0)
	}()

	return runREPL(wsConn)
}

// ─── REPL ─────────────────────────────────────────────────────────────────────

func runREPL(wsConn *websocket.Conn) error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "mysql> ",
		HistoryFile:     os.ExpandEnv("$HOME/.bbctl_db_history"),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return fmt.Errorf("readline init: %w", err)
	}
	defer rl.Close()

	var scanner scanState
	var pendingTicket string

	for {
		if scanner.active() {
			rl.SetPrompt("    -> ")
		} else {
			rl.SetPrompt("mysql> ")
		}

		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			if line == "" {
				fmt.Println("\nBye")
				return nil
			}
			// Reset mid-statement input on Ctrl+C.
			scanner = scanState{}
			pendingTicket = ""
			fmt.Println()
			continue
		}
		if err != nil { // io.EOF = Ctrl+D
			fmt.Println("Bye")
			return nil
		}

		// Ticket-wait mode: next non-empty line is the ticket key.
		if pendingTicket != "" {
			key := strings.TrimSpace(line)
			if key == "" {
				continue
			}
			if strings.EqualFold(key, "cancel") {
				pendingTicket = ""
				fmt.Println("Query cancelled.")
				continue
			}
			if err := sendDBTicket(wsConn, key); err != nil {
				return err
			}
			pendingTicket = ""
			if _, err := receiveDBResponse(wsConn); err != nil {
				return err
			}
			continue
		}

		if scanner.feed(line) < 0 {
			continue
		}

		sql := scanner.flush()
		if sql == "" {
			continue
		}

		if err := sendDBQuery(wsConn, sql); err != nil {
			return err
		}
		ticketKey, err := receiveDBResponse(wsConn)
		if err != nil {
			return err
		}
		if ticketKey != "" {
			pendingTicket = ticketKey
		}
	}
}

// ─── send / receive helpers ───────────────────────────────────────────────────

func sendDBQuery(wsConn *websocket.Conn, sql string) error {
	raw, _ := json.Marshal(dbQueryMsg{Type: "query", SQL: sql})
	return wsConn.WriteMessage(websocket.TextMessage, raw)
}

func sendDBTicket(wsConn *websocket.Conn, key string) error {
	raw, _ := json.Marshal(dbTicketMsg{Type: "ticket", Key: key})
	return wsConn.WriteMessage(websocket.TextMessage, raw)
}

// receiveDBResponse reads one server message, prints it, and returns the
// ticket key if the response was "restricted" (empty string otherwise).
func receiveDBResponse(wsConn *websocket.Conn) (string, error) {
	_, msg, err := wsConn.ReadMessage()
	if err != nil {
		return "", err
	}
	var base dbBaseMsg
	json.Unmarshal(msg, &base) //nolint:errcheck

	switch base.Type {
	case "resultset":
		var rs dbResultSet
		json.Unmarshal(msg, &rs) //nolint:errcheck
		fmt.Print(renderTable(rs.Columns, rs.Rows, rs.DurationMs))
	case "ok":
		var ok dbOKMsg
		json.Unmarshal(msg, &ok) //nolint:errcheck
		fmt.Print(renderOK(ok.RowsAffected, ok.LastInsertID, ok.DurationMs))
	case "restricted":
		var r dbRestrictedMsg
		json.Unmarshal(msg, &r) //nolint:errcheck
		fmt.Print(renderRestricted(r.TicketKey, r.TicketURL, r.SQL))
		fmt.Print("Enter ticket key (or 'cancel'): ")
		return r.TicketKey, nil
	case "error":
		var e dbErrorMsg
		json.Unmarshal(msg, &e) //nolint:errcheck
		fmt.Print(renderError(e.Code, e.Message))
	default:
		fmt.Printf("[unknown response type: %s]\n", base.Type)
	}
	return "", nil
}

// ─── multi-line SQL scanner ───────────────────────────────────────────────────

type scanState struct {
	buf      strings.Builder
	inSingle bool
	inDouble bool
	inBack   bool
	inBlock  bool
}

// active returns true when a partial statement is buffered.
func (s *scanState) active() bool {
	return s.buf.Len() > 0
}

// feed appends line to the buffer. Returns the position of the first
// unquoted semicolon (≥0), or -1 if the statement is not yet complete.
func (s *scanState) feed(line string) int {
	if s.buf.Len() > 0 {
		s.buf.WriteByte('\n')
	}
	s.buf.WriteString(line)
	return s.scan(line)
}

// flush returns the accumulated SQL and resets all state.
func (s *scanState) flush() string {
	sql := strings.TrimSpace(s.buf.String())
	sql = strings.TrimRight(sql, ";")
	sql = strings.TrimSpace(sql)
	s.buf.Reset()
	s.inSingle = false
	s.inDouble = false
	s.inBack = false
	s.inBlock = false
	return sql
}

// scan returns the index of the first unquoted semicolon in line, or -1.
// State (inSingle, inDouble, etc.) carries across calls for multi-line SQL.
func (s *scanState) scan(line string) int {
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		ch := runes[i]

		if s.inBlock {
			if ch == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				s.inBlock = false
				i += 2
			} else {
				i++
			}
			continue
		}

		if s.inSingle {
			if ch == '\\' {
				i += 2
			} else if ch == '\'' && i+1 < len(runes) && runes[i+1] == '\'' {
				i += 2
			} else if ch == '\'' {
				s.inSingle = false
				i++
			} else {
				i++
			}
			continue
		}

		if s.inDouble {
			if ch == '\\' {
				i += 2
			} else if ch == '"' && i+1 < len(runes) && runes[i+1] == '"' {
				i += 2
			} else if ch == '"' {
				s.inDouble = false
				i++
			} else {
				i++
			}
			continue
		}

		if s.inBack {
			if ch == '`' {
				s.inBack = false
			}
			i++
			continue
		}

		switch ch {
		case '\'':
			s.inSingle = true
			i++
		case '"':
			s.inDouble = true
			i++
		case '`':
			s.inBack = true
			i++
		case '/':
			if i+1 < len(runes) && runes[i+1] == '*' {
				s.inBlock = true
				i += 2
			} else {
				i++
			}
		case '-':
			if i+1 < len(runes) && runes[i+1] == '-' {
				return -1
			}
			i++
		case '#':
			return -1
		case ';':
			return i
		default:
			i++
		}
	}
	return -1
}
