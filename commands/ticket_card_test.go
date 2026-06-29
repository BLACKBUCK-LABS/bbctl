package commands

import (
	"strings"
	"testing"

	"github.com/blackbuck/bbctl/internal/ui"
)

func TestTicketCard_ContainsKeyFields(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	got := ticketCard("REQ-4821", "https://jira/REQ-4821",
		"bbctl run i-0abc --ticket REQ-4821 -- systemctl restart x")
	for _, want := range []string{"REQ-4821", "https://jira/REQ-4821", "--ticket REQ-4821"} {
		if !strings.Contains(got, want) {
			t.Errorf("card missing %q: %q", want, got)
		}
	}
}
