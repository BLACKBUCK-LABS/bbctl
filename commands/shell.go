package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	ec2picker "github.com/blackbuck/bbctl/internal/ec2"
	"github.com/blackbuck/bbctl/internal/shell"
	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
)

var shellAccount string

var shellCmd = &cobra.Command{
	Use:   "shell [instance-id]",
	Short: "Open an interactive shell session against an EC2 instance",
	Args:  cobra.ArbitraryArgs,
	RunE:  runShell,
}

func init() {
	shellCmd.Flags().StringVarP(&shellAccount, "account", "a", "", "AWS account name or ID (overrides default_account_id in config)")
	rootCmd.AddCommand(shellCmd)
}

func runShell(cmd *cobra.Command, args []string) error {
	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	token, err := config.LoadToken(configDir)
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(configDir)
	if err != nil {
		return err
	}
	if cfg.BackendURL == "" {
		return fmt.Errorf("backend_url not set in ~/.bbctl/config.yaml")
	}

	var instanceID string
	if len(args) > 0 && strings.HasPrefix(args[0], "i-") {
		instanceID = args[0]
	}

	accountID := shellAccount
	if accountID == "" {
		accountID = cfg.DefaultAccountID
	}
	accountID = cfg.ResolveAccount(accountID)

	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)

	if instanceID == "" {
		instances, err := ec2picker.LoadAll(cmd.Context(), c, cfg, configDir, false)
		if err != nil {
			return fmt.Errorf("load instances: %w", err)
		}
		selected, err := ec2picker.Pick(instances)
		if err != nil {
			return err
		}
		if selected == nil {
			fmt.Println("Cancelled.")
			return nil
		}
		instanceID = selected.InstanceID
		if shellAccount == "" {
			accountID = selected.AccountID
		}
		fmt.Printf("→ %s (%s)\n", selected.Name, selected.InstanceID)
	}

	if accountID == "" {
		return fmt.Errorf("AWS account ID is required: pass --account 123456789012 or set default_account_id in ~/.bbctl/config.yaml")
	}

	return runShellDirect(instanceID, accountID, cfg, token)
}

