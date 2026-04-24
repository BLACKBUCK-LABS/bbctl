package shell_test

import (
	"testing"

	"github.com/blackbuck/bbctl/internal/shell"
	"github.com/stretchr/testify/assert"
)

func TestIsSlashCommand(t *testing.T) {
	assert.True(t, shell.IsSlashCommand("/help"))
	assert.True(t, shell.IsSlashCommand("/ticket PRODACCESS-1234"))
	assert.False(t, shell.IsSlashCommand("ls -la"))
	assert.False(t, shell.IsSlashCommand(""))
}

func TestParseSlashCommand(t *testing.T) {
	name, arg := shell.ParseSlashCommand("/ticket PRODACCESS-1234")
	assert.Equal(t, "ticket", name)
	assert.Equal(t, "PRODACCESS-1234", arg)

	name, arg = shell.ParseSlashCommand("/help")
	assert.Equal(t, "help", name)
	assert.Equal(t, "", arg)

	name, arg = shell.ParseSlashCommand("/TICKET ABC-123")
	assert.Equal(t, "ticket", name) // case-insensitive
	assert.Equal(t, "ABC-123", arg)
}

func TestFormatPrompt_NoTicket(t *testing.T) {
	p := shell.FormatPrompt("alice@org.com", "i-abc123", false, "")
	assert.Contains(t, p, "alice")
	assert.Contains(t, p, "i-abc123")
	assert.NotContains(t, p, "approved")
}

func TestFormatPrompt_WithTicket(t *testing.T) {
	p := shell.FormatPrompt("alice@org.com", "i-abc123", true, "")
	assert.Contains(t, p, "approved")
}

func TestFormatPrompt_EmailWithoutAt(t *testing.T) {
	p := shell.FormatPrompt("alice", "i-abc123", false, "")
	assert.Contains(t, p, "alice")
}

func TestFormatPrompt_CurrentDir(t *testing.T) {
	p := shell.FormatPrompt("alice@org.com", "i-abc123", false, "/var/log")
	assert.Contains(t, p, "/var/log")
	assert.NotContains(t, p, "~")
}

func TestFormatPrompt_HomeDir(t *testing.T) {
	p := shell.FormatPrompt("alice@org.com", "i-abc123", false, "")
	assert.Contains(t, p, "~")
}
