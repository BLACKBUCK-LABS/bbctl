package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

	return runShellDirect(instanceID, accountID, cfg, configDir, token)
}

func runShellDirect(instanceID, accountID string, cfg *config.Config, cfgDir, token string) error {
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

		// Silently refresh token if expiring soon.
		if config.IsTokenExpired(cfgDir) {
			newToken, err := config.RefreshToken(cfgDir, cfg)
			if err != nil {
				fmt.Fprintln(os.Stdout, "\n⚠️  Session expired. Please run: bbctl login")
				return nil
			}
			token = newToken
			c = client.New(cfg.BackendURL, token, "bbctl/"+Version)
		}

		// Detect curl @file references and handle uploads if needed.
		if strings.Fields(line)[0] == "curl" {
			promptUpdater := func() {
				rl.SetPrompt(shell.FormatPrompt(email, instanceID,
					activeTicket != "", currentDir))
			}
			if handled, err := handleCurlFileRefs(
				&line, instanceID, accountID, &activeTicket,
				cfg, token, c, promptUpdater,
			); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			} else if handled {
				continue
			}
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

func buildCombinedCurl(
	ctx context.Context,
	localRefs []shell.FileRef,
	rewrites map[string]string,
	rewritten string,
	c *client.Client,
) (combined string, ok bool) {
	fmt.Fprintln(os.Stdout, "\n⬆️  Staging files to S3...")
	var segments []string
	for _, ref := range localRefs {
		content, err := os.ReadFile(ref.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", ref.Path, err)
			return "", false
		}
		sum := sha256.Sum256(content)
		stageResp, err := c.StageFile(ctx, client.StageRequest{
			Filename:   filepath.Base(ref.Path),
			ContentB64: base64.StdEncoding.EncodeToString(content),
			SHA256:     hex.EncodeToString(sum[:]),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "stage %s: %v\n", ref.Path, err)
			return "", false
		}
		fmt.Fprintf(os.Stdout, "   ✅ %s staged\n", filepath.Base(ref.Path))
		ec2Path := rewrites[ref.Path]
		if len(segments) == 0 {
			segments = append(segments,
				fmt.Sprintf("curl -sf '%s' -o '%s'", stageResp.PresignedURL, ec2Path))
		} else {
			segments = append(segments,
				fmt.Sprintf("--next -sf '%s' -o '%s'", stageResp.PresignedURL, ec2Path))
		}
	}
	segments = append(segments, "--next "+rewritten)
	return strings.Join(segments, " "), true
}

func handleCurlFileRefs(
	line *string,
	instanceID, accountID string,
	activeTicket *string,
	cfg *config.Config,
	token string,
	c *client.Client,
	promptUpdater func(),
) (handled bool, err error) {
	refs := shell.DetectFileRefs(*line)
	if len(refs) == 0 {
		return false, nil
	}

	for _, ref := range refs {
		if ref.Location == "postman" {
			fmt.Fprintf(os.Stderr,
				"\n⚠️  Postman Cloud paths are not supported.\n"+
					"   Replace %s with an actual file path.\n"+
					"   Example: @\"/path/to/your/file\"\n\n",
				ref.Original)
			return true, nil
		}
	}

	var localRefs []shell.FileRef
	for _, ref := range refs {
		if ref.Location == "local" {
			localRefs = append(localRefs, ref)
		}
	}
	if len(localRefs) == 0 {
		return false, nil
	}

	fmt.Fprintln(os.Stdout, "\n💡 This command references local files:")
	for _, ref := range localRefs {
		fmt.Fprintf(os.Stdout, "   %s\n", ref.Original)
	}
	fmt.Fprintln(os.Stdout, "\nThese files need to be on the EC2 instance first.")

	rewrites := make(map[string]string)
	for _, ref := range localRefs {
		suggested := shell.SuggestEC2Path(ref.Path)
		fmt.Fprintf(os.Stdout,
			"\n  Local:  %s\n  Remote: %s\n  Press Enter to confirm or type new path: ",
			ref.Path, suggested)
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input != "" {
			suggested = input
		}
		rewrites[ref.Path] = suggested
	}

	rewritten := shell.RewriteCommand(*line, rewrites)

	// TICKET ACTIVE: stage files fresh, build combined curl --next, execute directly.
	if *activeTicket != "" {
		combined, ok := buildCombinedCurl(context.Background(), localRefs, rewrites, rewritten, c)
		if !ok {
			return true, nil
		}
		resp, err := c.RunCommand(context.Background(), client.CommandRequest{
			InstanceID:             instanceID,
			AccountID:              accountID,
			Command:                combined,
			JiraTicketID:           *activeTicket,
			EffectiveForValidation: stripQuotes(rewritten),
		})
		if err != nil {
			return true, err
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
		ticketKey := *activeTicket
		*activeTicket = ""
		promptUpdater()
		fmt.Fprintf(os.Stdout, "\n✅ Ticket %s marked as Access Granted — cleared.\n", ticketKey)
		return true, nil
	}

	// NO TICKET: send main rewritten curl for auto-ticket creation (no staging yet).
	resp, err := c.RunCommand(context.Background(), client.CommandRequest{
		InstanceID: instanceID,
		AccountID:  accountID,
		Command:    rewritten,
	})
	if err != nil {
		return true, err
	}
	if resp.TicketKey != "" {
		fmt.Fprintf(os.Stdout, "\n✅ Jira ticket created: %s\n", resp.TicketKey)
		fmt.Fprintf(os.Stdout, "   %s\n\n", resp.TicketURL)

		// Attach files to ticket for manager review
		fmt.Fprintln(os.Stdout, "📎 Attaching files for manager review...")
		for _, ref := range localRefs {
			content, err := os.ReadFile(ref.Path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠️  Could not read %s: %v\n", ref.Path, err)
				continue
			}
			attachErr := c.AttachToTicket(context.Background(), client.AttachRequest{
				TicketKey:  resp.TicketKey,
				Filename:   filepath.Base(ref.Path),
				ContentB64: base64.StdEncoding.EncodeToString(content),
			})
			if attachErr != nil {
				fmt.Fprintf(os.Stderr, "  ⚠️  Could not attach %s: %v\n",
					filepath.Base(ref.Path), attachErr)
			} else {
				fmt.Fprintf(os.Stdout, "   ✅ %s attached\n", filepath.Base(ref.Path))
			}
		}

		fmt.Fprintln(os.Stdout, "\n⏳ Waiting for manager approval.")
		fmt.Fprintln(os.Stdout, "   Once approved, run these in order:")
		fmt.Fprintf(os.Stdout, "     1. /ticket %s\n", resp.TicketKey)
		fmt.Fprintf(os.Stdout, "     2. %s\n\n", *line)
		fmt.Fprintln(os.Stdout, "📎 Re-running with the ticket will stage files and execute automatically.")
	}
	return true, nil
}

func stripQuotes(cmd string) string {
	return strings.ReplaceAll(cmd, "'", "")
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
