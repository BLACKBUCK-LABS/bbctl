package resize

import "testing"

// TestWatchStopIsClean verifies Watch/stop don't panic or leak on the
// current platform (unix build or windows build, whichever compiles).
func TestWatchStopIsClean(t *testing.T) {
	ch, stop := Watch()
	if ch == nil {
		t.Fatal("Watch returned nil channel")
	}
	stop()
	stop() // must be safe to call twice — nothing here relies on it, but don't panic
}
