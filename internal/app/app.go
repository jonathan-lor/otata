// Package app wires everything together. It's the only place that knows about storage, transport, builder and render at the same time.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/storage"
	"github.com/jonathan-lor/otata/internal/transport"
)

type App struct {
	Root   string
	Config config.Config
	Store  *storage.Store
}

func DefaultRoot() string {
	if v := os.Getenv("OTATA_ROOT"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".otata")
}

func Open() (*App, error) {
	root := DefaultRoot()
	cfg, err := config.Load(root)
	if err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not read config: %v", err)
	}
	store, err := storage.Open(root)
	if err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not open %s: %v", root, err)
	}
	return &App{Root: root, Config: cfg, Store: store}, nil
}

// ---------- transport ----------

/*
selectTransport picks the transport WITHOUT the visibility guard. Callers
needing only its metadata (the server asking which prefix is stripped) must
work even when publishing through it would be refused, and must use the same
rule Transport() does.

The selection is read from config and is always nil until 'otata transport use' is run.
*/
func (a *App) selectTransport() transport.Transport {
	switch a.Config.Transport {
	case "tailscale":
		return transport.NewTailscale(a.Config.ServePath)
	case "manual":
		if a.Config.Manual == nil || a.Config.Manual.BaseURL == "" {
			return nil
		}
		m := a.Config.Manual
		// The command that writes the file validates visibility, and covers a
		// file edited by hand. Anything unparseable fails closed, read as
		// public, which the guard refuses, rather than open as private.
		vis, err := transport.ParseVisibility(m.Visibility)
		if err != nil {
			vis = transport.Public
		}
		return transport.NewManual(m.BaseURL, m.KeepPrefix, vis)
	}
	return nil
}

// Transport resolves the selected transport and enforces the visibility guard.
func (a *App) Transport() (transport.Transport, error) {
	t := a.selectTransport()
	if t == nil {
		switch a.Config.Transport {
		case "":
			// The hint may probe tailscale, but only to inform the choice
			hint := "run 'otata transport use tailscale', or 'otata transport use manual --base-url <url>' for your own proxy"
			if transport.NewTailscale(a.Config.ServePath).Available() {
				hint = "tailscale is running on this machine: run 'otata transport use tailscale'"
			}
			return nil, cli.Fail(cli.CodeNoTransport, "no transport selected").WithHint(hint)
		case "manual":
			return nil, cli.Fail(cli.CodeNoTransport, "manual transport selected but no base URL configured").
				WithHint("otata transport use manual --base-url https://example.com/otata")
		default:
			// A hand-edited config; the closed set is the same one 'transport use' accepts.
			return nil, cli.Failf(cli.CodeNoTransport, "config selects unknown transport %q", a.Config.Transport).
				WithHint("run 'otata transport use tailscale' or 'otata transport use manual --base-url <url>'")
		}
	}
	return t, a.guard(t)
}

// guard refuses a public transport. None ships with an access guard, so choosing one is refused.
//
// The code is transport_down, not invalid_args: the command was called
// correctly and the transport exists, but the machine's network makes it
// unusable (Funnel on the listener, a proxy declared public). invalid_args
// exits 2, which the docs define as "fix the arguments", and there is no
// argument to fix.
func (a *App) guard(t transport.Transport) error {
	if t.Visibility() == transport.Public {
		return cli.Failf(cli.CodeTransportDown,
			"%s is a public transport and no access guard is implemented yet", t.Name()).
			WithHint("use a private transport, or declare visibility private if your proxy is not publicly reachable")
	}
	return nil
}

// ---------- server lifecycle ----------

// RootDigest identifies this store by location. The server sends it on every
// response and ServerRunning requires a match, so an otata server on the port
// is not enough and must serve the tree publish writes into. Normalized so a
// symlinked ~/.otata is one store and hashed so no local path rides in a header.
func (a *App) RootDigest() string { return rootDigest(a.Root) }

func rootDigest(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:8])
}

// serverProbe is what the listener on the port said about itself.
type serverProbe struct {
	PID     int
	Root    string // RootDigest of the store it serves
	Version string // the build it is running, "" if it does not say
	Status  int    // for the GET probeServer was asked to make
}

// describe names the server for an error or a repair.
func (p serverProbe) describe() string {
	return fmt.Sprintf("an otata server for a different OTATA_ROOT (pid %d)", p.PID)
}

