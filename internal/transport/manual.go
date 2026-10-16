package transport

import (
	"net/url"
	"strings"
)

// Manual is a proxy or tunnel the user runs, which otata only points at.
type Manual struct {
	baseURL    string
	keepPrefix bool
}

// NewManual describes a proxy at baseURL. keepPrefix means it forwards the
// URL's path unchanged rather than stripping it.
func NewManual(baseURL string, keepPrefix bool) *Manual {
	return &Manual{baseURL: strings.TrimSuffix(baseURL, "/"), keepPrefix: keepPrefix}
}

func (m *Manual) Name() string { return "manual" }

// Visibility is private: the user vouches for the route, which otata can't check.
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
	// Validate again, since config.json can be edited by hand.
	if err := ValidateBaseURL(m.baseURL); err != nil {
		return "", &ErrUnavailable{Name: m.Name(), Reason: err.Error()}
	}
	return m.baseURL, nil
}

func (m *Manual) Status(int) Status {
	s := Status{Name: m.Name(), BaseURL: m.baseURL, Visibility: Private}
	// Ensure changes nothing here, so it doubles as the readiness check.
	if _, err := m.Ensure(0); err != nil {
		s.Detail = err.Error()
		return s
	}
	s.Ready = true
	s.Detail = "reachability is whatever your proxy provides; not verified here"
	return s
}

func normalizePath(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
