package transport

import (
	"strings"
	"testing"
)

func TestManualStatus(t *testing.T) {
	for _, tc := range []struct {
		url, detail string
	}{
		{"http://box.local/otata", "https"},
		{"", "no base URL"},
	} {
		s := NewManual(tc.url, false).Status(0)
		if s.Ready || !strings.Contains(s.Detail, tc.detail) {
			t.Fatalf("invalid manual URL: %+v", s)
		}
	}
	m := NewManual("https://box.local/otata", false)
	if s := m.Status(0); !s.Ready || s.Visibility != Private || m.Visibility() != Private {
		t.Fatalf("manual transport: %+v", s)
	}
}

func TestValidateBaseURL(t *testing.T) {
	for _, good := range []string{"https://builds.example.com", "https://builds.example.com/otata", "https://box.local:8443/a/b/"} {
		if err := ValidateBaseURL(good); err != nil {
			t.Errorf("%s rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"http://builds.example.com/otata", "https://", "https://x/otata?x=1", "https://x/otata#frag", "https://user:pw@x/otata", "builds.example.com/otata", "://bad"} {
		if err := ValidateBaseURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestManualIncomingPrefix(t *testing.T) {
	for _, tc := range []struct {
		base string
		keep bool
		want string
	}{
		{"https://x/otata", false, ""},
		{"https://x/otata", true, "/otata"},
		{"https://x/otata/", true, "/otata"},
		{"https://x/a/b", true, "/a/b"},
		{"https://x", true, ""},
		{"https://x/", true, ""},
	} {
		if got := NewManual(tc.base, tc.keep).IncomingPrefix(); got != tc.want {
			t.Errorf("NewManual(%q, %v).IncomingPrefix() = %q, want %q", tc.base, tc.keep, got, tc.want)
		}
	}
}
