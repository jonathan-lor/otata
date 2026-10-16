//go:build unix

package builder

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts cmd in its own process group and makes cancelling it
// signal the whole group. The terminal's Ctrl-C no longer reaches the child,
// so publish's signal handler does the cancelling.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
