package app

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/server"
)

func (a *App) Serve() error {
	srv, err := server.New(a.Store.Public(), a.IncomingPrefix(), a.RootDigest(), log.New(os.Stdout, "", 0))
	if err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	defer srv.Close()
	ln, err := server.Listen(a.Config.Port)
	if err != nil {
		return cli.Failf(cli.CodeServerDown, "could not bind 127.0.0.1:%d: %v", a.Config.Port, err)
	}
	fmt.Printf("serving %s on 127.0.0.1:%d\n", a.Store.Public(), a.Config.Port)
	return http.Serve(ln, srv)
}

// StartServer brings the server up through the launch agent, which is the only way a
// background server exists. Everything that needs the server comes through
// here. With no agent installed, it refuses and tells you the command to run.
func (a *App) StartServer() error {
	if a.ServerRunning() {
		return nil
	}
	if p, ok := a.otherRootServer(); ok {
		// Publish must not kill the server of another store because an environment variable was set in this shell.
		return cli.Failf(cli.CodeServerDown, "port %d is held by %s", a.Config.Port, p.describe()).
			WithHint("run 'otata restart' to replace it with one for " + a.Root + ", or set OTATA_PORT to a free port")
	}
	if a.PortBusy() {
		return cli.Failf(cli.CodeServerDown,
			"port %d is held by another process, not otata", a.Config.Port).
			WithHint("stop it, or set OTATA_PORT to a free port")
	}

	if !a.AutostartEnabled() {
		return cli.Fail(cli.CodeServerDown, "the server is not running").WithHint(startHint)
	}
	return a.reloadAgent()
}

// StopServer stops our server, and only ours.
// It identifies the process over HTTP instead of something like lsof,
// since a process holding a port doesn't necessarily mean it's otata.
func (a *App) StopServer() error {
	// launchd's KeepAlive respawns anything we signal, so while the agent is
	// loaded, stopping means booting the job out, not sending a signal.
	// The waits below ask whether ANY otata server still holds the port, not
	// whether ours does. A server for another root never counts as running,
	// and waiting on that would declare it stopped while it was still alive.
	gone := func() bool { _, ours := a.serverPID(); return !ours }

	// A server another root's launch agent keeps alive cannot be stopped from
	// here. Signaling it only makes launchd respawn it, and booting the job
	// out is that root's `autostart off`.
	if spec, loaded := a.foreignAgentLoaded(); loaded {
		if p, held := a.otherRootServer(); held && agentRootDigest(spec) == p.Root {
			return cli.Failf(cli.CodeServerDown,
				"port %d is held by an otata server for %s, kept alive by its launch agent",
				a.Config.Port, describeAgent(spec)).
				WithHint("run 'otata autostart off' to remove that agent (there is one per user), then 'otata autostart on' here")
		}
	}

	if a.agentLoaded() {
		if err := bootoutAgent(); err != nil {
			return err
		}
		for range 30 {
			if gone() {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	pid, ours := a.serverPID()
	if !ours {
		if a.PortBusy() {
			return cli.Failf(cli.CodeInternal,
				"port %d is held by another process, not otata; refusing to signal it", a.Config.Port)
		}
		return nil
	}
	if pid <= 0 {
		return cli.Failf(cli.CodeInternal, "the server did not report its pid")
	}
	proc, _ := os.FindProcess(pid) // never fails on Unix
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return cli.Failf(cli.CodeInternal, "could not signal the server (pid %d): %v", pid, err)
	}
	for range 30 {
		if gone() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cli.Failf(cli.CodeInternal, "server on port %d did not stop", a.Config.Port)
}