func runShellDirect(instanceID, accountID string, cfg *config.Config, token string) error {
	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	email := extractEmailFromJWT(token)

	var activeTicket string
	var history []string
	var currentDir string // "" = home (~)
	var prevDir string

	rl, err := readline.New(shell.FormatPrompt(email, instanceID, activeTicket != "", currentDir))
	if err != nil {
		return err
	}
	defer rl.Close()

	fmt.Printf("Connected to %s.\nSafe mode. Type /help for commands, /exit to quit.\n\n", instanceID)

	// Ctrl-C handling: cancel in-flight request but keep shell open.
	type cancelState struct {
		cancel    context.CancelFunc
		requestID string
	}
	var inFlight *cancelState

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGTERM {
				rl.Close()
				os.Exit(0)
			}
			// SIGINT / Ctrl-C
			if inFlight != nil {
				inFlight.cancel()
				if inFlight.requestID != "" {
					_ = c.CancelCommand(context.Background(), inFlight.requestID)
				}
				inFlight = nil
				fmt.Fprintln(os.Stderr, "^C [cancelled]")
			}
			// If nothing in flight, readline will handle the ^C display itself.
		}
	}()

	for {
		rl.SetPrompt(shell.FormatPrompt(email, instanceID, activeTicket != "", currentDir))
		line, err := rl.Readline()
		if err != nil {
			if err == io.EOF {
				fmt.Println("\nBye.")
				return nil
			}
			// readline.ErrInterrupt is Ctrl-C with no in-flight command.
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Accumulate backslash-continued lines into one command.
		for strings.HasSuffix(strings.TrimRight(line, " "), "\\") {
			line = strings.TrimRight(line, " ")
			line = line[:len(line)-1] // strip trailing backslash
			rl.SetPrompt("> ")
			nextLine, contErr := rl.Readline()
			rl.SetPrompt(shell.FormatPrompt(email, instanceID, activeTicket != "", currentDir))
			if contErr != nil {
				line = ""
				break
			}
			line = line + " " + strings.TrimSpace(nextLine)
		}
		if line == "" {
			continue
		}

		// "clear" always handles locally — clears ticket if active, else clears screen.
		if strings.Fields(line)[0] == "clear" {
			if activeTicket != "" {
				activeTicket = ""
				fmt.Println("Ticket cleared. Back to safe mode.")
				rl.SetPrompt(shell.FormatPrompt(email, instanceID, false, currentDir))
			} else {
				fmt.Print("\033[2J\033[H")
			}
			continue
		}

		// "exit"/"quit" — clear ticket if active, else exit shell.
		if strings.Fields(line)[0] == "exit" || strings.Fields(line)[0] == "quit" {
			if activeTicket != "" {
				activeTicket = ""
				fmt.Println("Ticket cleared. Back to safe mode.")
				rl.SetPrompt(shell.FormatPrompt(email, instanceID, false, currentDir))
			} else {
				fmt.Println("Bye.")
				return nil
			}
			continue
		}

		// Local pwd — print tracked dir without a backend round-trip.
		if strings.Fields(line)[0] == "pwd" {
			if currentDir != "" {
				fmt.Println(currentDir)
			} else {
				fmt.Println("~")
			}
			continue
		}

		// Local cd — never sent to backend, never audited.
		if isCd(line) {
			fields := strings.Fields(line)
			arg := ""
			if len(fields) >= 2 {
				arg = fields[1]
			}
			if arg == "-" {
				currentDir, prevDir = prevDir, currentDir
			} else {
				prevDir = currentDir
				currentDir = resolveCd(currentDir, line)
			}
			continue
		}

		// Slash commands — handle locally.
		if shell.IsSlashCommand(line) {
			name, arg := shell.ParseSlashCommand(line)
			switch name {
			case "exit", "quit":
				fmt.Println("Bye.")
				return nil
			case "help":
				fmt.Print(shell.HelpText)
			case "ticket":
				if arg == "clear" || arg == "none" {
					activeTicket = ""
					fmt.Println("Ticket cleared. Back to safe mode.")
					rl.SetPrompt(shell.FormatPrompt(email, instanceID, false, currentDir))
				} else if arg == "" {
					if activeTicket != "" {
						fmt.Printf("Active ticket: %s\n", activeTicket)
					} else {
						fmt.Println("No active ticket set.")
					}
				} else {
					activeTicket = arg
					fmt.Printf("Ticket %s set.\n", activeTicket)
					rl.SetPrompt(shell.FormatPrompt(email, instanceID, true, currentDir))
				}
			case "classify":
				if arg == "" {
					fmt.Fprintln(os.Stderr, "Usage: /classify <command>")
					continue
				}
				cr, err := c.Classify(context.Background(), arg, instanceID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "classify error: %v\n", err)
				} else {
					fmt.Printf("Tier: %s", cr.Tier)
					if cr.Reason != "" {
						fmt.Printf(" (%s)", cr.Reason)
					}
					if len(cr.RewrittenTo) > 0 {
						fmt.Printf(" -> %s", strings.Join(cr.RewrittenTo, " "))
					}
					fmt.Println()
				}
			case "history":
				for i, h := range history {
					fmt.Printf("%3d  %s\n", i+1, h)
				}
			case "whoami":
				fmt.Printf("Email:  %s\n", email)
				if activeTicket != "" {
					fmt.Printf("Ticket: %s\n", activeTicket)
				}
			case "account":
				if accountID != "" {
					label := accountID
					for alias, id := range cfg.AccountAliases {
						if id == accountID {
							label = fmt.Sprintf("%s%s (%s)", strings.ToUpper(alias[:1]), alias[1:], accountID)
							break
						}
					}
					fmt.Fprintf(os.Stdout, "Account: %s\n", label)
				} else {
					fmt.Fprintln(os.Stdout, "No account set")
				}
			default:
				fmt.Fprintf(os.Stderr, "Unknown command: /%s — type /help\n", name)
			}
			continue
		}

		// Regular command: preview classification first for better UX.
		cr, classifyErr := c.Classify(context.Background(), line, instanceID)
		if classifyErr == nil {
			switch cr.Tier {
			case "denied":
				fmt.Fprintf(os.Stderr, "Denied: %s\n", cr.Reason)
				continue
			case "restricted":
				// No early bail — let RunCommand proceed so the backend can auto-create a ticket.
			}
		}
		// If classify fails (network error), proceed — backend will re-enforce.

		// Execute.
		ctx, cancelFn := context.WithCancel(context.Background())
		state := &cancelState{cancel: cancelFn}
		inFlight = state

		resp, err := c.RunCommand(ctx, client.CommandRequest{
			InstanceID:   instanceID,
			AccountID:    accountID,
			Command:      resolvePaths(line, currentDir),
			JiraTicketID: activeTicket,
		})
		cancelFn()
		inFlight = nil

		history = append(history, line)

		if err != nil {
			var apiErr *client.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.HTTPStatus {
				case 402:
					cmdName := strings.Fields(line)[0]
					fmt.Fprintf(os.Stderr,
						"'%s' requires approval — ticket auto-creation failed.\n"+
							"Please create a Jira ticket manually, then set it with:\n"+
							"  /ticket <TICKET-ID>\n", cmdName)
				case 401:
					fmt.Fprintln(os.Stderr, "Session expired — run: bbctl login")
				default:
					fmt.Fprintf(os.Stderr, "error: %s\n", apiErr.Error())
				}
			} else if errors.Is(err, context.Canceled) {
				// Already printed "^C [cancelled]" from signal handler.
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			continue
		}

		state.requestID = resp.RequestID

		if resp.TicketKey != "" {
			fmt.Fprintf(os.Stdout, "\n✅ Jira ticket created: %s\n", resp.TicketKey)
			fmt.Fprintf(os.Stdout, "   %s\n\n", resp.TicketURL)
			fmt.Fprintln(os.Stdout, "⏳ Waiting for manager approval.")
			fmt.Fprintln(os.Stdout, "   Once approved, run these in order:")
			fmt.Fprintf(os.Stdout, "     1. /ticket %s\n", resp.TicketKey)
			fmt.Fprintf(os.Stdout, "     2. %s\n\n", line)
			continue
		}

		if resp.Status == "success" && resp.TicketKey == "" && activeTicket != "" {
			fmt.Fprintf(os.Stdout, "\nTicket %s marked as Access Granted — cleared.\n", activeTicket)
			activeTicket = ""
			rl.SetPrompt(shell.FormatPrompt(email, instanceID, false, currentDir))
		}

		if resp.Stdout != "" {
			fmt.Print(resp.Stdout)
			if !strings.HasSuffix(resp.Stdout, "\n") {
				fmt.Println()
			}
		}
		if resp.Stderr != "" {
			fmt.Fprint(os.Stderr, resp.Stderr)
			if !strings.HasSuffix(resp.Stderr, "\n") {
				fmt.Fprintln(os.Stderr)
			}
		}
		if resp.Truncated {
			fmt.Fprintln(os.Stderr, "[output truncated — use tail/head to narrow]")
		}
	}
}

