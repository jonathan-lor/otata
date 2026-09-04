//go:build !darwin

package app

import "errors"

func newSupervisor() supervisor { return none{} }

// none is the supervisor where no service manager is wired up. Nothing is
// ever installed, loaded or disabled, and the server lives only as long as
// the terminal or init system that runs `otata serve`.
type none struct{}

var errNoManager = errors.New("no service manager is wired up on this platform")

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
