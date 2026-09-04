package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/builder"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/render"
	"github.com/jonathan-lor/otata/internal/server"
	"github.com/jonathan-lor/otata/internal/storage"
	"github.com/jonathan-lor/otata/internal/transport"
	"github.com/jonathan-lor/otata/internal/version"
)

// IncomingPrefix is the path prefix requests carry when they reach the server.
// It asks the transport rather than re-deriving it, so the two cannot
// disagree. A mismatch 404s every app while the index still works. With no
// transport nothing forwards to us, so nothing is stripped.
func (a *App) IncomingPrefix() string {
	if tr := a.selectTransport(); tr != nil {
		return tr.IncomingPrefix()
	}
	return ""
}

func hostOf(baseURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return s
}

// Reindex regenerates the install surface from what is on disk. Records and
// build markers are the only source of truth, so this can always be re-run.
func (a *App) Reindex(baseURL string) error {
	records, err := a.Store.Records()
	if err != nil {
		return err
	}
	building, err := a.Store.Building()
	if err != nil {
		return err
	}
	page, err := render.Index(hostOf(baseURL), baseURL, records, building)
	if err != nil {
		return err
	}
	if err := a.Store.WriteFile(filepath.Join(a.Store.Public(), "index.html"), page); err != nil {
		return err
	}
	// Per-app pages restate the build marker, so a bookmarked link is as honest
	// as the index. Manifests are regenerated here too. They embed the base URL,
	// so a new transport or serve path would otherwise leave every app pointing
	// at a URL that no longer resolves.
	for _, r := range records {
		if err := a.Store.WriteFile(filepath.Join(a.Store.AppDir(r.Slug), "manifest.plist"),
			Manifest(r, baseURL)); err != nil {
			return err
		}
		var b *artifact.Building
		if m, ok := building[r.Slug]; ok {
			b = &m
		}
		appPage, err := render.App(r, baseURL, b)
		if err != nil {
			return err
		}
		if err := a.Store.WriteFile(filepath.Join(a.Store.AppDir(r.Slug), "index.html"), appPage); err != nil {
			return err
		}
	}
	return nil
}

// ---------- publish ----------

type PublishOptions struct {
	Dir      string
	Config   string
	Scheme   string
	Slug     string
	Artifact string
	Builder  string // "" or "build" for the incremental build path, "archive" for the archive+export path
}

type PublishResult struct {
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Version     string  `json:"version"`
	Build       string  `json:"build"`
	BuildConfig string  `json:"config"`
	SizeMB      float64 `json:"size_mb"`
	Commit      string  `json:"commit,omitempty"`
	Dirty       bool    `json:"dirty"`
	InstallURL  string  `json:"install_url"`
	IndexURL    string  `json:"index_url"`
	Transport   string  `json:"transport"`

	// Signing is when this build stops being installable, for a caller that wants the dates.
	Signing *appmeta.Signing `json:"signing,omitempty"`
	// SigningWarning is set only when that deadline is close enough to act on,
	// so an regular publish says nothing about signing.
	SigningWarning string `json:"signing_warning,omitempty"`
}

func (r PublishResult) Human(w io.Writer) {
	cli.Section(w, "Published")
	cli.Line(w, "%s %s (%s), %s, %s", r.Title, r.Version, r.Build, render.Size(r.SizeMB), r.BuildConfig)
	if r.SigningWarning != "" {
		cli.Section(w, "Signing")
		cli.Line(w, "\033[1;33m%s\033[0m", r.SigningWarning)
	}
	cli.Section(w, "Ready")
	cli.Line(w, "This app:  \033[1;32m%s\033[0m", r.InstallURL)
	cli.Line(w, "All apps:  \033[1;32m%s\033[0m\n", r.IndexURL)
}

