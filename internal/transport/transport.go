// Package transport makes the loopback server reachable from a phone.
//
// iOS requires a URL served over HTTPS with a publicly trusted certificate.
// Tailscale is one way to obtain that, not the only one, and nothing outside
// this package knows which is in use, except the guard keyed on Visibility.
package transport

import (
	"fmt"
	"net/url"
)

// Visibility is where a transport's URL can be reached from. It is derived
// where it can be (Tailscale reads it off Funnel) and private by definition
// where it cannot (a proxy otata knows nothing about); the guard that keeps
// unreleased builds off the public internet refuses Public.
type Visibility string

const (
	// Private: reachable only by devices on a network you control.
	Private Visibility = "private"
	// Public: reachable by anyone holding the URL.
	Public Visibility = "public"
)

type Status struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Repairable is set when Ready is false and Ensure would make it true:
	// the transport is usable and only its wiring is missing. Unset, the
	// obstacle is on the machine (logged out, certificates off, a bad base
	// URL) and Detail names it; nothing otata does will clear it.
	Repairable bool       `json:"repairable,omitempty"`
	BaseURL    string     `json:"base_url,omitempty"`
	Visibility Visibility `json:"visibility"`
	Detail     string     `json:"detail,omitempty"`
}

type Transport interface {
	Name() string
	Visibility() Visibility

	// Ensure makes the local port reachable and returns the base URL a phone
	// should use. It must be idempotent.
	Ensure(port int) (string, error)

	// Status reports without mutating.
	Status(port int) Status

	// Teardown removes only what otata added. Scoping matters: the command
	// Tailscale itself suggests for removing a proxy takes down every handler
	// on the port, including ones the user depends on.
	Teardown() error

	// IncomingPrefix is the path prefix requests still carry when they reach the
	// loopback server: what this transport forwards without stripping. Empty
	// means bare paths. The server strips exactly this, so the two cannot differ.
	IncomingPrefix() string
}

// ValidateBaseURL checks a manual base URL before it is persisted, so a bad
// one fails the command that sets it rather than every publish afterwards.
//
// iOS refuses an itms-services manifest not served over HTTPS with a publicly
// trusted certificate, and http gives a URL that works from curl on the Mac and
// fails on every phone. Query, fragment and userinfo are refused because the
// base URL is extended by concatenation, and "…/otata?x=1/app/manifest.plist"
// is not a manifest URL.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base URL %q: %w", raw, err)
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("base URL must be https; iOS refuses an itms-services manifest over http")
	case u.Host == "":
		return fmt.Errorf("base URL %q has no host", raw)
	case u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("base URL %q must be a plain https://host/path with no query, fragment or credentials", raw)
	}
	return nil
}

// ErrUnavailable is returned by Ensure when the transport cannot be used.
type ErrUnavailable struct {
	Name   string
	Reason string
}

func (e *ErrUnavailable) Error() string {
	return fmt.Sprintf("%s transport unavailable: %s", e.Name, e.Reason)
}