/*
probeServer asks the listener to identify itself. ok means it is an otata
server at all, and whether it is the right one is the caller's question.

The identity headers ride on every response. 404s are included, so identity
callers pass "/" and stay cheap in their polling loops. Status is only
meaningful for a path the server is expected to serve, which under a
keep-prefix manual transport is never the bare root since
judging health by GET / declared every healthy keep-prefix server broken.
*/
func (a *App) probeServer(path string) (serverProbe, bool) {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", a.Config.Port, path))
	if err != nil {
		return serverProbe{}, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.Header.Get("X-Otata") == "" {
		return serverProbe{}, false // someone else's server
	}
	p := serverProbe{
		Root:    resp.Header.Get("X-Otata-Root"),
		Version: resp.Header.Get("X-Otata-Version"),
		Status:  resp.StatusCode,
	}
	p.PID, _ = strconv.Atoi(resp.Header.Get("X-Otata-Pid")) // 0 if unknown
	return p, true
}

// ServerRunning reports whether the server for THIS root is on the port, not
// merely that something is listening, and not merely that otata is.
func (a *App) ServerRunning() bool {
	p, ok := a.probeServer("/")
	return ok && p.Root == a.RootDigest()
}

// serverPID returns the pid of whichever otata server holds the port,
// regardless of root. Stopping cares about ownership, not about which store.
func (a *App) serverPID() (int, bool) {
	p, ok := a.probeServer("/")
	return p.PID, ok
}

// otherRootServer reports an otata server on the port that serves a different store.
// This could be the state a stray OTATA_ROOT leaves, or a test run beside the real thing.
func (a *App) otherRootServer() (serverProbe, bool) {
	p, ok := a.probeServer("/")
	if !ok || p.Root == a.RootDigest() {
		return serverProbe{}, false
	}
	return p, true
}

// PortBusy reports whether anything at all holds the port, so a foreign
// listener gets named rather than silently trusted or killed.
func (a *App) PortBusy() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", a.Config.Port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ---------- project identity ----------

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(name string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

// CheckSlug refuses to let one project overwrite another's published app.
func (a *App) CheckSlug(slug, projectPath string) error {
	rec, ok, err := a.Store.Record(slug)
	if err != nil {
		// Failing open here lets a second project silently overwrite the first's
		// published app, which is exactly what ProjectPath exists to prevent.
		return cli.Failf(cli.CodeSlugConflict,
			"a record already exists for %q but cannot be read: %v", slug, err).
			WithHint("run 'otata forget " + slug + "' to clear it, or pass --slug")
	}
	if !ok {
		return nil
	}
	if rec.ProjectPath != "" && rec.ProjectPath != projectPath {
		return cli.Failf(cli.CodeSlugConflict,
			"slug %q already belongs to %s", slug, rec.ProjectPath).
			WithHint("pass --slug to publish under a different name")
	}
	return nil
}

// GitInfo reads what makes one build distinguishable from another at a glance.
func GitInfo(dir string) (commit, branch string, dirty bool) {
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	commit = run("rev-parse", "--short", "HEAD")
	branch = run("rev-parse", "--abbrev-ref", "HEAD")
	dirty = run("status", "--porcelain") != ""
	return
}

// Manifest is the plist iOS fetches to learn what it is installing.
// Every interpolated value is XML-escaped and comes from the payload's Info.plist,
// which is untrusted for --artifact and arbitrary otherwise.
func Manifest(r artifact.Record, baseURL string) []byte {
	appBase := fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), r.Slug)
	var assets strings.Builder
	fmt.Fprintf(&assets, `
				<dict>
					<key>kind</key><string>software-package</string>
					<key>url</key><string>%s/%s?v=%s</string>
				</dict>`, xmlText(appBase), xmlText(r.PayloadName), xmlText(r.CacheKey()))
	if r.HasIcon {
		for _, kind := range []string{"display-image", "full-size-image"} {
			fmt.Fprintf(&assets, `
				<dict>
					<key>kind</key><string>%s</string>
					<key>url</key><string>%s/icon.png</string>
				</dict>`, kind, xmlText(appBase))
		}
	}
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
	<dict>
		<key>items</key>
		<array>
			<dict>
				<key>assets</key>
				<array>%s
				</array>
				<key>metadata</key>
				<dict>
					<key>bundle-identifier</key><string>%s</string>
					<key>bundle-version</key><string>%s</string>
					<key>kind</key><string>software</string>
					<key>title</key><string>%s</string>
				</dict>
			</dict>
		</array>
	</dict>
</plist>
`, assets.String(), xmlText(r.BundleID), xmlText(r.Version), xmlText(r.Title))
}