/*
onInterrupt turns a signal into the cancellation of the build.

Go runs no deferred functions on a fatal signal, so without this a Ctrl-C
during a multi-minute archive strands a marker, and the app renders as
BUILDING with no install link until doctor --fix or the next publish
notices its process is gone.

The first signal cancels ctx, which kills the build's process group, and
Publish unwinds through its ordinary failure path: the marker is cleared,
the pages regenerated, and the caller gets an interrupted failure carrying
the shell's exit status for that signal. It used to exit on the spot, which
cleared the marker but left xcodebuild running into the work directory the
next publish would claim. A second signal is the caller insisting: the
marker is cleared and the process exits at once.

A signal that lands after the build, during the seconds it takes to stage
the payload, lets the publish complete rather than stop it half-staged.
*/
func (a *App) onInterrupt(slug, baseURL string) (ctx context.Context, interrupted func() os.Signal, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	// SIGHUP is what a publish driven over SSH receives when the connection
	// drops. An inherited ignore (nohup) is the caller saying
	// "survive the hangup", and Notify would override it, so it is honored.
	// Otherwise this handler would kill the build nohup exists to protect.
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	signal.Notify(ch, signals...)
	done := make(chan struct{})
	var mu sync.Mutex
	var got os.Signal
	go func() {
		select {
		case s := <-ch:
			mu.Lock()
			got = s
			mu.Unlock()
			cancel()
		case <-done:
			return
		}
		select {
		case s := <-ch:
			_ = a.Store.ClearBuilding(slug)
			_ = a.Reindex(baseURL)
			os.Exit(signalExit(s))
		case <-done:
		}
	}()
	interrupted = func() os.Signal {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	stop = func() {
		signal.Stop(ch)
		close(done)
		cancel()
	}
	return ctx, interrupted, stop
}

// signalExit is the status a shell reports for a process a signal killed:
// 128 plus the signal's number, so 130 for Ctrl-C and 143 for SIGTERM.
func signalExit(s os.Signal) int {
	if n, ok := s.(syscall.Signal); ok {
		return 128 + int(n)
	}
	return 130
}

// signalName is the conventional spelling; syscall.Signal's own String is
// "interrupt" and "terminated", which read as prose rather than as a name.
func signalName(s os.Signal) string {
	switch s {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return s.String()
}

// claimBuild takes the build marker for a slug, or refuses because a live
// publish holds it. A marker whose owner is gone is taken over, the same rule
// doctor uses to report one and doctor --fix to clear it.
func (a *App) claimBuild(b artifact.Building) error {
	for range 2 {
		existing, claimed, err := a.Store.ClaimBuilding(b)
		if err != nil {
			return cli.Failf(cli.CodeInternal, "could not mark the build: %v", err)
		}
		if claimed {
			return nil
		}
		if !markerStale(existing, time.Now()) {
			return cli.Failf(cli.CodeBuildInProgress,
				"a publish of %q is already running (pid %d, started %s ago)",
				b.Slug, existing.PID, shortAge(time.Since(existing.Started))).
				WithHint("wait for it to finish; if that process is gone, 'otata doctor --fix' clears the marker")
		}
		if err := a.Store.ClearBuilding(b.Slug); err != nil {
			return cli.Failf(cli.CodeInternal, "could not clear a stale build marker: %v", err)
		}
	}
	return cli.Failf(cli.CodeInternal, "could not claim the build marker for %q", b.Slug)
}

// setupHint states the remedy as something to run, because that is what the
// caller does next: an agent literally, a human by pasting it.
func setupHint(e *builder.SetupError) string {
	if e.Command == "" {
		return e.Hint
	}
	hint := "run '" + e.Command + "'"
	if e.Dir != "" {
		hint += " in " + e.Dir
	}
	hint += ", then publish again"
	if e.Hint != "" {
		hint += "; " + e.Hint
	}
	return hint
}

func shortAge(d time.Duration) string { return d.Truncate(time.Second).String() }

// maxBuildAge bounds how long a marker is believed without its process being
// checked. No archive takes six hours, and a marker older than that is a leftover.
const maxBuildAge = 6 * time.Hour

// markerStale is the one rule for whether a build marker still means something.
// Pid liveness is primary but not sufficient because pids are reused after a reboot, and
// a stale marker whose pid now belongs to another of our processes would be
// believed forever — no install link, doctor sees nothing wrong. The age bound
// closes that, and is the only evidence for a marker with no pid.
func markerStale(b artifact.Building, now time.Time) bool {
	if now.Sub(b.Started) > maxBuildAge {
		return true
	}
	if b.PID == 0 {
		return false
	}
	return !processAlive(b.PID)
}

// DefaultConfig is what a publish builds with when --config is absent. Neither
// value works as a configurable default: Release strips every #if DEBUG surface
// out of an iteration build, Debug reports -Onone timings as the app's, and
// neither mistake is visible in the artifact. So it's fixed, and announced.
const DefaultConfig = "Release"

func (a *App) Publish(opts PublishOptions, progress func(string)) (*PublishResult, error) {
	dir := opts.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	abs, _ := filepath.Abs(dir)
	// Resolve symlinks so the recorded project path is one spelling, not
	// whichever the shell happened to use; otherwise the same project checked
	// out through a symlink reports a false slug conflict against itself.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	var b builder.Builder = &builder.XcodeBuild{}
	switch opts.Builder {
	case "", "build":
	case "archive":
		b = &builder.Xcode{}
	default:
		return nil, cli.Fail(cli.CodeInvalidArgs, fmt.Sprintf("unknown builder %q", opts.Builder)).
			WithHint("--builder takes archive or build")
	}
	if opts.Artifact != "" {
		b = &builder.Passthrough{Path: opts.Artifact}
	} else if ok, _ := b.Detect(abs); !ok {
		return nil, cli.Fail(cli.CodeNoProject, "no .xcworkspace or .xcodeproj found here").
			WithHint("run inside a project, or pass --artifact <path to .ipa>")
	}

	slug := opts.Slug
	if slug == "" {
		slug = Slugify(filepath.Base(abs))
	}
	// An explicit --slug used to bypass Slugify entirely and reach filepath.Join.
	if err := storage.ValidateSlug(slug); err != nil {
		return nil, cli.Failf(cli.CodeInvalidArgs, "%v", err).
			WithHint("pass --slug with a simple name like my-app")
	}
	if err := a.CheckSlug(slug, abs); err != nil {
		return nil, err
	}

	// Only a real build has a configuration to resolve, --artifact doesn't
	// build, and the payload's own provenance is recorded instead. A default
	// the caller didn't choose will say so before the build starts.
	config := opts.Config
	if opts.Artifact == "" && config == "" {
		config = DefaultConfig
		progress(fmt.Sprintf("using %s, the default; pass --config to build another way", config))
	}

	tr, err := a.Transport()
	if err != nil {
		return nil, err
	}
	// The server before Ensure: a refusal (no autostart set up, a foreign
	// process on the port) should arrive before anything wires the network.
	if err := a.StartServer(); err != nil {
		return nil, err
	}
	baseURL, err := tr.Ensure(a.Config.Port)
	if err != nil {
		return nil, cli.Failf(cli.CodeTransportDown, "%v", err)
	}

	commit, branch, dirty := GitInfo(abs)

	// Mark before building so the surface shows the in-flight state for the whole
	// window someone might be watching. The marker is also the lock so a second
	// publish of this slug doesn't share its work directory and offer a half-built app as installable.
	if err := a.claimBuild(artifact.Building{
		Slug: slug, Started: time.Now(), Config: config, Commit: commit, PID: os.Getpid(),
	}); err != nil {
		return nil, err
	}
	ctx, interrupted, stopSignals := a.onInterrupt(slug, baseURL)
	defer stopSignals()
	_ = a.Reindex(baseURL)

	published := false
	defer func() {
		// Only the failure path needs this. The success path below clears and
		// reindexes explicitly so it can report a failure to do so.
		if published {
			return
		}
		_ = a.Store.ClearBuilding(slug)
		_ = a.Reindex(baseURL)
	}()

	built, err := b.Build(ctx, builder.Options{
		Dir: abs, Config: config, Scheme: opts.Scheme,
		Work: filepath.Join(a.Root, "build", slug),
		Log:  progress,
	})
	// Asked before the build's own error is read: a build the caller stopped
	// reports the stop, not whatever a killed xcodebuild left in its log.
	// The deferred cleanup above clears the marker on the way out.
	if sig := interrupted(); sig != nil {
		return nil, cli.Failf(cli.CodeInterrupted,
			"stopped by %s; the build was killed and its marker cleared", signalName(sig)).
			WithExit(signalExit(sig))
	}
	if err != nil {
		if amb, ok := errors.AsType[*builder.AmbiguousScheme](err); ok {
			return nil, cli.Failf(cli.CodeAmbiguousScheme, "%v", amb).
				WithHint("re-run with --scheme").WithDetails(map[string]any{"candidates": amb.Candidates})
		}
		// A prerequisite of the project's own toolchain is not a build failure:
		// the code is fine, one command fixes it, and an agent can run that
		// command and retry rather than escalating to a human.
		if setup, ok := errors.AsType[*builder.SetupError](err); ok {
			details := map[string]any{}
			if built.LogPath != "" {
				details["log"] = built.LogPath
			}
			if setup.Command != "" {
				details["command"] = setup.Command
			}
			if setup.Dir != "" {
				details["dir"] = setup.Dir
			}
			f := cli.Failf(cli.CodeNeedsSetup, "%v", setup).WithHint(setupHint(setup))
			// An empty map is not omitted from the JSON the way a nil one is.
			if len(details) > 0 {
				f = f.WithDetails(details)
			}
			return nil, f
		}
		if signing, ok := errors.AsType[*builder.SigningError](err); ok {
			hint := signing.Hint
			if hint == "" {
				hint = "register the device and update the provisioning profile in Apple's developer portal"
			}
			f := cli.Failf(cli.CodeSigningFailed, "%v", signing).WithHint(hint)
			if built.LogPath != "" {
				f = f.WithDetails(map[string]any{"log": built.LogPath})
			}
			return nil, f
		}
		f := cli.Failf(cli.CodeBuildFailed, "%v", err)
		if built.LogPath != "" {
			f = f.WithHint("see " + built.LogPath).WithDetails(map[string]any{"log": built.LogPath})
		}
		return nil, f
	}

	// Metadata always comes from the payload, never the build tree, so every
	// builder converges on one path.
	appFS, closer, appName, err := appmeta.FromIPA(built.PayloadPath)
	if err != nil {
		return nil, cli.Failf(cli.CodeBuildFailed, "%v", err)
	}
	defer closer()
	info, err := appmeta.Read(appFS)
	if err != nil {
		return nil, cli.Failf(cli.CodeBuildFailed, "%v", err)
	}

	// Read while the payload is open. Publish runs constantly and doctor only
	// when someone thinks to run it, so a deadline surfacing only in doctor
	// would surface too late. A read failure is not one. It says nothing about
	// whether the build works, and doctor will say so at leisure.
	//
	// The keychain is enumerated up front and handed in. The held identities
	// are a fact about the machine, not the payload, and are what ReadSigning
	// joins the profile's certificates against.
	held, heldErr := appmeta.HeldIdentities()
	var signing *appmeta.Signing
	var signingWarning string
	// Team is identity read off the profile. It stays empty when the payload
	// carries no readable one: an --artifact publish of something stripped, or
	// a platform that has none.
	team := ""
	if s, err := appmeta.ReadSigning(appFS, held); err == nil {
		now := time.Now()
		team = s.Team
		// Refuse before staging anything. The build is fine and signs fine, but what
		// it cannot do is install by the only route otata has, so publishing
		// would put a URL on the index that iOS declines on every tap.
		// The check reads only the profile, so it holds even when the keychain
		// could not be enumerated.
		if s.Free {
			return nil, cli.Fail(cli.CodeFreeProfile,
				"this build is signed by a free provisioning profile, and iOS refuses to install those over the air").
				WithHint("sign with a paid Apple developer team; a personal team can only install by cable, through Xcode or devicectl").
				WithDetails(map[string]any{
					"profile":   s.ProfileName,
					"ios_error": "MIInstallerErrorDomain 111: apps validated by a free provisioning profile are not allowed to be installed from this source",
				})
		}
		// The deadline is the profile joined against the held certificates.
		// With the keychain unlistable the join is unverifiable, and a deadline
		// that may overstate reality is worse than none, so none is claimed.
		// otata doctor warns about the same payload at leisure.
		if heldErr == nil {
			signing = &s
			if s.Expired(now) || s.Within(signingWindow, now) {
				signingWarning = s.Detail(now)
			}
		}
	}

	payloadName := sanitizeFilename(appName) + ".ipa"
	appDir := a.Store.AppDir(slug)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "%v", err)
	}
	if err := a.Store.CopyInto(filepath.Join(appDir, payloadName), built.PayloadPath); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not stage the payload: %v", err)
	}

	hasIcon := false
	if info.IconName != "" {
		tmpIcon := filepath.Join(a.Store.Tmp(), "icon-"+slug+".png")
		// The normalize must succeed for the icon to ship: Xcode's packaging
		// rewrites PNGs into a form only iOS decodes, and a crushed icon is a
		// broken image on the page where no icon is a clean placeholder.
		if appmeta.CopyOut(appFS, info.IconName, tmpIcon) == nil && appmeta.NormalizeIcon(tmpIcon) == nil {
			if a.Store.CopyInto(filepath.Join(appDir, "icon.png"), tmpIcon) == nil {
				hasIcon = true
			}
		}
		// Remove unconditionally because a failed CopyOut may have left a partial file.
		_ = os.Remove(tmpIcon)
	}

	stat, err := os.Stat(filepath.Join(appDir, payloadName))
	if err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not stat the staged payload: %v", err)
	}
	rec := artifact.Record{
		Slug: slug, Platform: built.Platform, Title: info.Title, BundleID: info.BundleID, Team: team,
		Version: info.Version, Build: info.Build, Config: built.Config,
		Commit: commit, Branch: branch, Dirty: dirty,
		BuiltAt: time.Now(), PayloadName: payloadName,
		SizeBytes: stat.Size(), HasIcon: hasIcon, ProjectPath: abs,
	}
	// The record goes down BEFORE anything derived from it, and before pruning.
	// Pruning first could delete the payload the on-disk record still names.
	if err := a.Store.PutRecord(rec); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not write the record: %v", err)
	}
	if err := a.Store.PruneStalePayloads(slug, payloadName); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not prune old payloads: %v", err)
	}

	// Clear the marker, then render once. A single Reindex produces the final
	// state and can report its own failure. Publishing writes no manifest or
	// page separately, so they cannot disagree.
	if err := a.Store.ClearBuilding(slug); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not clear the build marker: %v", err)
	}
	if err := a.Reindex(baseURL); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "published, but the install page could not be written: %v", err)
	}
	published = true

	return &PublishResult{
		Slug: slug, Title: rec.Title, Version: rec.Version, Build: rec.Build,
		BuildConfig: rec.Config, SizeMB: rec.SizeMB(), Commit: commit, Dirty: dirty,
		InstallURL: fmt.Sprintf("%s/%s/", strings.TrimSuffix(baseURL, "/"), slug),
		IndexURL:   strings.TrimSuffix(baseURL, "/") + "/",
		Transport:  tr.Name(),
		Signing:    signing, SigningWarning: signingWarning,
	}, nil
}

