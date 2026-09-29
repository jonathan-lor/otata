// Package transport provides embedded Tailscale and external HTTPS adapters.
package transport

import (
	"fmt"
	"net/url"
)

// Visibility describes who can reach a transport's URL.
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
	State      string     `json:"state,omitempty"`
	AuthURL    string     `json:"auth_url,omitempty"`
}

type Transport interface {
	Name() string
	Visibility() Visibility

	// Ensure waits for transport readiness and returns the base URL a phone
	// should use. It must be idempotent; it cannot prove remote reachability.
	Ensure(port int) (string, error)

	// Status reports without mutating.
	Status(port int) Status

	// IncomingPrefix is the path prefix requests carry when they reach the
	// HTTP handler, through the embedded node or a loopback proxy. Empty means
	// bare paths. The server strips exactly this, so the two cannot differ.
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
