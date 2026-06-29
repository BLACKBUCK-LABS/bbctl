package commands

import (
	"strings"
	"testing"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/ui"
)

func TestApiErrorText_PlainNonTTY(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	got := apiErrorText(&client.APIError{HTTPStatus: 429})
	if !strings.Contains(got, "Rate limit") {
		t.Errorf("want rate-limit copy, got %q", got)
	}
	if strings.Contains(got, "\033") {
		t.Errorf("leaked ANSI on non-TTY: %q", got)
	}
}
