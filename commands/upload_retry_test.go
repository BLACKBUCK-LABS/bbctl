package commands

import (
	"strings"
	"testing"

	"github.com/blackbuck/bbctl/internal/ui"
	"github.com/stretchr/testify/assert"
)

func TestUploadRetryCmd_RequiresRequestID(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	err := uploadRetryCmd.Args(uploadRetryCmd, []string{})
	if err == nil {
		t.Fatal("expected an error for zero args")
	}
	if !strings.Contains(err.Error(), "arg") {
		t.Errorf("expected a Cobra arg-count error, got: %v", err)
	}
	assert.NotNil(t, err)
}
