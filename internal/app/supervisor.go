package app

// supervisor keeps the background server alive: launchd on macOS, the user's
// systemd on Linux, or none. There is one unit per user, embedding the root
// and port it was installed with.
type supervisor interface {
	// Available reports whether a service manager exists here at all.
	Available() bool
	// Kind names the unit in the manager's own words, for messages: "launch agent".
	Kind() string
	// Installed is the unit this user has, if any, and what it says it runs.
	Installed() (agentSpec, bool)
	// Loaded reports whether the manager is running the unit, whichever root it serves.
	Loaded() bool
	// Disabled reports whether the user switched the unit off in the manager.
	Disabled() bool
	// Enable clears that switch. Only an explicit `autostart on` may.
	Enable()
	// Install writes the unit to run at next login without loading it. It may
	// refuse the program up front with errAgentProgram.
	Install(spec agentSpec) error
	// Load starts the installed unit. The caller waits for it to bind.
	Load() error
	// Unload stops the unit and leaves it installed.
	Unload() error
	// Remove unloads and deletes the unit.
	Remove() error
	// StartHint is what to run when the server is down and no unit is installed.
	StartHint() string
	// DisabledHint says how to undo the user's switch.
	DisabledHint() string
}

// autostart returns the supervisor, creating the platform's on first use.
func (a *App) autostart() supervisor {
	if a.sup == nil {
		a.sup = newSupervisor()
	}
	return a.sup
}
