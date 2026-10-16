//go:build !unix

package builder

import "os/exec"

// ownProcessGroup does nothing here, so cancelling kills only the parent.
func ownProcessGroup(*exec.Cmd) {}
