package app

/*
supervisor is whatever keeps the background server alive on this OS: launchd
on macOS, the user's own systemd manager on Linux, nothing where neither is
reachable. It speaks the manager's own language and nothing else. What an
installed unit means for this root, how one is brought to a bound server,
and what to say when it cannot be, is App's to decide, once, in autostart.go.

One unit exists per user, and it embeds the root, port and serve path it was
installed with, so a unit for another root is not this root's autostart.
*/
type supervisor interface {
	// Available reports whether a service manager exists here at all.
	Available() bool
	// Kind names the unit in the manager's own words, for messages: "launch agent".
	Kind() string
	// Installed is the unit this user has, if any, and what it says it runs.
	Installed() (agentSpec, bool)
	// Loaded reports whether the manager is running the unit, or trying to,
	// whoever it belongs to: the state in which signaling the server only
	// gets it respawned.
	Loaded() bool
	// Disabled reports whether the user switched the unit off at the
	// manager's own level, so that it will not return at login, whether or
	// not the manager would still load it now.
	Disabled() bool
	// Enable clears that switch. Only an explicit `autostart on` may.
	Enable()
	// Install writes the unit's definition, replacing any, such that the
	// manager would run it at next login. It loads nothing now. A program
	// the manager cannot run from where it is may be refused up front, as
	// an errAgentProgram.
	Install(spec agentSpec) error
	// Load asks the manager to run the installed unit. Binding the port is
	// no part of the answer; the caller waits for that.
	Load() error
	// Unload stops the manager managing the unit, leaving it installed. Not
	// loaded is not an error.
	Unload() error
	// Remove unloads the unit and deletes its definition. Nothing installed
	// is not an error.
	Remove() error
	// StartHint is what to run when the server is down and no unit is installed.
	StartHint() string
	// DisabledHint is where the user switched the unit off, and how to switch it back.
	DisabledHint() string
}

// autostart is this process's supervisor, made on first use so an App built
// by hand, as the tests do, gets the platform's without naming it.
func (a *App) autostart() supervisor {
	if a.sup == nil {
		a.sup = newSupervisor()
	}
	return a.sup
}
