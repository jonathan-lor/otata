// Package app implements the commands. It is the one package that ties
// storage, transport, builder and render together.
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
	"github.com/jonathan-lor/otata/internal/tsnetnode"
)

type App struct {
	Root   string
	Config config.Config
	Store  *storage.Store

	// transport caches the transport Config selects.
	transport    transport.Transport
	transportSet bool

	// sup keeps the background server alive: launchd on macOS, systemd on Linux.
	sup supervisor
	// bindWait, when set, overrides every wait for a unit to bind the port.
	bindWait time.Duration
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

// selectTransport returns the configured transport without Transport's
// visibility guard, for callers that only need its metadata.
func (a *App) selectTransport() transport.Transport {
	if !a.transportSet {
		a.setTransport(a.transportFor(a.Config))
	}
	return a.transport
}

func (a *App) setTransport(t transport.Transport) {
	a.transport, a.transportSet = t, true
}

// transportFor builds the transport cfg selects, or nil. It takes a config
// rather than reading a.Config so a selection can be checked before it is saved.
func (a *App) transportFor(cfg config.Config) transport.Transport {
	switch cfg.Transport {
	case "tailscale":
		return transport.NewTSNet(a.tsnetDir(), a.tsnetIdentity(cfg))
	case "manual":
		if cfg.Manual == nil || cfg.Manual.BaseURL == "" {
			return nil
		}
		return transport.NewManual(cfg.Manual.BaseURL, cfg.Manual.KeepPrefix)
	}
	return nil
}

func (a *App) tsnetDir() string { return storage.TSNetDir(a.Root) }
func (a *App) tsnetIdentity(cfg config.Config) tsnetnode.Identity {
	return tsnetnode.Identity{Root: a.RootDigest(), Port: cfg.Port, Hostname: cfg.TSNetHostname()}
}

// Transport returns the selected transport, refusing a public one.
func (a *App) Transport() (transport.Transport, error) {
	t := a.selectTransport()
	if t == nil {
		switch a.Config.Transport {
		case "manual":
			return nil, cli.Fail(cli.CodeNoTransport, "manual transport selected but no base URL configured").
				WithHint("otata transport use manual --base-url https://example.com/otata")
		default:
			// Only a hand-edited config gets here.
			return nil, cli.Failf(cli.CodeNoTransport, "config selects unknown transport %q", a.Config.Transport).
				WithHint("run 'otata transport use tailscale' or 'otata transport use manual --base-url <url>'")
		}
	}
	return t, a.guard(t)
}

// guard refuses public transports, since none has an access guard yet.
func (a *App) guard(t transport.Transport) error {
	if t.Visibility() != transport.Public {
		return nil
	}
	return cli.Failf(cli.CodeTransportDown,
		"%s is a public transport and no access guard is implemented yet", t.Name()).
		WithHint("use Tailscale or a private HTTPS proxy")
}

// RootDigest identifies this store by its resolved path, hashed so no path
// leaks into the server's response headers. ServerRunning requires a match.
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
	Status  int    // HTTP status for the probed path
}

func (p serverProbe) describe() string {
	return fmt.Sprintf("an otata server for a different OTATA_ROOT (pid %d)", p.PID)
}

// probeServer asks the listener on the port to identify itself. ok means it
// is an otata server, whichever root it serves. Every response carries the
// identity headers, even a 404, so callers that only want identity probe "/".
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

// ServerRunning reports whether this root's server holds the port.
func (a *App) ServerRunning() bool {
	p, ok := a.probeServer("/")
	return ok && p.Root == a.RootDigest()
}

// serverPID returns the pid of whichever otata server holds the port, for any root.
func (a *App) serverPID() (int, bool) {
	p, ok := a.probeServer("/")
	return p.PID, ok
}

// otherRootServer reports an otata server on the port that serves another root.
func (a *App) otherRootServer() (serverProbe, bool) {
	p, ok := a.probeServer("/")
	if !ok || p.Root == a.RootDigest() {
		return serverProbe{}, false
	}
	return p, true
}

// PortBusy reports whether anything at all is listening on the port.
func (a *App) PortBusy() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", a.Config.Port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(name string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

// DefaultSlug is the directory name, with the platform appended for anything
// but iOS so one project's builds sit side by side (myapp, myapp-android).
func DefaultSlug(dir string, platform artifact.Platform) string {
	slug := Slugify(filepath.Base(dir))
	if platform != artifact.IOS {
		slug = Slugify(slug + "-" + string(platform))
	}
	return slug
}

// CheckSlug refuses to let one publish overwrite another's app: a different
// project's, or the same project's build for another platform.
func (a *App) CheckSlug(slug, projectPath string, platform artifact.Platform) error {
	rec, ok, err := a.Store.Record(slug)
	if err != nil {
		// Fail closed: an unreadable record could belong to another project.
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
	if rec.Platform != "" && rec.Platform != platform {
		return cli.Failf(cli.CodeSlugConflict,
			"slug %q holds this project's %s build; this is an %s build", slug, rec.Platform, platform).
			WithHint(fmt.Sprintf("omit --slug to publish it as %q, or pass another name", DefaultSlug(projectPath, platform)))
	}
	return nil
}

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

// Manifest is the plist iOS fetches to install r. Every value comes from the
// payload's Info.plist, so all of them are XML-escaped.
func Manifest(r artifact.Record, baseURL string) []byte {
	appBase := fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), r.Slug)
	var assets strings.Builder
	fmt.Fprintf(&assets, `
				<dict>
					<key>kind</key><string>software-package</string>
					<key>url</key><string>%s/%s?v=%s</string>
				</dict>`, xmlText(appBase), xmlText(r.PayloadName), xmlText(r.CacheKey()))
	if icon := r.IconFile(); icon != "" {
		for _, kind := range []string{"display-image", "full-size-image"} {
			fmt.Fprintf(&assets, `
				<dict>
					<key>kind</key><string>%s</string>
					<key>url</key><string>%s/%s</string>
				</dict>`, kind, xmlText(appBase), xmlText(icon))
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