// ---------- list / status ----------

type ListResult struct {
	Apps     []artifact.Record            `json:"apps"`
	Building map[string]artifact.Building `json:"building,omitempty"`
	IndexURL string                       `json:"index_url,omitempty"`
	// ServerRunning is whether the URL above answers right now. The list is
	// read from disk and is correct either way (`stop` unpublishes nothing),
	// but a URL printed for tapping must not look live when it is not.
	ServerRunning bool `json:"server_running"`
}

// listColumns is the single format the header and every row go through, so a
// column cannot be labeled at one width and printed at another. The size is
// rendered to a string first for the same reason: "%6.1f MB" and a heading for
// it would be two formats to keep in agreement.
const listColumns = "%-18s %-20s %-9s %-8s %-11s %9s  %-16s %s"

func (r ListResult) Human(w io.Writer) {
	cli.Section(w, "Published")
	if len(r.Apps) == 0 {
		cli.Line(w, "nothing published yet; run 'otata publish' inside a project")
	} else {
		// Dim: a header is orientation, not content, findable when wanted,
		// and ignorable once the columns are known.
		cli.Line(w, "\033[2m"+listColumns+"\033[0m",
			"SLUG", "APP", "VERSION", "CONFIG", "TEAM", "SIZE", "COMMIT", "BUILT")
	}
	now := time.Now()
	for _, a := range r.Apps {
		commit := a.Commit
		if a.Dirty {
			commit += " +dirty"
		}
		state := render.Age(a.BuiltAt, now)
		if _, ok := r.Building[a.Slug]; ok {
			state = "BUILDING"
		}
		cli.Line(w, listColumns,
			a.Slug, a.Title, a.Version+" ("+a.Build+")", a.Config, orDash(a.Team),
			render.Size(a.SizeMB()), orDash(commit), state)
	}
	switch {
	case r.IndexURL != "" && r.ServerRunning:
		cli.Line(w, "\n\033[1;32m%s\033[0m\n", r.IndexURL)
	case r.IndexURL != "":
		cli.Line(w, "\n\033[1;33mserver is not running\033[0m; 'otata start' brings %s back\n", r.IndexURL)
	}
}

