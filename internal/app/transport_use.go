package app

import (
	"fmt"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/transport"
)

// TransportSelection is what `otata transport use` was asked for. The manual
// fields apply to the manual transport only.
type TransportSelection struct {
	Name       string
	BaseURL    string
	KeepPrefix bool
	Visibility string
}

// UseTransport selects the transport every later command will serve through,
// persists the choice, and brings the running state in line with it: the
// transport being replaced is torn down, a server reading a prefix that has
// changed is restarted, and every page and manifest is regenerated against the
// new base URL. progress receives the warnings a caller should see but that
// do not fail the command.
func (a *App) UseTransport(sel TransportSelection, progress func(string)) error {
	// Everything gets validated before anything is changed: the arguments
	// first, as usage errors.
	var manual *config.Manual
	switch sel.Name {
	case "tailscale":
	case "manual":
		if sel.BaseURL == "" {
			return cli.Fail(cli.CodeInvalidArgs, "manual transport needs --base-url")
		}
		if err := transport.ValidateBaseURL(sel.BaseURL); err != nil {
			return cli.Failf(cli.CodeInvalidArgs, "%v", err)
		}
		vis, err := transport.ParseVisibility(sel.Visibility)
		if err != nil {
			return cli.Failf(cli.CodeInvalidArgs, "%v", err)
		}
		manual = &config.Manual{BaseURL: sel.BaseURL, KeepPrefix: sel.KeepPrefix, Visibility: string(vis)}
	default:
		return cli.Failf(cli.CodeInvalidArgs, "unknown transport %q", sel.Name).
			WithHint("tailscale or manual")
	}

	// The candidate, built by the same rule every later command will use, and
	// kept once selected so what it is asked here it need not be asked again.
	next := a.Config
	next.Transport = sel.Name
	if manual != nil {
		next.Manual = manual
	}
	candidate := transportFor(next)

	// Selection is where the transport proves it can serve, changing nothing.
	// Failing here names the actual obstacle (tailscale logged out, HTTPS
	// certificates disabled), whereas failing at the first publish would
	// surface whatever `tailscale serve` prints. Unwired is not an obstacle:
	// Ensure below is what wires it.
	if st := candidate.Status(a.Config.Port); !st.Ready && !st.Repairable {
		return cli.Fail(cli.CodeTransportDown, st.Detail)
	}
	// The guard every later command applies, applied here first. Without this
	// a funnelled tailnet or a route declared public was torn down to, saved,
	// and reported as selected, and then refused by every command after.
	if err := a.guard(candidate); err != nil {
		return err
	}

	// The server strips whatever prefix the transport forwards, and reads that
	// once at startup, so if this change moves it, the server is restarted
	// below rather than left serving the old contract.
	previousPrefix := a.IncomingPrefix()

	// Tear down the transport being replaced, or its route stays wired to our
	// port after we stop using it. Teardown is scoped to what otata added.
	if previous := a.selectTransport(); previous != nil && previous.Name() != sel.Name {
		if err := previous.Teardown(); err != nil {
			progress(fmt.Sprintf("warning: could not tear down %s: %v", previous.Name(), err))
		}
	}
	a.Config = next
	a.setTransport(candidate)

	// Persist what was on disk plus this change, so an environment override for
	// this one invocation does not become permanent.
	onDisk, err := config.LoadFile(a.Root)
	if err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	onDisk.Transport = a.Config.Transport
	onDisk.Manual = a.Config.Manual
	if err := config.Save(a.Root, onDisk); err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	if a.IncomingPrefix() != previousPrefix && a.ServerRunning() {
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
			progress("the incoming prefix changed; run 'otata serve' again to serve it")
		}
	}
	// Manifests embed the base URL, so switching transports invalidates every published app until something regenerates them.
	if tr, err := a.Transport(); err == nil {
		if baseURL, err := tr.Ensure(a.Config.Port); err == nil {
			if err := a.Reindex(baseURL); err != nil {
				return cli.Failf(cli.CodeInternal, "transport saved but pages could not be regenerated: %v", err)
			}
		}
	}
	return nil
}
