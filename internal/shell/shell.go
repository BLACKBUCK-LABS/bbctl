package shell

import (
	"fmt"
	"path/filepath"
	"strings"
)

// IsSlashCommand returns true if input starts with "/".
func IsSlashCommand(input string) bool {
	return strings.HasPrefix(input, "/")
}

// ParseSlashCommand splits "/ticket PRODACCESS-1234" into ("ticket", "PRODACCESS-1234").
func ParseSlashCommand(input string) (name, arg string) {
	input = strings.TrimPrefix(input, "/")
	parts := strings.SplitN(input, " ", 2)
	name = strings.ToLower(parts[0])
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return
}

// FormatPrompt builds the shell prompt string.
// currentDir is the locally tracked directory ("" means home/~).
func FormatPrompt(email, instanceID string, hasTicket bool, currentDir string) string {
	user := email
	if idx := strings.Index(email, "@"); idx >= 0 {
		user = email[:idx]
	}
	mode := ""
	if hasTicket {
		mode = " [approved]"
	}
	dir := "~"
	if currentDir != "" {
		dir = currentDir
	}
	return fmt.Sprintf("%s@%s:%s%s$ ", user, instanceID, dir, mode)
}

// CleanPath normalises a path for use in FormatPrompt (exported for tests).
func CleanPath(p string) string {
	return filepath.Clean(p)
}

// SafeCommandsTable is the formatted safe-commands reference, shared between
// /help output and the root command Long description.
const SafeCommandsTable = `SAFE COMMANDS (run without approval):
┌─────────────────┬──────────────────────────────────────────────┐
│    Command      │                Description                   │
├─────────────────┼──────────────────────────────────────────────┤
│ ls              │ List directory contents                      │
│ cat             │ Print file contents                          │
│ tail            │ Show last N lines of a file                  │
│ head            │ Show first N lines of a file                 │
│ grep            │ Search file contents                         │
│ less            │ Page through file contents                   │
│ top / htop      │ CPU and memory usage (read-only snapshot)    │
│ ps              │ Show running processes                       │
│ df              │ Show disk space usage                        │
│ du              │ Show directory sizes                         │
│ free            │ Show memory usage                            │
│ netstat         │ Show network connections                     │
│ ss              │ Show socket statistics                       │
│ pwd             │ Show current directory                       │
│ whoami          │ Show current user                            │
│ date            │ Show current date and time                   │
│ uptime          │ Show system uptime and load                  │
│ hostname        │ Show instance hostname                       │
│ wc              │ Count lines, words, bytes in a file          │
│ find            │ Find files by name, type, size               │
│ echo            │ Print text to terminal                       │
│ pgrep           │ Find processes by name                       │
│ pstree          │ Show process tree                            │
│ lscpu           │ Show CPU information                         │
│ lsmem           │ Show memory information                      │
│ lshw            │ Show hardware information                    │
│ uname           │ Show kernel and system info                  │
│ arch            │ Show system architecture                     │
│ nproc           │ Show number of CPU cores                     │
│ id              │ Show user and group IDs                      │
│ groups          │ Show group memberships                       │
│ last            │ Show last login history                      │
│ lastlog         │ Show last login per user                     │
│ w               │ Show who is logged in and what they're doing │
│ who             │ Show logged-in users                         │
│ finger          │ Show user information                        │
│ lsblk           │ Show block devices (disks, partitions)       │
└─────────────────┴──────────────────────────────────────────────┘`

// HelpText is the output of the /help slash command.
const HelpText = `
Available slash commands:
  /help                  — show this help
  /exit, /quit           — exit the shell
  /ticket <jira-id>      — set active Jira ticket for restricted commands
  /classify <command>    — preview command classification without executing
  /history               — show readline history
  /whoami                — show your identity

Tips:
  - Safe commands run immediately.
  - Restricted commands (curl, jmap, etc.) require a Jira ticket: /ticket REQ-XXXX
  - Denied commands are never executed.
  - Ctrl-C cancels an in-flight command (shell stays open).
  - Ctrl-D exits.

` + SafeCommandsTable + "\n"