func isCd(line string) bool {
	fields := strings.Fields(line)
	return len(fields) >= 1 && fields[0] == "cd"
}

func resolveCd(currentDir, line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "" // cd with no args → home
	}
	arg := fields[1]
	switch arg {
	case "~":
		return ""
	case "..":
		if currentDir == "" || currentDir == "/" {
			return ""
		}
		return filepath.Dir(currentDir)
	default:
		if filepath.IsAbs(arg) {
			return arg
		}
		if currentDir == "" {
			return "/" + arg
		}
		return filepath.Join(currentDir, arg)
	}
}

// defaultsToCwd is the set of commands that operate on currentDir when
// called with no positional arguments.
var defaultsToCwd = map[string]bool{
	"ls":   true,
	"ll":   true,
	"la":   true,
	"du":   true,
	"find": true,
}

// resolvePaths rewrites relative arguments in a command to absolute paths
// based on the locally tracked currentDir. Flags and absolute paths are
// passed through unchanged. The command name itself is never modified.
func resolvePaths(line, currentDir string) string {
	if currentDir == "" {
		return line
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return line
	}
	result := make([]string, 0, len(fields)+1)
	result = append(result, fields[0])
	hasPositional := false
	for _, arg := range fields[1:] {
		if !strings.HasPrefix(arg, "-") && !filepath.IsAbs(arg) {
			result = append(result, filepath.Join(currentDir, arg))
			hasPositional = true
		} else {
			result = append(result, arg)
		}
	}
	if !hasPositional && defaultsToCwd[fields[0]] {
		result = append(result, currentDir)
	}
	return strings.Join(result, " ")
}

// extractEmailFromJWT decodes the JWT payload and returns the email claim.
// Falls back to "developer" on any parse error.
func extractEmailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "developer"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "developer"
	}
	var claims struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "developer"
	}
	if claims.Email != "" {
		return claims.Email
	}
	if claims.Sub != "" {
		return claims.Sub
	}
	return "developer"
}
