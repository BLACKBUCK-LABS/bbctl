package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderBar_Midway(t *testing.T) {
	Std = Caps{Color: false, Unicode: true}
	got := RenderBar(5, 10, 10)
	if !strings.Contains(got, "50%") {
		t.Errorf("RenderBar = %q, want it to contain 50%%", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:     "512 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1048576: "1.0 MB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCountingReaderNonTTY(t *testing.T) {
	// Test that CountingReader does NOT draw on non-TTY
	Std = Caps{TTY: false}
	r := bytes.NewReader([]byte("hello world"))
	w := bytes.NewBuffer(nil)
	cr := NewCountingReader(r, 11, w)

	// Read all bytes
	p := make([]byte, 100)
	n, _ := cr.Read(p)

	// Check that nothing was written (no ANSI codes)
	if w.Len() != 0 {
		t.Errorf("CountingReader wrote to non-TTY: %q", w.String())
	}
	if n != 11 {
		t.Errorf("Read bytes: %d, want 11", n)
	}
}

func TestCountingReaderTTY(t *testing.T) {
	// Test that CountingReader DOES draw on TTY
	Std = Caps{TTY: true, Unicode: true}
	r := bytes.NewReader([]byte("hello"))
	w := bytes.NewBuffer(nil)
	cr := NewCountingReader(r, 5, w)

	// Read all bytes
	p := make([]byte, 100)
	n, _ := cr.Read(p)

	// Check that something was written
	if w.Len() == 0 {
		t.Errorf("CountingReader did not write to TTY")
	}
	if n != 5 {
		t.Errorf("Read bytes: %d, want 5", n)
	}
}

func TestCountingReaderFinish(t *testing.T) {
	Std = Caps{TTY: true}
	r := bytes.NewReader([]byte("x"))
	w := bytes.NewBuffer(nil)
	cr := NewCountingReader(r, 1, w)

	// Read byte
	p := make([]byte, 1)
	cr.Read(p)
	w.Reset()

	// Finish should clear the line with ANSI code
	cr.Finish()
	output := w.String()
	if !strings.Contains(output, "\033[K") {
		t.Errorf("Finish() did not contain ANSI clear code, got: %q", output)
	}
}

func TestCountingReaderFinishNonTTY(t *testing.T) {
	Std = Caps{TTY: false}
	r := bytes.NewReader([]byte("x"))
	w := bytes.NewBuffer(nil)
	cr := NewCountingReader(r, 1, w)

	cr.Read(make([]byte, 1))
	w.Reset()

	// Finish should NOT write anything on non-TTY
	cr.Finish()
	if w.Len() != 0 {
		t.Errorf("Finish() wrote to non-TTY: %q", w.String())
	}
}
