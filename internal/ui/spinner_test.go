package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestSpinner_NonTTYPrintsOnce(t *testing.T) {
	Std = Caps{TTY: false, Color: false, Unicode: false}
	var buf bytes.Buffer
	sp := NewSpinnerOut(&buf, "Loading instances")
	sp.Start()
	sp.StopOK("Loaded 12 instances")
	out := buf.String()
	if !strings.Contains(out, "Loading instances") {
		t.Errorf("expected start message, got %q", out)
	}
	if !strings.Contains(out, "[OK] Loaded 12 instances") {
		t.Errorf("expected OK summary, got %q", out)
	}
	if strings.Contains(out, "\033") {
		t.Errorf("non-TTY output leaked ANSI: %q", out)
	}
}

func TestSpinner_StopErr(t *testing.T) {
	Std = Caps{TTY: false, Color: false, Unicode: false}
	var buf bytes.Buffer
	sp := NewSpinnerOut(&buf, "Connecting")
	sp.Start()
	sp.StopErr("timeout")
	if !strings.Contains(buf.String(), "[x] timeout") {
		t.Errorf("expected error summary, got %q", buf.String())
	}
}

func TestSpinner_TTYDoubleStopNoPanic(t *testing.T) {
	Std = Caps{TTY: true, Color: false, Unicode: false}
	var buf bytes.Buffer
	sp := NewSpinnerOut(&buf, "Working")
	sp.Start()
	sp.StopOK("done")
	sp.Stop() // second stop must NOT panic
	if !strings.Contains(buf.String(), "[OK] done") {
		t.Errorf("expected OK summary, got %q", buf.String())
	}
}
