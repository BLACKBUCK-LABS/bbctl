package commands

import "testing"

func TestIsAuthErrText(t *testing.T) {
	cases := map[string]bool{
		"divum: 401 unauthorized":      true,
		"HTTP 401":                     true,
		"request failed: Unauthorized": true,
		"list databases: 401":          true,
		"connection refused":           false,
		"context deadline exceeded":    false,
		"":                             false,
		"500 internal server error":    false,
	}
	for in, want := range cases {
		if got := isAuthErrText(in); got != want {
			t.Errorf("isAuthErrText(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAnyAuthErr(t *testing.T) {
	if !anyAuthErr([]string{"divum: connection refused", "zinka: 401 unauthorized"}) {
		t.Error("expected true when any error is auth-related")
	}
	if anyAuthErr([]string{"divum: connection refused", "zinka: timeout"}) {
		t.Error("expected false when no error is auth-related")
	}
	if anyAuthErr(nil) {
		t.Error("expected false for empty error list")
	}
}
