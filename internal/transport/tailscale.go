package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Tailscale exposes the port over a tailnet, with a real Let's Encrypt
// certificate for a name that resolves only inside it.
//
// A value lives for one command invocation and is not safe for concurrent use.
type Tailscale struct {
	bin       string
	servePath string

	// The CLI reads below are memoized for the life of the value. One command
	// asks several questions of the same state (doctor shelled out four times
	// for two answers), and within a single invocation the only thing that
	// changes what `serve status` reports is this type's own Ensure and
	// Teardown, which drop the memo.
	serveCfg   serveConfig
	serveOK    bool
	serveRead  bool
	statusInf  statusInfo
	statusFail error
	statusRead bool
}

// tailscaleBinary resolves the CLI properly. On macOS `tailscale` is commonly
// only a shell alias to the binary inside the app bundle, so it is absent from
// any non-interactive process. PATH alone is not enough.
func tailscaleBinary() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	for _, candidate := range []string{
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/usr/local/bin/tailscale",
		"/opt/homebrew/bin/tailscale",
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func NewTailscale(servePath string) *Tailscale {
	return &Tailscale{bin: tailscaleBinary(), servePath: normalizePath(servePath)}
}

func normalizePath(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func (t *Tailscale) Name() string { return "tailscale" }

// IncomingPrefix is empty: `tailscale serve --set-path` strips the mount path
// before proxying, so the server sees bare paths whatever servePath is.
func (t *Tailscale) IncomingPrefix() string { return "" }

// Visibility is derived, not assumed. A tailnet is private, but Funnel exposes
// the same listener to the open internet, and a constant Private meant a
// Funnel-configured port was reported private while publishing unreleased builds publicly.
func (t *Tailscale) Visibility() Visibility {
	if t.funnelEnabled() {
		return Public
	}
	return Private
}

// servePort is the HTTPS port `tailscale serve` mounts handlers on. Funnel is
// granted per listener, so this is also the scope a Funnel check has to use.
const servePort = 443

func (t *Tailscale) Available() bool {
	if t.bin == "" {
		return false
	}
	return t.run("status") == nil
}

// run and output both time out. a wedged tailscaled must not hang the CLI
func (t *Tailscale) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, t.bin, args...).Run()
}

func (t *Tailscale) output(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, t.bin, args...).Output()
}

// serveConfig is the shape of `tailscale serve status --json`.
type serveConfig struct {
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
	AllowFunnel map[string]bool `json:"AllowFunnel"`
}

func (t *Tailscale) serveStatus() (serveConfig, bool) {
	if !t.serveRead {
		t.serveCfg, t.serveOK = t.fetchServeStatus()
		t.serveRead = true
	}
	return t.serveCfg, t.serveOK
}

func (t *Tailscale) fetchServeStatus() (serveConfig, bool) {
	var cfg serveConfig
	raw, err := t.output("serve", "status", "--json")
	if err != nil {
		return cfg, false
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return cfg, false
	}
	return cfg, true
}

// handlerPath is the key our handler occupies. Tailscale stores the root
// handler as "/" regardless of how it was set.
func (t *Tailscale) handlerPath() string {
	if t.servePath == "" {
		return "/"
	}
	return t.servePath
}

// wired reports whether a handler AT OUR PATH forwards to our port.
func (t *Tailscale) wired(port int) bool {
	cfg, ok := t.serveStatus()
	if !ok {
		return false
	}
	return wiredIn(cfg, t.handlerPath(), port)
}

// wiredIn is the decision, separated from fetching so it can be tested against
// real serve-status shapes without a running tailscaled.
func wiredIn(cfg serveConfig, handlerPath string, port int) bool {
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	for _, site := range cfg.Web {
		if h, present := site.Handlers[handlerPath]; present && h.Proxy == want {
			return true
		}
	}
	return false
}

// funnelEnabled reports whether the listener our handler mounts on is
// funnelled, whether or not the handler exists yet.
func (t *Tailscale) funnelEnabled() bool {
	cfg, ok := t.serveStatus()
	if !ok {
		return false
	}
	return funnelIn(cfg, servePort)
}

// funnelIn is the decision, and split from fetching so it can be tested.
//
// Funnel is granted per listener (AllowFunnel is keyed by host:port), so every
// handler on that port is public, including one mounted later. Asking whether a
// funnelled site already carried our handler is false on the first publish, so the
// guard saw Private, Ensure mounted the path on a funnelled :443, and the build
// was public until the next command looked. The right question is whether the
// listener we mount on is funnelled.
func funnelIn(cfg serveConfig, port int) bool {
	suffix := fmt.Sprintf(":%d", port)
	for hostPort, allowed := range cfg.AllowFunnel {
		if allowed && strings.HasSuffix(hostPort, suffix) {
			return true
		}
	}
	return false
}

