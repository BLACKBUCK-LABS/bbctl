package commands

import (
	"strings"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
)

// newBackendClient builds the backend client and, when the user is logged in to
// BOLT, attaches the BOLT token (saved by `bbctl login`) so the backend can
// authorize every command through Dwarpal. Without a BOLT login the backend
// answers 401.
func newBackendClient(cfg *config.Config, token string) *client.Client {
	c := client.New(cfg.BackendURL, token, "bbctl/"+Version)
	if dir, err := config.DefaultConfigDir(); err == nil {
		if boltToken, err := config.LoadBoltToken(dir, activeEnv); err == nil {
			if boltToken = strings.TrimSpace(boltToken); boltToken != "" {
				c.SetBoltToken(boltToken)
			}
		}
	}
	return c
}