// orDash distinguishes an absent value from a blank one. A payload with no
// readable profile has no team recorded, which is not "signed by nobody".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *App) List() (*ListResult, error) {
	records, err := a.Store.Records()
	if err != nil {
		return nil, cli.Failf(cli.CodeInternal, "%v", err)
	}
	building, _ := a.Store.Building()
	res := &ListResult{Apps: records, Building: building, ServerRunning: a.ServerRunning()}
	if tr, err := a.Transport(); err == nil {
		if st := tr.Status(a.Config.Port); st.BaseURL != "" {
			res.IndexURL = strings.TrimSuffix(st.BaseURL, "/") + "/"
		}
	}
	return res, nil
}

type StatusResult struct {
	Root string `json:"root"`
	Port int    `json:"port"`
	// Version is this binary's; ServerVersion is what the running server
	// reported. They differ when an upgrade replaced the binary on disk and
	// the long-lived server still runs the old image, a state nothing else
	// surfaces. ServerOutdated flags it so an agent doesn't need to compare strings.
	Version        string `json:"version"`
	ServerVersion  string `json:"server_version,omitempty"`
	ServerOutdated bool   `json:"server_outdated,omitempty"`
	ServerRunning  bool   `json:"server_running"`
	// ServerOtherRoot is set when an otata server holds the port but serves a
	// different store, so "down" isn't read as "nothing there".
	ServerOtherRoot bool `json:"server_other_root,omitempty"`
	// Autostart is whether the server returns at login: the agent is installed.
	// AutostartLoaded is whether launchd currently manages it: false after
	// `otata stop`, which boots the job out but leaves it installed. One field
	// answering the second question while labeled as the first made status say
	// "off" about a server that would come back at next login.
	Autostart       bool `json:"autostart"`
	AutostartLoaded bool `json:"autostart_loaded"`
	// AutostartDisabled is set when the user has switched the agent off at the
	// launchd level: the System Settings Login Items toggle, or `launchctl
	// disable`. The plist alone says autostart is on, while macOS will load
	// nothing until it is re-enabled, so the two cannot be conflated.
	AutostartDisabled bool `json:"autostart_disabled,omitempty"`
	// AutostartOtherRoot is set when the user's one launch agent serves a
	// different root or port than this invocation, so "off" is not read as
	// "nothing installed", and so it is clear why `autostart on` refuses.
	AutostartOtherRoot string                       `json:"autostart_other_root,omitempty"`
	AutostartProgram   string                       `json:"autostart_program,omitempty"`
	AutostartStale     bool                         `json:"autostart_stale"`
	Transport          transport.Status             `json:"transport"`
	Apps               []artifact.Record            `json:"apps"`
	Building           map[string]artifact.Building `json:"building,omitempty"`
}

