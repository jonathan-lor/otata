//go:build !darwin

package app

import "errors"

// none is the supervisor where no service manager is reachable: an OS with
// none wired up, or a Linux shell with no user manager behind it (WSL 1, a
// container, a session-less login). Nothing is ever installed, loaded or
// disabled, and the server lives only as long as the terminal or init system
// that runs `otata serve`. macOS never needs it, since launchd is always there.
type none struct{}

var errNoManager = errors.New("no service manager is reachable from here")

func (none) Available() bool              { return false }
func (none) Kind() string                 { return "autostart service" }
func (none) Installed() (agentSpec, bool) { return agentSpec{}, false }
func (none) Loaded() bool                 { return false }
func (none) Disabled() bool               { return false }
func (none) Enable()                      {}
func (none) Install(agentSpec) error      { return errNoManager }
func (none) Load() error                  { return errNoManager }
func (none) Unload() error                { return nil }
func (none) Remove() error                { return nil }
func (none) StartHint() string {
	return "run 'otata serve' under your init system, or in a foreground terminal"
}
func (none) DisabledHint() string { return "" }
