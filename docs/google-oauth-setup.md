# Google OAuth Setup for bbctl login

## What is BBCTL_OIDC_CLIENT_SECRET?

`BBCTL_OIDC_CLIENT_SECRET` is the OAuth 2.0 client secret issued by Google for
the bbctl desktop application. Google's device authorization flow requires the
client secret during the token exchange step, even though bbctl is a public
(installed) application.

The secret must **never** be committed to git or written to `~/.bbctl/config.yaml`.
It is read exclusively from the environment variable `BBCTL_OIDC_CLIENT_SECRET`.

## Where to get it

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Select the **Jenkins Login** project
3. Navigate to **APIs & Services → Credentials**
4. Find the OAuth 2.0 client named **bbctl**
5. Click the download icon to view the client secret (starts with `GOCSPX-`)

## How to set it

Add to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.):

```bash
export BBCTL_OIDC_CLIENT_SECRET=GOCSPX-...
```

Or set it for a single session:

```bash
export BBCTL_OIDC_CLIENT_SECRET=GOCSPX-... bbctl login
```

## ~/.bbctl/config.yaml

Create (or update) `~/.bbctl/config.yaml` with the following:

```yaml
# Google OAuth settings for bbctl login
oidc_issuer: https://accounts.google.com
oidc_client_id: 396628175360-g90ptoadcl2coqrtk09oa2625a0k4ppf.apps.googleusercontent.com
oidc_auth_endpoint: https://accounts.google.com/o/oauth2/v2/auth
oidc_token_endpoint: https://oauth2.googleapis.com/token
# client secret is set via env var BBCTL_OIDC_CLIENT_SECRET, never in this file

backend_url: https://<your-bbctl-backend-host>
```

## Security note

The client secret authenticates the **application**, not the user. It is
still sensitive — anyone with it can initiate OAuth flows as the bbctl app.
Treat it like a password: do not log it, do not paste it into Slack or issues,
and rotate it via Google Cloud Console if it is ever exposed.
