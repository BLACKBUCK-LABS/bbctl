package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var spinnerASCII = []string{"|", "/", "-", "\\"}

// Spinner is a single-line progress indicator writing to w.
type Spinner struct {
	w    io.Writer
	msg  string
	stop chan struct{}
	done sync.WaitGroup
	once sync.Once
}

// NewSpinner creates a spinner that writes to stderr.
func NewSpinner(msg string) *Spinner { return NewSpinnerOut(os.Stderr, msg) }

// NewSpinnerOut creates a spinner writing to w (used in tests).
func NewSpinnerOut(w io.Writer, msg string) *Spinner {
	return &Spinner{w: w, msg: msg, stop: make(chan struct{})}
}

// Start begins animation. On a non-TTY it prints the message once.
func (s *Spinner) Start() {
	if !Std.TTY {
		fmt.Fprintf(s.w, "%s…\n", s.msg)
		return
	}
	frames := spinnerASCII
	if Std.Unicode {
		frames = spinnerFrames
	}
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		i := 0
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				fmt.Fprintf(s.w, "\r%s %s", Render(Brand, frames[i%len(frames)]), s.msg)
				i++
			}
		}
	}()
}

func (s *Spinner) clear() {
	if !Std.TTY {
		return
	}
	s.once.Do(func() {
		close(s.stop)
		s.done.Wait()
		fmt.Fprintf(s.w, "\r\033[K") // carriage return + clear to EOL
	})
}

// Stop clears the spinner line, leaving nothing behind.
func (s *Spinner) Stop() { s.clear() }

// StopOK clears the line and prints a success summary.
func (s *Spinner) StopOK(msg string) {
	s.clear()
	fmt.Fprintln(s.w, Success(msg))
}

// StopErr clears the line and prints an error summary.
func (s *Spinner) StopErr(msg string) {
	s.clear()
	fmt.Fprintln(s.w, Err(msg))
}
