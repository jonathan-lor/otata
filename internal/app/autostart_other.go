//go:build !darwin

package app

import "github.com/jonathan-lor/otata/internal/cli"

func (a *App) AutostartEnabled() bool { return false }

func (a *App) agentLoaded() bool { return false }

// readAgentPlist has nothing to read where no agent can be installed.
func readAgentPlist() (agentSpec, bool) { return agentSpec{}, false }

func (a *App) AutostartProgram() (string, bool) { return "", false }

// bootoutAgent has nothing to unload where autostart is not implemented.
func bootoutAgent() error { return nil }

// agentDisabled has no launchd to consult.
func agentDisabled() bool { return false }

// errAgentDisabled is unreachable where agentDisabled is always false.
func errAgentDisabled() *cli.Failure {
	return cli.Fail(cli.CodeServerDown, "no launch agent exists on this platform")
}

// foreignAgentLoaded has no launchd job to find.
func (a *App) foreignAgentLoaded() (agentSpec, bool) { return agentSpec{}, false }

// startHint is what to run when the server is down; there is no launchd here.
const startHint = "run 'otata serve' under your init system, or in a foreground terminal"

// reloadAgent is unreachable. StartServer refuses first, because
// AutostartEnabled is always false where no agent can be installed.
func (a *App) reloadAgent() error {
	return cli.Fail(cli.CodeServerDown, "no launch agent exists on this platform").WithHint(startHint)
}

func (a *App) EnableAutostart() error {
	return cli.Fail(cli.CodeInvalidArgs, "autostart is only implemented on macOS").
		WithHint("run 'otata serve' under your init system instead")
}

func (a *App) DisableAutostart() error { return nil }
