//go:build windows

package commands

import (
	"fmt"

	"github.com/blackbuck/bbctl/internal/config"
)

func runPTYShell(cfg *config.Config, token, instanceID, accountID string) error {
	return fmt.Errorf("PTY shell is not supported on Windows")
}
