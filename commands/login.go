package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/blackbuck/bbctl/internal/config"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the SSO provider and store a token",
	RunE:  runLogin,
}

func runLogin(cmd *cobra.Command, args []string) error {
	configDir, err := config.DefaultConfigDir()
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(configDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.OIDCAuthEndpoint == "" {
		return errors.New("oidc_auth_endpoint not set in ~/.bbctl/config.yaml")
	}
	if cfg.OIDCTokenEndpoint == "" {
		return errors.New("oidc_token_endpoint not set in ~/.bbctl/config.yaml")
	}
	if cfg.OIDCClientID == "" {
		return errors.New("oidc_client_id not set in ~/.bbctl/config.yaml")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	idToken, err := runLoopbackFlow(ctx, cfg)
	if err != nil {
		return err
	}
	if err := config.SaveToken(configDir, idToken); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	if err := config.WriteDefaultConfig(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "note: could not write config.yaml: %v\n", err)
	}

	email := emailFromIDToken(idToken)
	if email != "" {
		fmt.Printf("✓ Logged in as %s\n", email)
	} else {
		fmt.Println("✓ Logged in. Token stored.")
	}
	return nil
}

func runLoopbackFlow(ctx context.Context, cfg *config.Config) (string, error) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return "", fmt.Errorf("start local callback server: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d", port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if e := r.URL.Query().Get("error"); e != "" {
			desc := r.URL.Query().Get("error_description")
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, errorHTML, e, desc)
			errCh <- fmt.Errorf("oauth error: %s — %s", e, desc)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, successHTML)
		codeCh <- code
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	authURL := cfg.OIDCAuthEndpoint +
		"?client_id=" + url.QueryEscape(cfg.OIDCClientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&response_type=code" +
		"&scope=" + url.QueryEscape("openid email profile") +
		"&access_type=offline" +
		"&prompt=consent"

	fmt.Println("Opening browser for Google login...")
	fmt.Printf("If browser didn't open, visit:\n  %s\n\n", authURL)
	openBrowser(authURL)

	select {
	case code := <-codeCh:
		return exchangeCode(ctx, cfg, code, redirectURI)
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", errors.New("login timed out after 2 minutes — please try again")
	}
}

func exchangeCode(ctx context.Context, cfg *config.Config, code, redirectURI string) (string, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"client_id":    {cfg.OIDCClientID},
		"redirect_uri": {redirectURI},
	}
	if cfg.OIDCClientSecret != "" {
		data.Set("client_secret", cfg.OIDCClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.OIDCTokenEndpoint,
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("token exchange: parse response: %w", err)
	}
	if tok.Error != "" {
		return "", fmt.Errorf("token exchange failed: %s — %s", tok.Error, tok.Desc)
	}
	if tok.IDToken == "" {
		return "", fmt.Errorf("token exchange: no id_token in response: %s", string(body))
	}
	return tok.IDToken, nil
}

// emailFromIDToken extracts the email claim from the JWT payload without
// verifying the signature (the backend verifies it).
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Email
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", u)
	default:
		return
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Start()
}

const successHTML = `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>bbctl &mdash; Logged in</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI',
        Roboto, sans-serif;
      background: #0f1117;
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100vh;
    }
    .card {
      background: #1a1d27;
      border: 1px solid #2a2d3a;
      border-radius: 16px;
      padding: 48px 56px;
      text-align: center;
      max-width: 440px;
      width: 90%;
    }
    .icon { font-size: 52px; margin-bottom: 20px; }
    h1 {
      color: #ffffff;
      font-size: 22px;
      font-weight: 600;
      margin-bottom: 10px;
    }
    .subtitle {
      color: #8b8fa8;
      font-size: 14px;
      margin-bottom: 28px;
      line-height: 1.5;
    }
    .cmd {
      background: #0f1117;
      border: 1px solid #2a2d3a;
      border-radius: 8px;
      padding: 12px 20px;
      font-family: 'SF Mono', 'Fira Code', monospace;
      font-size: 13px;
      color: #7ee787;
      margin-bottom: 32px;
    }
    .footer {
      color: #4a4d5e;
      font-size: 12px;
      border-top: 1px solid #2a2d3a;
      padding-top: 20px;
      line-height: 1.6;
    }
    .footer a {
      color: #6e7aff;
      text-decoration: none;
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">&#x1F510;</div>
    <h1>You're logged in!</h1>
    <p class="subtitle">
      Authentication successful.<br>
      You can close this tab and return to your terminal.
    </p>
    <div class="cmd">bbctl run &lt;instance-id&gt; -- &lt;command&gt;</div>
    <div class="footer">
      bbctl &mdash; Gated EC2 Access<br>
      Built by Krishna<br>
      <a href="https://github.com/Blackbuck-LABS">Blackbuck Labs</a>
    </div>
  </div>
</body>
</html>`

// errorHTML is a template; callers must pass two %s args: error code and description.
const errorHTML = `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>bbctl &mdash; Login failed</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI',
        Roboto, sans-serif;
      background: #0f1117;
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100vh;
    }
    .card {
      background: #1a1d27;
      border: 1px solid #3a2020;
      border-radius: 16px;
      padding: 48px 56px;
      text-align: center;
      max-width: 440px;
      width: 90%%;
    }
    .icon { font-size: 52px; margin-bottom: 20px; }
    h1 {
      color: #ffffff;
      font-size: 22px;
      font-weight: 600;
      margin-bottom: 10px;
    }
    .subtitle {
      color: #8b8fa8;
      font-size: 14px;
      margin-bottom: 28px;
      line-height: 1.5;
    }
    .error-box {
      background: #0f1117;
      border: 1px solid #3a2020;
      border-radius: 8px;
      padding: 12px 20px;
      font-family: 'SF Mono', 'Fira Code', monospace;
      font-size: 13px;
      color: #f85149;
      margin-bottom: 32px;
      word-break: break-word;
    }
    .footer {
      color: #4a4d5e;
      font-size: 12px;
      border-top: 1px solid #2a2d3a;
      padding-top: 20px;
      line-height: 1.6;
    }
    .footer a {
      color: #6e7aff;
      text-decoration: none;
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">&#x274C;</div>
    <h1>Login failed</h1>
    <p class="subtitle">
      Authentication was not completed.<br>
      You can close this tab and try again.
    </p>
    <div class="error-box">%s: %s</div>
    <div class="footer">
      bbctl &mdash; Gated EC2 Access<br>
      Built by Krishna<br>
      <a href="https://github.com/Blackbuck-LABS">Blackbuck Labs</a>
    </div>
  </div>
</body>
</html>`