func (r StatusResult) Human(w io.Writer) {
	cli.Section(w, "Status")
	cli.Line(w, "root:      %s", r.Root)
	cli.Line(w, "server:    %s on 127.0.0.1:%d (autostart %s)",
		boolWord(r.ServerRunning, "running", "down"), r.Port, boolWord(r.Autostart, "on", "off"))
	if r.ServerOutdated {
		v := r.ServerVersion
		if v == "" {
			v = "an unreported version"
		}
		cli.Line(w, "           \033[1;33mthe server is running %s and this binary is %s\033[0m; 'otata restart' picks up the new one", v, r.Version)
	}
	if r.ServerOtherRoot {
		cli.Line(w, "           \033[1;33man otata server that is not this root's holds the port\033[0m; 'otata restart' replaces it")
	}
	if r.AutostartOtherRoot != "" {
		cli.Line(w, "           the launch agent serves %s, not this root", r.AutostartOtherRoot)
	}
	// Disabled trumps not-loaded. Reloading a disabled agent cannot work, so
	// the not-loaded line's advice would prescribe a dead end.
	switch {
	case r.Autostart && r.AutostartDisabled:
		cli.Line(w, "           \033[1;33mthe launch agent is disabled\033[0m; enable otata in System Settings > General > Login Items")
	case r.Autostart && !r.AutostartLoaded:
		cli.Line(w, "           \033[1;33mthe launch agent is installed but not loaded\033[0m; 'otata start' reloads it")
	}
	if r.AutostartStale {
		cli.Line(w, "           \033[1;33mthe launch agent runs a stale copy\033[0m; re-run 'otata autostart on'")
	}
	// "not ready", not "not wired": the detail line below says which, and a
	// logged-out tailscale is not a wiring problem.
	cli.Line(w, "transport: %s (%s) %s", r.Transport.Name, r.Transport.Visibility,
		boolWord(r.Transport.Ready, "ready", "not ready"))
	if r.Transport.BaseURL != "" {
		cli.Line(w, "url:       %s/", strings.TrimSuffix(r.Transport.BaseURL, "/"))
	}
	if r.Transport.Detail != "" {
		cli.Line(w, "detail:    %s", r.Transport.Detail)
	}
	cli.Line(w, "apps:      %d published, %d building", len(r.Apps), len(r.Building))
}

