package proc

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// SelfToken identifies this process as "pid:creation time". A pid alone is
// not enough because Windows reuses pids; the creation time tells a new
// process that got the same pid apart from the old one.
func SelfToken() string {
	return token(windows.CurrentProcess(), windows.GetCurrentProcessId())
}

func token(h windows.Handle, pid uint32) string {
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", pid, created.Nanoseconds())
}

// Alive reports whether the process a token names is still running.
// An empty or malformed token counts as not running.
func Alive(tok string) bool {
	pidText, _, ok := strings.Cut(tok, ":")
	if !ok {
		return false
	}
	pid, err := strconv.ParseUint(pidText, 10, 32)
	if err != nil {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != 259 /* STILL_ACTIVE */ {
		return false
	}
	return token(h, uint32(pid)) == tok
}
