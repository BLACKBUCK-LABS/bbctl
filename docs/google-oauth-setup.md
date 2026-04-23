# Google OAuth Setup for bbctl

## Normal users: nothing to do

The bbctl binary released via Homebrew or the GitHub Releases page has the Google
OAuth client secret injected at build time. Just run:

```bash
bbctl login
```

No environment variables, no config file, no setup required.

## What is BBCTL_OIDC_CLIENT_SECRET?

`BBCTL_OIDC_CLIENT_SECRET` is an **override** for the build-injected OAuth 2.0
client secret. You only need it if you are:

- Building bbctl from source (the secret is not in the source tree)
- Pointing bbctl at a different Google OAuth client (e.g. your own deployment)

### Why is the secret not in source?

bbctl uses a Google OAuth **Desktop App** client. For this client type, the
"secret" is not cryptographically sensitive — PKCE (Proof Key for Code Exchange)
protects the flow, not the client secret. However, having it in source triggers
GitHub push protection and creates unnecessary noise in the git history. The
solution is to inject it at build time via `-ldflags` so it lives only in the
compiled binary, not in the repository.

## Advanced: building from source

Local builds (e.g. `go build ./cmd/bbctl`) produce a binary with an empty client
secret. To use `bbctl login` with a locally built binary, set the env var:

```bash
export BBCTL_OIDC_CLIENT_SECRET=GOCSPX-...
bbctl login
```

Or for a single invocation:

```bash
BBCTL_OIDC_CLIENT_SECRET=GOCSPX-... bbctl login
```

Ask DevOps for the secret value if you need it for a local build.

Release builds (GitHub Actions) get it injected automatically via the
`OIDC_CLIENT_SECRET` repository secret — no manual step needed for CI.

## Advanced: overriding the OAuth client

If you are running your own bbctl backend with a different Google OAuth client:

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Select your project → **APIs & Services → Credentials**
3. Find your OAuth 2.0 Desktop App client and note the client ID and secret

Set the override before running `bbctl login`:

```bash
export BBCTL_OIDC_CLIENT_SECRET=GOCSPX-...
bbctl login
```

You will also want to override the other OIDC fields and backend URL via
`~/.bbctl/config.yaml` or `BBCTL_BACKEND_URL`.

## Security note

The client secret authenticates the **application** to Google, not the user.
Do not log it or paste it into Slack or issues. If it is ever compromised,
rotate it via Google Cloud Console, update the `OIDC_CLIENT_SECRET` repository
secret, and ship a new binary release.
