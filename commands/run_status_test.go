package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackbuck/bbctl/internal/client"
	"github.com/blackbuck/bbctl/internal/config"
	"github.com/blackbuck/bbctl/internal/ui"
)

func plainUI() { ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false} }

func TestApiErrorText_PlainNonTTY(t *testing.T) {
	plainUI()
	got := apiErrorText(&client.APIError{HTTPStatus: 429})
	if !strings.Contains(got, "Rate limit") {
		t.Errorf("want rate-limit copy, got %q", got)
	}
	if strings.Contains(got, "\033") {
		t.Errorf("leaked ANSI on non-TTY: %q", got)
	}
}

func TestApiErrorText_401(t *testing.T) {
	plainUI()
	got := apiErrorText(&client.APIError{HTTPStatus: 401})
	if !strings.Contains(got, "bbctl login") {
		t.Errorf("want login hint, got %q", got)
	}
}

func TestApiErrorText_403_ShowsServerReason(t *testing.T) {
	plainUI()
	got := apiErrorText(&client.APIError{HTTPStatus: 403, Message: "Permission Denied by BBCTL"})
	if !strings.Contains(got, "Permission Denied by BBCTL") {
		t.Errorf("want server message, got %q", got)
	}
	if strings.Contains(got, "backend error") {
		t.Errorf("should show the server's words, not the wrapped error: %q", got)
	}
}

func TestApiErrorText_409_ShowsServerMessage(t *testing.T) {
	plainUI()
	msg := "Please wait untill the ongoing request is completed."
	got := apiErrorText(&client.APIError{HTTPStatus: 409, Message: msg})
	if !strings.Contains(got, msg) {
		t.Errorf("want %q, got %q", msg, got)
	}
}

func TestApiErrorText_402_Unchanged(t *testing.T) {
	plainUI()
	got := apiErrorText(&client.APIError{HTTPStatus: 402})
	if !strings.Contains(got, "manager approval") {
		t.Errorf("402 text must stay for upload/download, got %q", got)
	}
}

func TestLoadRunBoltToken_Missing_ReturnsLoginError(t *testing.T) {
	dir := t.TempDir()
	_, err := loadRunBoltToken(dir, "dev")
	if err == nil || !strings.Contains(err.Error(), "bbctl login") {
		t.Fatalf("want login error, got %v", err)
	}
}

func TestLoadRunBoltToken_Missing_ProdMentionsProdLogin(t *testing.T) {
	dir := t.TempDir()
	_, err := loadRunBoltToken(dir, "prod")
	if err == nil || !strings.Contains(err.Error(), "bbctl prod login") {
		t.Fatalf("want prod login error, got %v", err)
	}
}

func TestLoadRunBoltToken_Blank_ReturnsLoginError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bolt_token_dev"), []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRunBoltToken(dir, "dev"); err == nil {
		t.Fatal("blank token must be treated as not logged in")
	}
}

func TestLoadRunBoltToken_Present_ReturnsTrimmed(t *testing.T) {
	dir := t.TempDir()
	if err := config.SaveBoltToken(dir, "dev", "abc.def.ghi\n"); err != nil {
		t.Fatal(err)
	}
	got, err := loadRunBoltToken(dir, "dev")
	if err != nil || got != "abc.def.ghi" {
		t.Fatalf("got %q, %v", got, err)
	}
}
