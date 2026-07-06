//go:build windows

package commands

import (
	"fmt"

	"github.com/blackbuck/bbctl/internal/config"
)

func runBoltShell(relayURL, token, instanceID, instanceName string) error {
	return fmt.Errorf("BOLT is not supported on Windows")
}

func boltEnvAndToken(cfg *config.Config, configDir string) (relayURL, token string, err error) {
	return "", "", fmt.Errorf("BOLT is not supported on Windows")
}
