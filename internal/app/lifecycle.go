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
	if a.Config.Transport == "tailscale" {
		return a.serveTSNet(srv)
	}
	ln, err := server.Listen(a.Config.Port)
	if err != nil {
		return cli.Failf(cli.CodeServerDown, "could not bind 127.0.0.1:%d: %v", a.Config.Port, err)
	}
	fmt.Printf("serving %s on 127.0.0.1:%d\n", a.Store.Public(), a.Config.Port)
	return http.Serve(ln, srv)
}

// StartServer brings the server up through its unit, the only way a background
// server runs. With no unit installed, it refuses with the setup command.
func (a *App) StartServer() error {
	if p, ok := a.probeServer("/"); ok {
		if p.Root == a.RootDigest() {
			return nil
		}
		// Never replace another root's server implicitly.
		return cli.Failf(cli.CodeServerDown, "port %d is held by %s", a.Config.Port, p.describe()).
			WithHint("run 'otata restart' to replace it with one for " + a.Root + ", or set OTATA_PORT to a free port")
	}
	if a.PortBusy() {
		return cli.Failf(cli.CodeServerDown,
			"port %d is held by another process, not otata", a.Config.Port).
			WithHint("stop it, or set OTATA_PORT to a free port")
	}

	if !a.AutostartEnabled() {
		return cli.Fail(cli.CodeServerDown, "the server is not running").WithHint(a.autostart().StartHint())
	}
	return a.reloadAgent()
}

// StopServer stops the otata server on the port, which it identifies over
// HTTP, so it never signals another process.
func (a *App) StopServer() error {
	// Wait for any otata server to leave the port, not just this root's.
	gone := func() bool { _, ours := a.serverPID(); return !ours }
	// tsnet can take five seconds to close, so the wait covers that.
	waitForStop := func() bool {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if gone() {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Another root's unit would just respawn its server.
	if spec, loaded := a.foreignAgentLoaded(); loaded {
		if p, held := a.otherRootServer(); held && agentRootDigest(spec) == p.Root {
			return cli.Failf(cli.CodeServerDown,
				"port %d is held by an otata server for %s, kept alive by its %s",
				a.Config.Port, describeAgent(spec), a.autostart().Kind()).
				WithHint("run 'otata autostart off' to remove it (there is one per user), then 'otata autostart on' here")
		}
	}

	if a.agentLoaded() {
		if err := a.autostart().Unload(); err != nil {
			return cli.Failf(cli.CodeInternal, "could not unload the %s: %v", a.autostart().Kind(), err)
		}
		if waitForStop() {
			return nil
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
	if waitForStop() {
		return nil
	}
	return cli.Failf(cli.CodeInternal, "server on port %d did not stop", a.Config.Port)
}
