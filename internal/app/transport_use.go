package app

import (
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/transport"
	"github.com/jonathan-lor/otata/internal/tsnetnode"
)

// TransportSelection is what `otata transport use` was asked for.
// The manual fields only apply to the manual transport.
type TransportSelection struct {
	Name       string
	BaseURL    string
	KeepPrefix bool
	Hostname   string
}

// UseTransport selects the transport every later command will serve through,
// persists the choice, and restarts a server whose transport settings changed.
// Pages are regenerated once the selected transport is ready.
func (a *App) UseTransport(sel TransportSelection, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}
	if sel.Hostname != "" && sel.Name != "tailscale" {
		return cli.Fail(cli.CodeInvalidArgs, "--hostname applies only to tailscale")
	}
	if sel.Name != "manual" && (sel.BaseURL != "" || sel.KeepPrefix) {
		return cli.Fail(cli.CodeInvalidArgs, "--base-url and --keep-prefix apply only to manual")
	}
	// Everything gets validated before anything is changed.
	var manual *config.Manual
	switch sel.Name {
	case "tailscale":
		if sel.Hostname == "" {
			sel.Hostname = a.Config.TSNetHostname()
		}
		if err := tsnetnode.ValidateHostname(sel.Hostname); err != nil {
			return cli.Failf(cli.CodeInvalidArgs, "%v", err)
		}
	case "manual":
		if sel.BaseURL == "" {
			return cli.Fail(cli.CodeInvalidArgs, "manual transport needs --base-url")
		}
		if err := transport.ValidateBaseURL(sel.BaseURL); err != nil {
			return cli.Failf(cli.CodeInvalidArgs, "%v", err)
		}
		manual = &config.Manual{BaseURL: sel.BaseURL, KeepPrefix: sel.KeepPrefix}
	default:
		return cli.Failf(cli.CodeInvalidArgs, "unknown transport %q", sel.Name).
			WithHint("tailscale or manual")
	}

	// Build a Transport instance candidate for the requested transport
	next := a.Config
	next.Transport = sel.Name
	if manual != nil {
		next.Manual = manual
	}
	if sel.Name == "tailscale" {
		next.TSNet = &config.TSNet{Hostname: sel.Hostname}
	}
	candidate := a.transportFor(next)

	// The common sanity guard against letting a public transport through.
	// Public transports are not yet supported.
	if err := a.guard(candidate); err != nil {
		return err
	}

	// The server strips whatever prefix the transport forwards, and reads that
	// once at startup, so if this change moves it, the server is restarted
	// below rather than left serving the old contract.
	previousPrefix := a.IncomingPrefix()
	nodeChanged := (a.Config.Transport == "tailscale" || sel.Name == "tailscale") &&
		(a.Config.Transport != sel.Name || a.Config.TSNetHostname() != next.TSNetHostname())

	// Persist what was on disk plus this change, so an environment override for
	// this one invocation does not become permanent.
	onDisk, err := config.LoadFile(a.Root)
	if err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	onDisk.Transport = next.Transport
	onDisk.Manual = next.Manual
	onDisk.TSNet = next.TSNet
	if err := config.Save(a.Root, onDisk); err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	a.Config = next
	a.setTransport(candidate)
	if (a.IncomingPrefix() != previousPrefix || nodeChanged) && a.ServerRunning() {
		if err := a.StopServer(); err != nil {
			return err
		}
		if a.AutostartEnabled() {
			if err := a.StartServer(); err != nil {
				return err
			}
		} else {
			// A foreground `otata serve` was stopped so it cannot keep serving the old contract.
			// Only its own terminal can restart it, and a stopped server is a safe state.
			progress("the transport or incoming prefix changed; run 'otata serve' again to serve it")
		}
	}
	if sel.Name == "tailscale" {
		// The server prepares HTTPS and rewrites pages when it joins.
		if !candidate.Status(a.Config.Port).Ready {
			progress("start the server with 'otata autostart on' or 'otata serve', then run 'otata transport login' and 'otata status'")
		}
		return nil
	}
	// Manifests embed the base URL, so switching transports invalidates every published app until something regenerates them.
	if tr, err := a.Transport(); err == nil {
		if baseURL, err := tr.Ensure(a.Config.Port); err == nil {
			if err := a.Reindex(baseURL); err != nil {
				return cli.Failf(cli.CodeInternal, "transport saved but pages could not be regenerated: %v", err)
			}
		} else {
			return cli.Failf(cli.CodeTransportDown, "transport saved but could not be prepared: %v", err)
		}
	}
	return nil
}
