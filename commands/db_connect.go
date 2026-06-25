package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/chzyer/readline"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

// ─── flags ────────────────────────────────────────────────────────────────────

var dbAccount string
var dbVPC string

// ─── cobra wiring ─────────────────────────────────────────────────────────────

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Database access commands",
}

func init() {
	rootCmd.AddCommand(dbCmd)
}

var dbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available databases for an account",
	Long: `List all RDS identifiers available for an account.
Identifiers are fetched from AWS Secrets Manager (bbctl/rds/<account>/).

Examples:
  bbctl db list -a zinka`,
	Args: cobra.NoArgs,
	RunE: runDBList,
}

func init() {
	dbCmd.AddCommand(dbListCmd)
	dbListCmd.Flags().StringVarP(&dbAccount, "account", "a", "", "AWS account name (required)")
	dbListCmd.Flags().StringVarP(&dbVPC, "vpc", "v", "", "Filter by VPC name (e.g. vpc-mumbai-dev)")
	dbListCmd.MarkFlagRequired("account") //nolint:errcheck
}

func runDBList(cmd *cobra.Command, args []string) error {
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

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	resp, err := c.ListDatabases(cmd.Context(), dbAccount, dbVPC)
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}

	if len(resp.Databases) == 0 {
		if dbVPC != "" {
			fmt.Printf("No databases found in account %q VPC %q.\n", dbAccount, dbVPC)
		} else {
			fmt.Printf("No databases found for account %q.\n", dbAccount)
		}
		return nil
	}

	if dbVPC != "" {
		fmt.Printf("Databases in %s (%s):\n\n", resp.Account, dbVPC)
	} else {
		fmt.Printf("Databases in %s:\n\n", resp.Account)
	}
	fmt.Printf("  %-40s  %-10s  %-12s\n", "IDENTIFIER", "ENGINE", "STATUS")
	fmt.Printf("  %-40s  %-10s  %-12s\n", "----------", "------", "------")
	for _, db := range resp.Databases {
		fmt.Printf("  %-40s  %-10s  %-12s\n", db.Identifier, db.Engine, db.Status)
	}
	fmt.Printf("\nConnect: bbctl db connect <identifier> -a %s\n", dbAccount)
	return nil
}

var dbConnectCmd = &cobra.Command{
	Use:   "connect <identifier>",
	Short: "Open a governed MySQL REPL",
	Long: `Connect to a governed RDS instance via the bbctl backend.
Credentials are fetched from AWS Secrets Manager — no password prompt.
All queries are classified and audited. Restricted queries (DELETE, DROP, etc.)
require Jira ticket approval before execution.

Examples:
  bbctl db connect preprod-core-mysql -a zinka`,
	Args: cobra.ExactArgs(1),
	RunE: runDBConnect,
}

func init() {
	dbCmd.AddCommand(dbConnectCmd)
	dbConnectCmd.Flags().StringVarP(&dbAccount, "account", "a", "", "AWS account name or ID")
}

// ─── protocol types (mirror backend JSON) ────────────────────────────────────

type dbConnectMsg struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
	Account    string `json:"account"`
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
	identifier := args[0]
	if dbAccount == "" {
		return fmt.Errorf("account required: pass -a <account>")
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

	return startDBConnect(identifier, dbAccount, cfg, token)
}

// startDBConnect dials the backend WebSocket and starts a governed MySQL REPL.
// Called by both the cobra subcommand and the interactive picker.
func startDBConnect(identifier, account string, cfg *config.Config, token string) error {
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

	raw, _ := json.Marshal(dbConnectMsg{Type: "connect", Identifier: identifier, Account: account})
	if err := wsConn.WriteMessage(websocket.TextMessage, raw); err != nil {
		return fmt.Errorf("send connect: %w", err)
	}

	wsConn.SetReadDeadline(time.Now().Add(20 * time.Second)) //nolint:errcheck
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
