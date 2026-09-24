//go:build windows

package commands

import (
	"fmt"
	"syscall"
	"unsafe"
)

// openBrowserWindows opens u in the default browser via the Win32
// ShellExecuteW API — the same call Explorer itself uses to open a URL.
// Unlike "cmd /c start" or "rundll32 url.dll,FileProtocolHandler", it never
// re-parses the URL through a command-line shell: cmd.exe's line parser
// splits on unquoted "&" (every OAuth URL has several), and url.dll's
// FileProtocolHandler has a long-standing bug that silently truncates URLs
// around ~170-215 characters — both corrupt a Google OAuth URL, which
// commonly runs 200+ characters.
func openBrowserWindows(u string) error {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExecuteW := shell32.NewProc("ShellExecuteW")

	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return fmt.Errorf("encode verb: %w", err)
	}
	target, err := syscall.UTF16PtrFromString(u)
	if err != nil {
		return fmt.Errorf("encode url: %w", err)
	}

	const swShowNormal = 1
	ret, _, _ := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		swShowNormal,
	)
	// ShellExecuteW returns a value > 32 on success, an error code otherwise.
	if ret <= 32 {
		return fmt.Errorf("ShellExecuteW failed with code %d", ret)
	}
	return nil
}
