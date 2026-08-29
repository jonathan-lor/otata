// Package transport makes the loopback server reachable from a phone.
//
// iOS requires a URL served over HTTPS with a publicly trusted certificate.
// Tailscale is one way to obtain that, not the only one, and nothing outside
// this package knows which is in use, except the guard keyed on Visibility.
package transport

import (
	"fmt"
	"net/url"
	"strings"
)

type Visibility string

const (
	// Private: reachable only by devices on a network you control.
	Private Visibility = "private"
	// Public: reachable by anyone holding the URL.
	Public Visibility = "public"
)

type Status struct {
	Name       string     `json:"name"`
	Ready      bool       `json:"ready"`
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

// ParseVisibility accepts only the closed set. Visibility is the input to the
// guard that keeps unreleased builds off the public internet, so a typo must
// be refused rather than quietly read as private.
func ParseVisibility(s string) (Visibility, error) {
	switch Visibility(strings.ToLower(s)) {
	case Private:
		return Private, nil
	case Public:
		return Public, nil
	}
	return "", fmt.Errorf("visibility must be %q or %q, not %q", Private, Public, s)
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
