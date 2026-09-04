package transport

import (
	"net/url"
	"strings"
)

// Manual is for users who already terminate HTTPS themselves. E.g. a
// reverse proxy on a real domain, an existing tunnel. The tool changes nothing
// about the network, and is only told where the port already appears.
type Manual struct {
	baseURL    string
	keepPrefix bool
}

/*
NewManual describes a proxy that serves baseURL. keepPrefix says the proxy
forwards the base URL's path unchanged rather than stripping it, such as nginx with
a bare proxy_pass, Caddy's handle rather than handle_path, a Cloudflare
tunnel, etc. Stripping is the default, as Tailscale does.

The prefix is derived from the base URL, not declared separately.
*/
func NewManual(baseURL string, keepPrefix bool) *Manual {
	return &Manual{baseURL: strings.TrimSuffix(baseURL, "/"), keepPrefix: keepPrefix}
}

func (m *Manual) Name() string { return "manual" }

// Visibility is private by definition. otata verifies nothing about where a
// proxy is reachable from, and with no access guard shipping, this transport
// is for routes the user considers private; which routes those are is the
// documentation's subject. A declared visibility used to exist and could only
// ever be "private", which is not a declaration.
func (m *Manual) Visibility() Visibility { return Private }

// IncomingPrefix is the base URL's path when the proxy forwards it, else nothing.
func (m *Manual) IncomingPrefix() string {
	if !m.keepPrefix {
		return ""
	}
	u, err := url.Parse(m.baseURL)
	if err != nil {
		return ""
	}
	return normalizePath(u.Path)
}

func (m *Manual) Ensure(int) (string, error) {
	if m.baseURL == "" {
		return "", &ErrUnavailable{Name: m.Name(), Reason: "no base URL configured"}
	}
	// Validated again here, not only when set. The config file is editable.
	if err := ValidateBaseURL(m.baseURL); err != nil {
		return "", &ErrUnavailable{Name: m.Name(), Reason: err.Error()}
	}
	return m.baseURL, nil
}

func (m *Manual) Status(int) Status {
	s := Status{Name: m.Name(), BaseURL: m.baseURL, Visibility: Private}
	// Ensure changes nothing for this transport, so it is the readiness
	// question itself: a base URL it would refuse is the obstacle, and never
	// repairable, because the config is what has to change.
	if _, err := m.Ensure(0); err != nil {
		s.Detail = err.Error()
		return s
	}
	s.Ready = true
	s.Detail = "reachability is whatever your proxy provides; not verified here"
	return s
}

// Teardown is a no-op. otata did not create the route and must not remove
// something the developer configured themselves.
func (m *Manual) Teardown() error { return nil }
