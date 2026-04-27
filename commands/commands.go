package commands

import (
	"fmt"

	"github.com/blackbuck/bbctl/internal/shell"
	"github.com/spf13/cobra"
)

var commandsCmd = &cobra.Command{
	Use:   "commands",
	Short: "Show all safe, restricted, and denied commands",
	Example: `  bbctl run i-0abc123 -- ls /tmp
  bbctl run i-0abc123 -a divum -- ls /tmp
  bbctl shell i-0abc123 -a finserv
  bbctl run i-0abc123 -a tzf --ticket REQ-123 -- curl https://api.internal.com
  bbctl upload i-0abc123 ./dump.sql /tmp/dump.sql
  bbctl download i-0abc123 /var/log/app.log ./app.log`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(shell.SafeCommandsTable)
		fmt.Println(`
RESTRICTED COMMANDS (require Jira approval — auto-created on first run):
  curl, wget, sudo, systemctl, service, supervisorctl,
  kill, pkill, rm, mv, cp, chmod, chown, tee,
  vi, nano, touch, java, jar, jps, jinfo, jmap, jstack, jstat,
  pip, pip3, npm, yarn, pm2, gunicorn, uwsgi, celery,
  mysql, mysqldump, psql, pg_dump, mongosh, redis-cli,
  apt, apt-get, nginx, apache2, journalctl, dmesg,
  ping, traceroute, nmap, vmstat, iostat, sar,
  tar, zip, unzip, python3 <script>, bash <script>,
  node <script>, and many more...

DENIED COMMANDS (never allowed, no exceptions):
  bash/sh/zsh/python/node (bare — no script path)
  nc, ncat, socat, dd, mkfs,
  screen, tmux, nohup, ssh, scp,
  awk, gawk, and more`)
	},
}

func init() {
	rootCmd.AddCommand(commandsCmd)
}