func boolWord(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// Status never mutates.
func (a *App) Status() (*StatusResult, error) {
	res := &StatusResult{Root: a.Root, Port: a.Config.Port,
		Version: version.String(), Autostart: a.AutostartEnabled()}
	// One probe answers running, other-root and version at once.
	p, ok := a.probeServer("/")
	res.ServerRunning = ok && p.Root == a.RootDigest()
	if res.ServerRunning {
		res.ServerVersion = p.Version
		res.ServerOutdated = p.Version != res.Version
	} else {
		res.ServerOtherRoot = ok
	}
	if res.Autostart {
		res.AutostartLoaded = a.agentLoaded()
		res.AutostartProgram, res.AutostartStale = a.AutostartProgram()
		res.AutostartDisabled = agentDisabled()
	} else if spec, ok := readAgentPlist(); ok {
		res.AutostartOtherRoot = describeAgent(spec)
	}
	if tr, err := a.Transport(); err == nil {
		res.Transport = tr.Status(a.Config.Port)
	} else {
		res.Transport = transport.Status{Name: "none", Detail: failureDetail(err)}
	}
	if res.Apps, _ = a.Store.Records(); res.Apps == nil {
		res.Apps = []artifact.Record{} // unreadable state dir: still [] not null
	}
	res.Building, _ = a.Store.Building()
	return res, nil
}

// ---------- doctor ----------

type DoctorResult struct {
	Repaired []string `json:"repaired,omitempty"`
	Checks   []Check  `json:"checks"`
	// Healthy is not in the JSON: the envelope's ok and the exit code carry
	// it, and they must not be able to disagree with a third copy.
	Healthy  bool   `json:"-"`
	IndexURL string `json:"index_url,omitempty"`
}

// Failure is the error an unhealthy result is reported with, naming the checks
// that failed so the message alone says where to look.
func (r DoctorResult) Failure() *cli.Failure {
	var failed []string
	for _, c := range r.Checks {
		if !c.OK && !c.Warn {
			failed = append(failed, c.Name)
		}
	}
	noun := "checks"
	if len(failed) == 1 {
		noun = "check"
	}
	return cli.Failf(cli.CodeUnhealthy, "%d %s failed: %s", len(failed), noun, strings.Join(failed, ", ")).
		WithHint("each failing check says what to do")
}

type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Warn marks a check that is not a failure yet but will become one. It never clears Healthy.
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// record appends one check and applies the one severity rule: only a failing
// check clears Healthy, which is what keeps a warning from failing the command
// used as a health gate. Every check reaches the result through here.
func (r *DoctorResult) record(c Check) {
	if !c.OK {
		r.Healthy = false
	}
	r.Checks = append(r.Checks, c)
}

func (r DoctorResult) Human(w io.Writer) {
	if len(r.Repaired) > 0 {
		cli.Section(w, "Repaired")
		for _, x := range r.Repaired {
			cli.Line(w, "%s", x)
		}
	}
	cli.Section(w, "Checks")
	for _, c := range r.Checks {
		// Warn is tested first. A warning is a passing check, so an OK-first
		// switch would swallow every one of them.
		switch {
		case c.Warn:
			cli.Line(w, "\033[1;33mWARN\033[0m  %s: %s", c.Name, c.Detail)
		case c.OK:
			cli.Line(w, "\033[1;32mOK\033[0m    %s", c.Name)
		default:
			cli.Line(w, "\033[1;31mFAIL\033[0m  %s: %s", c.Name, c.Detail)
		}
	}
	if r.Healthy {
		cli.Section(w, "Healthy")
		if r.IndexURL != "" {
			cli.Line(w, "\033[1;32m%s\033[0m\n", r.IndexURL)
		}
	}
}

// processAlive reports whether a pid can still be signaled. Signal 0 performs
// the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid) // never fails on Unix
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// staleMarkers lists the build markers left behind by a process that is gone.
func (a *App) staleMarkers() []string {
	building, err := a.Store.Building()
	if err != nil {
		return nil
	}
	var stale []string
	now := time.Now()
	for slug, b := range building {
		if markerStale(b, now) {
			stale = append(stale, slug)
		}
	}
	sort.Strings(stale)
	return stale
}

// reconcileMarkers clears the stale build markers.
//
// A stale marker removes an app's install link entirely and no other command
// clears it, so doctor --fix, the command for "after a reboot", is exactly where this belongs.
func (a *App) reconcileMarkers() []string {
	var cleared []string
	for _, slug := range a.staleMarkers() {
		if a.Store.ClearBuilding(slug) == nil {
			cleared = append(cleared, slug)
		}
	}
	return cleared
}

// Doctor verifies the whole path (server, transport, markers, every
// published URL, signing deadlines) and with fix repairs what it can first.
// The default is read-only, as `doctor` is everywhere else The remote
// workflow is `doctor --fix`.
func (a *App) Doctor(fix bool) (*DoctorResult, error) {
	res := &DoctorResult{Healthy: true}
	fail := func(name, detail string) {
		res.record(Check{Name: name, Detail: detail})
	}
	// needsFix is a finding the read-only run could have mended.
	needsFix := func(name, problem, remedy string) { fail(name, problem+"; "+remedy) }

	tr, err := a.Transport()
	if err != nil {
		fail("transport", failureDetail(err))
		return res, nil
	}
	if !a.ServerRunning() {
		// Another store's server on the port is refused here exactly as
		// publish refuses it. `otata restart` is the explicit way to replace it.
		// except when that root's own launch agent would respawn whatever restart stops.
		// In that case, the remedy is the agent's removal, and the check says so directly.
		if p, ok := a.otherRootServer(); ok {
			detail := fmt.Sprintf("port %d is held by %s; 'otata restart' replaces it with one for %s, or set OTATA_PORT to a free port",
				a.Config.Port, p.describe(), a.Root)
			if spec, loaded := a.foreignAgentLoaded(); loaded && agentRootDigest(spec) == p.Root {
				detail = fmt.Sprintf("port %d is held by an otata server for %s, kept alive by its launch agent; run 'otata autostart off' to remove that agent (there is one per user), then 'otata autostart on' here",
					a.Config.Port, describeAgent(spec))
			}
			fail("server", detail)
			return res, nil
		}
		// With no agent installed there is nothing --fix can start, and the remedy is the setup step.
		if !a.AutostartEnabled() {
			fail("server", "not running, and autostart is not set up; "+startHint)
			return res, nil
		}
		// A disabled agent is likewise beyond repair from here. Only the user
		// can re-enable what they switched off in Login Items.
		if agentDisabled() {
			fail("server", "not running; "+failureDetail(errAgentDisabled()))
			return res, nil
		}
		if !fix {
			needsFix("server", "not running", "'otata start' or 'otata doctor --fix' reloads the launch agent")
			return res, nil
		}
		if err := a.StartServer(); err != nil {
			fail("server", failureDetail(err))
			return res, nil
		}
		res.Repaired = append(res.Repaired, "reloaded the launch agent")
	}
	// Note the state BEFORE repairing it. Ensure is what wires the transport, so
	// asking afterwards always says "ready" and the repair goes unreported.
	status := tr.Status(a.Config.Port)
	if !status.Ready && !fix {
		// Only a usable-but-unwired transport is --fix's to mend. Any other
		// obstacle (logged out, certificates off, a bad base URL) is on the
		// machine, and promising that --fix wires it sent the caller to a
		// repair that then failed with the real reason.
		if status.Repairable {
			needsFix("transport", tr.Name()+" is not wired to port "+strconv.Itoa(a.Config.Port), "'otata doctor --fix' wires it")
		} else {
			fail("transport", status.Detail)
		}
		return res, nil
	}
	baseURL := status.BaseURL
	if fix {
		baseURL, err = tr.Ensure(a.Config.Port)
		if err != nil {
			fail("transport", err.Error())
			return res, nil
		}
		if !status.Ready {
			res.Repaired = append(res.Repaired, "wired the "+tr.Name()+" transport")
		}
	}
	if fix {
		for _, slug := range a.reconcileMarkers() {
			res.Repaired = append(res.Repaired, "cleared a stale build marker for "+slug)
		}
	} else {
		for _, slug := range a.staleMarkers() {
			needsFix(slug+" build", "a build marker is left from a process that is gone, so the app cannot be installed or republished", "'otata doctor --fix' clears it")
		}
	}
	if fix {
		if err := a.Reindex(baseURL); err != nil {
			fail("reindex", err.Error())
		}
	} else if _, err := os.Stat(filepath.Join(a.Store.Public(), "index.html")); err != nil {
		// Nothing has generated the pages yet. The probes below would report the same absence per URL.
		needsFix("index", "the index page has not been generated", "'otata doctor --fix' writes it")
		return res, nil
	}

	/*
		The server for this root must be able to serve the index over loopback,
		asked for with the path requests actually arrive with. Under a
		keep-prefix manual transport the bare root is refused by design, so
		probing "/" declared every healthy keep-prefix server broken, and
		doctor --fix restarted one on every run. If the index does not answer,
		the server holds a directory that no longer exists (os.Root keeps the
		directory open, and `rm -rf ~/.otata` followed by a publish recreates
		the tree beside the one it is serving) or strips a prefix the config
		has since changed. Either way it reports as running, so nothing else
		would ever restart it, and a restart fixes both.
	*/
	if p, ok := a.probeServer(a.IncomingPrefix() + "/"); ok && p.Root == a.RootDigest() {
		switch {
		case p.Status != http.StatusOK:
			const brokenIndex = "running, but not serving this store's index; its public directory was replaced, or its prefix has changed"
			switch {
			case fix && a.AutostartEnabled():
				if err := a.StopServer(); err == nil {
					if err := a.StartServer(); err == nil {
						res.Repaired = append(res.Repaired, "restarted a server that was not serving this store's index")
					}
				}
			case a.AutostartEnabled():
				needsFix("server", brokenIndex, "'otata restart' or 'otata doctor --fix' restarts it")
				return res, nil
			default:
				// A foreground `otata serve`. Stopping it out from under its
				// own terminal is not a repair, so say what to do instead.
				fail("server", brokenIndex+"; restart 'otata serve'")
				return res, nil
			}
		case p.Version != version.String():
			// The server works, so this is a warning, not a repair. An upgrade
			// replaced the binary on disk and the long-lived server still runs
			// the old image. Restarting a healthy server mid-download is not
			// --fix's call to make.
			from := p.Version
			if from == "" {
				from = "an unreported version"
			}
			res.record(Check{Name: "server version", OK: true, Warn: true,
				Detail: fmt.Sprintf("the server is running %s and this binary is %s; 'otata restart' picks up the new one",
					from, version.String())})
		}
	}
	res.IndexURL = strings.TrimSuffix(baseURL, "/") + "/"

	records, _ := a.Store.Records()

	// The keychain answer is per-machine, not per-app. One enumeration serves
	// every signing check below. Enumerating it per app made doctor's cost
	// scale with the store.
	var held map[string]bool
	var heldErr error
	if len(records) > 0 {
		held, heldErr = appmeta.HeldIdentities()
	}

	// The default client has no timeout, so a wedged relay would hang doctor
	// forever, and a hang gives an agent nothing to act on, unlike an error.
	client := &http.Client{Timeout: 20 * time.Second}

	/*
		The probes are independent round trips over the real transport, so they
		fly together rather than one at a time. Serially, a store of N apps hung
		for up to (2N+1) timeouts when the relay was wedged.

		The concurrency is bounded so a large store does not slam the relay.
	*/
	type urlProbe struct{ name, url string }
	probes := []urlProbe{{"index", res.IndexURL}}
	for _, r := range records {
		base := strings.TrimSuffix(baseURL, "/") + "/" + r.Slug
		probes = append(probes,
			urlProbe{r.Slug + " manifest", base + "/manifest.plist"},
			urlProbe{r.Slug + " payload", base + "/" + r.PayloadName})
	}
	probed := make([]Check, len(probes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, p := range probes {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			probed[i] = probeURL(client, p.name, p.url)
		})
	}

	// The signing checks read local payloads and spawn the CMS unwrap, which is work
	// that neither touches nor waits on the network. Thus, it runs here while the
	// probes are in flight.
	now := time.Now()
	signing := make([]Check, len(records))
	signingPresent := make([]bool, len(records))
	for i, r := range records {
		signing[i], signingPresent[i] = a.checkSigning(r, held, heldErr, now)
	}
	wg.Wait()

	// Assembled in the order the serial loop produced: the index, then each
	// app's manifest, payload and signing.
	res.record(probed[0])
	for i := range records {
		res.record(probed[1+2*i])
		res.record(probed[2+2*i])
		if signingPresent[i] {
			res.record(signing[i])
		}
	}
	return res, nil
}

// probeURL asks whether one URL answers. With HEAD, it answers the same
// question as GET without streaming a payload that can be hundreds of
// megabytes over the tailnet.
func probeURL(client *http.Client, name, url string) Check {
	c := Check{Name: name}
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	resp, err := client.Do(req)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	resp.Body.Close()
	c.OK = resp.StatusCode == http.StatusOK
	if !c.OK {
		c.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return c
}

// signingWindow is how long before expiry doctor starts saying so.
const signingWindow = 30 * 24 * time.Hour

/*
checkSigning reports when a published build stops being installable by reading
the profile out of the staged payload rather than Xcode's profile directory.
The payload is what the phone installed, so there is no guess about which
profile signing chose, and an answer even if the project has since moved.
held and heldErr are the one keychain enumeration doctor made for every app.
Ok is false when there is nothing to say about this payload.
*/
func (a *App) checkSigning(r artifact.Record, held map[string]bool, heldErr error, now time.Time) (c Check, ok bool) {
	name := r.Slug + " signing"
	appFS, closer, _, err := appmeta.FromIPA(filepath.Join(a.Store.AppDir(r.Slug), r.PayloadName))
	if err != nil {
		// The payload probe already reports a payload that cannot be read. Saying it twice would imply two problems.
		return Check{}, false
	}
	defer closer()

	sig, err := appmeta.ReadSigning(appFS, held)
	switch {
	// Neither is a fault. An Android build carries no profile, and a node that
	// only serves has no business auditing what another machine signed.
	case errors.Is(err, appmeta.ErrNoProfile), errors.Is(err, appmeta.ErrUnsupported):
		return Check{}, false
	case err != nil:
		// Present but unreadable is worth saying quietly. nothing published is broken by it, just unverifiable.
		return Check{Name: name, OK: true, Warn: true, Detail: err.Error()}, true
	}
	// With the keychain unlistable the deadline is unverifiable, and the same
	// quiet warning is the honest report. The free developer profile wall stands
	// either way, since it only reads the profile.
	if heldErr != nil && !sig.Free {
		return Check{Name: name, OK: true, Warn: true, Detail: heldErr.Error()}, true
	}
	return signingCheck(name, sig, now), true
}

// signingCheck maps a deadline onto a severity. Split out from the reading so
// the mapping, which is the whole point of the check, is testable without a
// staged payload, a keychain or a particular date.
func signingCheck(name string, sig appmeta.Signing, now time.Time) Check {
	c := Check{Name: name, OK: true, Detail: sig.Detail(now)}
	// A free profile is not a deadline to count down. Publishing refuses these.
	if sig.Free {
		c.OK = false
		c.Detail = "signed by a free provisioning profile; iOS refuses to install it over the air"
		return c
	}
	switch {
	case sig.Expired(now):
		// Not a forecast. The installed app has stopped launching and the next publish will fail to sign.
		c.OK = false
	case sig.Within(signingWindow, now):
		c.Warn = true
	}
	return c
}

// ---------- serve / forget ----------

func (a *App) Serve() error {
	srv, err := server.New(a.Store.Public(), a.IncomingPrefix(), a.RootDigest(), log.New(os.Stdout, "", 0))
	if err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	defer srv.Close()
	ln, err := server.Listen(a.Config.Port)
	if err != nil {
		return cli.Failf(cli.CodeServerDown, "could not bind 127.0.0.1:%d: %v", a.Config.Port, err)
	}
	fmt.Printf("serving %s on 127.0.0.1:%d\n", a.Store.Public(), a.Config.Port)
	return http.Serve(ln, srv)
}

type ForgetResult struct {
	Slug string `json:"slug"`
}

func (r ForgetResult) Human(w io.Writer) {
	cli.Section(w, "Removed")
	cli.Line(w, "%s", r.Slug)
}

func (a *App) Forget(slug string) (*ForgetResult, error) {
	if err := storage.ValidateSlug(slug); err != nil {
		return nil, cli.Failf(cli.CodeInvalidArgs, "%v", err)
	}
	// A corrupt record used to make an app impossible to remove while it stayed
	// fully installable, so an unreadable record is a reason to remove. Likewise a directory with no record at all.
	_, ok, recErr := a.Store.Record(slug)
	orphan := false
	if !ok && recErr == nil {
		if info, err := os.Stat(a.Store.AppDir(slug)); err == nil && info.IsDir() {
			orphan = true
		}
	}
	if !ok && recErr == nil && !orphan {
		return nil, cli.Failf(cli.CodeNotFound, "no app published under %q", slug)
	}
	// A live build would write its record and payload back after this
	// removed them, leaving an app with no marker, no index entry and a
	// payload on disk.
	if building, err := a.Store.Building(); err == nil {
		if b, held := building[slug]; held && !markerStale(b, time.Now()) {
			return nil, cli.Failf(cli.CodeBuildInProgress,
				"a publish of %q is running (pid %d); it would recreate what forget removes", slug, b.PID).
				WithHint("wait for it to finish, or stop it first")
		}
	}
	if err := a.Store.Remove(slug); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "%v", err)
	}
	if tr, err := a.Transport(); err == nil {
		if st := tr.Status(a.Config.Port); st.BaseURL != "" {
			_ = a.Reindex(st.BaseURL)
		}
	}
	return &ForgetResult{Slug: slug}, nil
}