// statusInfo is the slice of `tailscale status --json` this package reads.
type statusInfo struct {
	Self struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
	// CertDomains is non-empty exactly when HTTPS certificates are enabled on
	// the tailnet, which `tailscale serve` requires and which is off by
	// default on a new tailnet.
	CertDomains []string `json:"CertDomains"`
}

func (t *Tailscale) statusJSON() (statusInfo, error) {
	if !t.statusRead {
		t.statusInf, t.statusFail = t.fetchStatusJSON()
		t.statusRead = true
	}
	return t.statusInf, t.statusFail
}

func (t *Tailscale) fetchStatusJSON() (statusInfo, error) {
	var st statusInfo
	raw, err := t.output("status", "--json")
	if err != nil {
		return st, &ErrUnavailable{Name: t.Name(), Reason: "tailscale is not running or not logged in"}
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("could not parse tailscale status: %w", err)
	}
	return st, nil
}

// Verify reports what stops this transport from serving, changing nothing –
// a resolvable CLI, a running and logged-in node, a MagicDNS name, and HTTPS
// certificates, which serve requires and a new tailnet has off. It exists so
// `transport use tailscale` fails at the command with the actual obstacle,
// instead of at the first publish with whatever `tailscale serve` prints.
// Ensure and Status run it first too, so every path names an obstacle in the
// same words.
func (t *Tailscale) Verify() error {
	if t.bin == "" {
		return &ErrUnavailable{Name: t.Name(), Reason: "tailscale CLI not found; is Tailscale installed?"}
	}
	st, err := t.statusJSON()
	if err != nil {
		return err
	}
	if reason := verifyIn(st); reason != "" {
		return &ErrUnavailable{Name: t.Name(), Reason: reason}
	}
	return nil
}

// verifyIn is the decision, separated from fetching so it can be tested
// against real status shapes without a running tailscaled.
func verifyIn(st statusInfo) string {
	if strings.TrimSuffix(st.Self.DNSName, ".") == "" {
		return "could not resolve this machine's MagicDNS name; enable MagicDNS in the tailnet's DNS settings"
	}
	if len(st.CertDomains) == 0 {
		return "HTTPS certificates are not enabled on this tailnet, and tailscale serve needs them; enable them in the admin console under DNS, then re-run"
	}
	return ""
}

// hostname returns this node's MagicDNS name, without the trailing root dot.
// Callers Verify first, which is what refuses an unreadable status or an
// empty name, so the wording for each failure exists once, in Verify's path.
func (t *Tailscale) hostname() string {
	st, _ := t.statusJSON()
	return strings.TrimSuffix(st.Self.DNSName, ".")
}

func (t *Tailscale) baseURL(host string) string {
	return "https://" + host + t.servePath
}

func (t *Tailscale) Ensure(port int) (string, error) {
	if err := t.Verify(); err != nil {
		return "", err
	}
	if !t.wired(port) {
		target := fmt.Sprintf("http://127.0.0.1:%d", port)
		args := []string{"serve", "--bg", fmt.Sprintf("--https=%d", servePort)}
		if t.servePath != "" {
			args = append(args, "--set-path="+t.servePath)
		}
		args = append(args, target)
		if out, err := exec.Command(t.bin, args...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("tailscale serve failed: %s", strings.TrimSpace(string(out)))
		}
		// The wiring just changed what `serve status` reports; drop the memo
		// so a later question re-reads reality.
		t.serveRead = false
	}
	return t.baseURL(t.hostname()), nil
}

func (t *Tailscale) Status(port int) Status {
	s := Status{Name: t.Name(), Visibility: t.Visibility()}
	if err := t.Verify(); err != nil {
		s.Detail = err.Error()
		return s
	}
	s.BaseURL = t.baseURL(t.hostname())
	s.Ready = t.wired(port)
	if !s.Ready {
		// Verified but unwired is the one state Ensure mends.
		s.Detail = "serve path not wired"
		s.Repairable = true
	}
	return s
}

// Teardown removes only our path. Passing just --https=443 off, as Tailscale's
// own message suggests, would remove every handler on the port.
func (t *Tailscale) Teardown() error {
	if t.bin == "" || t.servePath == "" {
		return nil
	}
	err := t.run("serve", fmt.Sprintf("--https=%d", servePort), "--set-path="+t.servePath, "off")
	// Whatever the outcome, the serve config may have changed underneath the memo.
	t.serveRead = false
	return err
}
