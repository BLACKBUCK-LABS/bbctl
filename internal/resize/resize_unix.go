//go:build !windows

// Package resize notifies callers when the terminal window size changes.
package resize

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Watch returns a channel that receives a value whenever the terminal is
// resized. Call stop() when done to release the signal handler.
func Watch() (ch <-chan struct{}, stop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)

	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-sigCh:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()

	var once sync.Once
	return out, func() {
		once.Do(func() {
			signal.Stop(sigCh)
			close(done)
		})
	}
}