// ---------- helpers ----------

// failureDetail flattens a Failure into one line for a doctor check or a
// status row, which have no separate hint field. The fix must ride along,
// or the check breaks doctor's promise that every failure says what to do.
func failureDetail(err error) string {
	f := cli.AsFailure(err)
	if f.Hint == "" {
		return f.Message
	}
	return f.Message + "; " + f.Hint
}

func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "app"
	}
	return b.String()
}

// StartServer brings the server up through the launch agent, which is the only way a
// background server exists. Everything that needs the server comes through
// here. With no agent installed, it refuses and tells you the command to run.
func (a *App) StartServer() error {
	if a.ServerRunning() {
		return nil
	}
	if p, ok := a.otherRootServer(); ok {
		// Publish must not kill the server of another store because an environment variable was set in this shell.
		return cli.Failf(cli.CodeServerDown, "port %d is held by %s", a.Config.Port, p.describe()).
			WithHint("run 'otata restart' to replace it with one for " + a.Root + ", or set OTATA_PORT to a free port")
	}
	if a.PortBusy() {
		return cli.Failf(cli.CodeServerDown,
			"port %d is held by another process, not otata", a.Config.Port).
			WithHint("stop it, or set OTATA_PORT to a free port")
	}

	if !a.AutostartEnabled() {
		return cli.Fail(cli.CodeServerDown, "the server is not running").WithHint(startHint)
	}
	return a.reloadAgent()
}

