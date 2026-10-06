package app

import (
	"io"
	"strings"

	"github.com/jonathan-lor/otata/internal/cli"
)

// TransportLoginResult keeps login's existing JSON status payload while giving
// people the enrollment outcome and next step instead of the full status report.
type TransportLoginResult struct {
	*StatusResult
}

func (r TransportLoginResult) Human(w io.Writer) {
	cli.Section(w, "Tailscale login")
	s := r.Transport
	if s.Ready {
		cli.Line(w, "Logged in and ready.")
		if s.BaseURL != "" {
			cli.Line(w, "")
			cli.Line(w, "%s/", strings.TrimSuffix(s.BaseURL, "/"))
		}
		if s.Detail != "" {
			cli.Line(w, "%s", s.Detail)
		}
		return
	}

	next := "Run 'otata status' to check readiness."
	switch {
	case s.State == "needs_approval":
		cli.Line(w, "Waiting for device approval in the Tailscale admin console.")
		next = "Approve this device at https://login.tailscale.com/admin/machines, then run 'otata status'."
	case s.State == "dns_disabled":
		cli.Line(w, "Logged in. Enable MagicDNS in your tailnet's DNS settings to continue.")
		next = "Open https://login.tailscale.com/admin/dns, then run 'otata status'."
	case s.State == "https_disabled":
		cli.Line(w, "Logged in. Enable HTTPS certificates in your tailnet's DNS settings to continue.")
		next = "Open https://login.tailscale.com/admin/dns, then run 'otata status'."
	case s.State == "certificate_pending":
		cli.Line(w, "Logged in and preparing HTTPS. This can take a minute.")
	case s.AuthURL != "":
		cli.Line(w, "Open this URL to enroll otata in your tailnet:")
		cli.Line(w, "")
		cli.Line(w, "%s", s.AuthURL)
		next = "After completing login, run 'otata status' to check readiness."
	case s.State == "needs_login":
		cli.Line(w, "Login is required, but the enrollment URL is not available yet.")
		next = "Run 'otata transport login' again in a moment."
	case s.State == "connected":
		cli.Line(w, "Logged in. Preparing HTTPS.")
	default:
		switch s.State {
		case "starting", "connecting":
			cli.Line(w, "Connecting to Tailscale. Readiness has not been confirmed yet.")
		case "error":
			cli.Line(w, "Otata could not finish preparing Tailscale.")
		case "stopped":
			cli.Line(w, "The embedded Tailscale node is unavailable.")
		default:
			cli.Line(w, "Tailscale is not ready.")
		}
		if s.Detail != "" {
			cli.Line(w, "%s", s.Detail)
		}
	}
	cli.Line(w, "")
	cli.Line(w, "%s", next)
}
