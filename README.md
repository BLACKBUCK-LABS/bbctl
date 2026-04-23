# bbctl

bbctl is Blackbuck's gated EC2 access tool. It replaces direct SSH/SSM with a controlled pipeline — every command is classified as safe, restricted, or denied; restricted commands need Jira approval; every execution is logged immutably to S3 for 13 months. Authenticates via Google SSO (blackbuck.com only).

## Install

```bash
brew install Blackbuck-LABS/bbctl/bbctl
```

Or download a binary from the [Releases](https://github.com/Blackbuck-LABS/bbctl/releases) page.

## First-time setup

```bash
bbctl login
```

Opens a browser for Google SSO. Token is saved to `~/.bbctl/config.yaml` and lasts ~1 hour. Run `bbctl login` again when it expires.

## Usage

### Run a single command

```bash
bbctl run i-0abc123def456 -- ls /var/log
bbctl run i-0abc123def456 -- tail -n 100 /var/log/app.log
```

The `--` separator is required. Anything after it is sent to the instance verbatim.

### Interactive shell

```bash
bbctl shell i-0abc123def456
```

Drops you into an interactive prompt. Each line is classified, executed, and audited independently — there is no persistent remote shell. Type `/exit` to leave, `/help` for shell commands.

### Restricted commands

If you run a restricted command (curl, kill, sudo, etc.) without a Jira ticket, bbctl creates one for you and prints the URL:

```
$ bbctl run i-0abc123def456 -- kill -9 12345
Restricted command. Created Jira ticket: https://blackbuck.atlassian.net/browse/REQ-1234
After your manager approves, re-run with:
  bbctl run i-0abc123def456 --ticket REQ-1234 -- kill -9 12345
```

Once approved, re-run with `--ticket REQ-XXXX`. The command in the ticket must match exactly.

One ticket = one execution. After running, the ticket transitions to "Access Granted" and cannot be reused.

### Other commands

```bash
bbctl commands         # list which commands are safe / restricted / denied
bbctl version          # show version, commit, build date
bbctl logout           # delete local token
```

## Configuration

`~/.bbctl/config.yaml` is created by `bbctl login`. The only field you might edit:

```yaml
backend_url: https://bbctl.blackbuck.com
```

Defaults are correct for production. Don't change unless you know what you're doing.

## Command categories

Run `bbctl commands` for the live list. Quick summary:

- **Safe (~22):** read-only and inspection commands (ls, cat, tail, ps, df, etc.) — run immediately, no approval.
- **Restricted (~80+):** anything that touches state or could exfiltrate data (curl, kill, sudo, systemctl, vi, mysql, etc.) — Jira approval required.
- **Denied (~50+):** shells, raw networking, kernel modules, anything that could escape the audit pipeline (bash, python, ssh, nc, dd, etc.) — never allowed, no approval will help.

## Limitations

- No pipes, redirects, or shell substitutions. `ps | grep java` will be rejected — run `pgrep java` instead. This is enforced by parsing the command as POSIX shell, not regex.
- No environment variable expansion. `cat $LOG` won't work — write the literal path.
- URL allowlist for curl/wget: only `*.blackbuck.com` and `*.jinka.in`.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `token expired` | Run `bbctl login` again. |
| `command not in any list` | Default-deny — the binary isn't classified. Ask DevOps to add it. |
| `ticket not found` or `ticket mismatch` | Ticket key wrong, ticket not in REQ project, command/instance/account doesn't match the ticket exactly. |
| `instance not reachable` | Target instance has no SSM agent, wrong region, or instance is stopped. |
| Browser doesn't open on `bbctl login` | Copy the URL printed in the terminal and open it manually. |

## Reporting issues

File an issue at https://github.com/Blackbuck-LABS/bbctl/issues or ping #infra-alert in Slack.

## License

Proprietary — Blackbuck internal use only.
