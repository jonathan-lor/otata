//go:build !unix

package builder

import "os/exec"

// ownProcessGroup has no process groups to arrange here; a cancelled command
// is killed the way exec does it by default, parent only.
func ownProcessGroup(*exec.Cmd) {}