// StopServer stops our server, and only ours.
// It identifies the process over HTTP instead of something like lsof,
// since a process holding a port doesn't necessarily mean it's otata.
func (a *App) StopServer() error {
	// launchd's KeepAlive respawns anything we signal, so while the agent is
	// loaded, stopping means booting the job out, not sending a signal.
	// The waits below ask whether ANY otata server still holds the port, not
	// whether ours does. A server for another root never counts as running,
	// and waiting on that would declare it stopped while it was still alive.
	gone := func() bool { _, ours := a.serverPID(); return !ours }

	// A server another root's launch agent keeps alive cannot be stopped from
	// here. Signaling it only makes launchd respawn it, and booting the job
	// out is that root's `autostart off`.
	if spec, loaded := a.foreignAgentLoaded(); loaded {
		if p, held := a.otherRootServer(); held && agentRootDigest(spec) == p.Root {
			return cli.Failf(cli.CodeServerDown,
				"port %d is held by an otata server for %s, kept alive by its launch agent",
				a.Config.Port, describeAgent(spec)).
				WithHint("run 'otata autostart off' to remove that agent (there is one per user), then 'otata autostart on' here")
		}
	}

	if a.agentLoaded() {
		if err := bootoutAgent(); err != nil {
			return err
		}
		for range 30 {
			if gone() {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	pid, ours := a.serverPID()
	if !ours {
		if a.PortBusy() {
			return cli.Failf(cli.CodeInternal,
				"port %d is held by another process, not otata; refusing to signal it", a.Config.Port)
		}
		return nil
	}
	if pid <= 0 {
		return cli.Failf(cli.CodeInternal, "the server did not report its pid")
	}
	proc, _ := os.FindProcess(pid) // never fails on Unix
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return cli.Failf(cli.CodeInternal, "could not signal the server (pid %d): %v", pid, err)
	}
	for range 30 {
		if gone() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cli.Failf(cli.CodeInternal, "server on port %d did not stop", a.Config.Port)
}
