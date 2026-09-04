//go:build unix

package builder

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts cmd in a process group of its own and makes a
// cancellation signal that group rather than the one process. Setpgid means
// the group id is the child's pid, which is what the negative pid addresses.
//
// A side effect worth knowing: the terminal's own Ctrl-C goes to the
// foreground group, which the child is no longer in. The publish's signal
// handler is what forwards it, so a build dies with the command that started
// it exactly as before, and now also on a SIGTERM or a dropped SSH session,
// which the terminal never forwarded.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
