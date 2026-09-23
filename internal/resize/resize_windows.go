//go:build windows

// Package resize notifies callers when the terminal window size changes.
package resize

import (
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// pollInterval is how often we check for a size change. Windows has no
// SIGWINCH equivalent for console resize, so this polls golang.org/x/term
// instead. 250ms is imperceptible to a user but cheap enough to run for the
// life of a shell session.
const pollInterval = 250 * time.Millisecond

// Watch returns a channel that receives a value whenever the terminal is
// resized. Call stop() when done to release the polling goroutine.
func Watch() (ch <-chan struct{}, stop func()) {
	out := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		lastW, lastH, _ := term.GetSize(int(os.Stdout.Fd()))
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				w, h, err := term.GetSize(int(os.Stdout.Fd()))
				if err != nil {
					continue
				}
				if w != lastW || h != lastH {
					lastW, lastH = w, h
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	var once sync.Once
	return out, func() { once.Do(func() { close(done) }) }
}
