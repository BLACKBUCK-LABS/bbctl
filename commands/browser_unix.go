//go:build !windows

package commands

// openBrowserWindows only exists so login.go's runtime.GOOS switch compiles
// on every platform. It is never reached outside a windows build — the real
// implementation lives in browser_windows.go.
func openBrowserWindows(_ string) error { return nil }
