//go:build !darwin

package app

import "errors"

// none is the supervisor where there is no service manager, like a Linux
// without systemd.
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
