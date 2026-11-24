package proc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindow makes a console child process (claude.exe, git, cmd.exe) run
// without opening a console window. Without it, a windowed parent like the
// desktop app makes Windows open a new terminal window for every child,
// which flashes on screen and disappears.
func NoWindow(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	return cmd
}
