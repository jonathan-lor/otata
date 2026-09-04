package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// Shapes taken from real `tailscale serve status --json` output.
const serveJSON = `{
  "TCP": {"443": {"HTTPS": true}},
  "Web": {
    "host.tailnet.ts.net:443": {
      "Handlers": {
        "/": {"Proxy": "http://127.0.0.1:8080"},
        "/otata": {"Proxy": "http://127.0.0.1:8787"}
      }
    }
  }
}`

const funnelJSON = `{
  "TCP": {"443": {"HTTPS": true}},
  "Web": {
    "host.tailnet.ts.net:443": {
      "Handlers": {"/otata": {"Proxy": "http://127.0.0.1:8787"}}
    }
  },
  "AllowFunnel": {"host.tailnet.ts.net:443": true}
}`

func parse(t *testing.T, raw string) serveConfig {
	t.Helper()
	var cfg serveConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// This was a substring search over the whole JSON blob, which said "already
// wired" for a handler on a different path or a port that was merely a prefix.
// Ensure would then skip wiring and hand back a URL nothing served.
func TestWiredRequiresOurPathAndExactPort(t *testing.T) {
	cfg := parse(t, serveJSON)

	if !wiredIn(cfg, "/otata", 8787) {
		t.Error("our own handler was not recognized")
	}
	// A handler exists for 8080, but at "/", not our path.
	if wiredIn(cfg, "/otata", 8080) {
		t.Error("matched a handler on a different path")
	}
	// Prefix collisions: :878 and :87 are substrings of :8787.
	for _, port := range []int{878, 87, 8, 80} {
		if wiredIn(cfg, "/otata", port) {
			t.Errorf("port %d matched by prefix against 8787", port)
		}
	}
	// Right port, wrong path.
	if wiredIn(cfg, "/elsewhere", 8787) {
		t.Error("matched a path we do not serve")
	}
}

// The state before a first publish on a machine that already funnels
// something else on :443; our handler does not exist yet.
const preWireFunnelJSON = `{
  "TCP": {"443": {"HTTPS": true}},
  "Web": {
    "host.tailnet.ts.net:443": {
      "Handlers": {"/blog": {"Proxy": "http://127.0.0.1:3000"}}
    }
  },
  "AllowFunnel": {"host.tailnet.ts.net:443": true}
}`

// Funnel on a different port is not our listener.
const otherPortFunnelJSON = `{
  "Web": {
    "host.tailnet.ts.net:8443": {
      "Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}
    }
  },
  "AllowFunnel": {"host.tailnet.ts.net:8443": true}
}`

// Visibility drove the guard that prevents publishing to the open internet, and
// it used to be a hardcoded Private, so a funnelled port reported "private"
// while serving unreleased builds publicly.
func TestFunnelIsDetected(t *testing.T) {
	if funnelIn(parse(t, serveJSON), servePort) {
		t.Error("plain serve reported as funnelled")
	}
	if !funnelIn(parse(t, funnelJSON), servePort) {
		t.Error("funnel NOT detected: builds would be exposed while reported private")
	}
}

// Funnel is granted per listener, so a handler mounted later on a funnelled
// :443 is public from the moment it exists. The check used to require our
// handler to be present, which is exactly false before the first Ensure. The
// guard passed, Ensure mounted the path, and the build was public until the
// next command looked again.
func TestFunnelIsDetectedBeforeOurHandlerExists(t *testing.T) {
	if !funnelIn(parse(t, preWireFunnelJSON), servePort) {
		t.Error("a funnelled :443 without our handler reported private: the first publish would go public")
	}
	if funnelIn(parse(t, otherPortFunnelJSON), servePort) {
		t.Error("a funnel on another port was attributed to our listener")
	}
}

// Shapes taken from real `tailscale status --json` output. CertDomains is
// non-empty exactly when HTTPS certificates are enabled on the tailnet, which
// `tailscale serve` requires and a new tailnet has off, so selection must
// refuse without it. The alternative is a publish that fails with whatever
// `tailscale serve` prints.
func TestVerifyGatesOnMagicDNSAndCertificates(t *testing.T) {
	parseStatus := func(raw string) statusInfo {
		var st statusInfo
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	ready := parseStatus(`{"Self": {"DNSName": "host.tailnet.ts.net."}, "CertDomains": ["host.tailnet.ts.net"]}`)
	if reason := verifyIn(ready); reason != "" {
		t.Errorf("a ready node was refused: %s", reason)
	}
	noCerts := parseStatus(`{"Self": {"DNSName": "host.tailnet.ts.net."}}`)
	if reason := verifyIn(noCerts); !strings.Contains(reason, "HTTPS certificates") {
		t.Errorf("HTTPS certificates disabled, but the reason says: %q", reason)
	}
	noDNS := parseStatus(`{"Self": {"DNSName": ""}, "CertDomains": []}`)
	if reason := verifyIn(noDNS); !strings.Contains(reason, "MagicDNS") {
		t.Errorf("no MagicDNS name, but the reason says: %q", reason)
	}
}

func TestManualRequiresHTTPS(t *testing.T) {
	if _, err := NewManual("http://box.local/otata", false).Ensure(0); err == nil {
		t.Error("accepted a plain-http base URL; iOS cannot install from one")
	}
	if _, err := NewManual("https://box.local/otata", false).Ensure(0); err != nil {
		t.Errorf("rejected a valid https base URL: %v", err)
	}
	if _, err := NewManual("", false).Ensure(0); err == nil {
		t.Error("accepted an empty base URL")
	}
}

// The config file is editable, so a base URL the command would have refused
// can still be what is on disk. Status must then name it as the obstacle, and
// never as repairable: nothing but the config can change it.
func TestManualStatusNamesABadBaseURL(t *testing.T) {
	bad := NewManual("http://box.local/otata", false).Status(0)
	if bad.Ready || bad.Repairable {
		t.Errorf("http base URL: ready=%v repairable=%v, want neither", bad.Ready, bad.Repairable)
	}
	if !strings.Contains(bad.Detail, "https") {
		t.Errorf("the obstacle is not named: %q", bad.Detail)
	}
	good := NewManual("https://box.local/otata", false).Status(0)
	if !good.Ready || good.Detail == "" {
		t.Errorf("valid base URL: ready=%v detail=%q", good.Ready, good.Detail)
	}
}

func TestValidateBaseURL(t *testing.T) {
	for _, ok := range []string{"https://builds.example.com", "https://builds.example.com/otata", "https://box.local:8443/a/b/"} {
		if err := ValidateBaseURL(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://builds.example.com/otata", // iOS refuses http
		"https://",                        // no host
		"https://x/otata?x=1",             // path concatenation would produce ?x=1/app/...
		"https://x/otata#frag",
		"https://user:pw@x/otata",
		"builds.example.com/otata", // no scheme
		"://bad",
	} {
		if err := ValidateBaseURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// The manual transport is private by definition: otata verifies nothing
// about it, so nothing it could learn would make it public.
func TestManualIsPrivateByDefinition(t *testing.T) {
	m := NewManual("https://box.local/otata", false)
	if m.Visibility() != Private || m.Status(0).Visibility != Private {
		t.Error("the manual transport reported a visibility other than private")
	}
}

// The prefix the server must strip is derived from the base URL, and only
// when the proxy is declared to forward it. A proxy that strips (the common
// case, and what Tailscale does) leaves nothing to strip.
func TestManualIncomingPrefix(t *testing.T) {
	cases := []struct {
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
	}
	for _, c := range cases {
		if got := NewManual(c.base, c.keep).IncomingPrefix(); got != c.want {
			t.Errorf("NewManual(%q, keep=%v).IncomingPrefix() = %q, want %q", c.base, c.keep, got, c.want)
		}
	}
	if got := NewTailscale("/otata").IncomingPrefix(); got != "" {
		t.Errorf("tailscale strips its mount path itself; IncomingPrefix = %q, want empty", got)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{"otata": "/otata", "/otata": "/otata", "/otata/": "/otata", "": "", "/": ""}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}
