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

	// Ensure waits until the transport is ready and returns the base URL for
	// the phone. It must be idempotent.
	Ensure(port int) (string, error)

	// Status reports without mutating.
	Status(port int) Status

	// IncomingPrefix is the path prefix requests carry when they reach the
	// server, which strips it.
	IncomingPrefix() string
}

// ValidateBaseURL refuses a manual base URL that isn't plain https, since
// iOS needs HTTPS and the URL is extended by concatenation.
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
