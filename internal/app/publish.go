package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/builder"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/render"
	"github.com/jonathan-lor/otata/internal/storage"
)

type PublishOptions struct {
	Dir      string
	Config   string
	Scheme   string
	Slug     string
	Artifact string
	Builder  string // "" or "build" for the incremental build path, "archive" for the archive+export path
	// Platform is what to build for; empty is iOS. One publish is one
	// platform: a cross-platform project publishes once per platform, each
	// under its own slug. No flag selects another platform yet.
	Platform artifact.Platform
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

	platform := opts.Platform
	if platform == "" {
		platform = artifact.IOS
	}
	// The builder is chosen and the project found before anything is claimed
	// or wired, so "nothing to build here" arrives at once. A prebuilt
	// payload skips both: there is nothing to detect.
	b, err := builder.For(platform, opts.Builder)
	if err != nil {
		return nil, cli.Failf(cli.CodeInvalidArgs, "%v", err)
	}
	container := ""
	if opts.Artifact == "" {
		if container, err = b.Detect(abs); err != nil {
			return nil, cli.Failf(cli.CodeNoProject, "%v", err).
				WithHint("run inside a project, or pass --artifact <path to a built payload>")
		}
	}
	slug := opts.Slug
	if slug == "" {
		slug = DefaultSlug(abs, platform)
	}
	// An explicit --slug used to bypass Slugify entirely and reach filepath.Join.
	if err := storage.ValidateSlug(slug); err != nil {
		return nil, cli.Failf(cli.CodeInvalidArgs, "%v", err).
			WithHint("pass --slug with a simple name like my-app")
	}
	if err := a.CheckSlug(slug, abs, platform); err != nil {
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

	var built builder.Result
	if opts.Artifact != "" {
		built, err = builder.Prebuilt(opts.Artifact)
	} else {
		built, err = b.Build(ctx, builder.Options{
			Container: container, Config: config, Scheme: opts.Scheme,
			Work: a.Store.BuildDir(slug),
			Log:  progress,
		})
	}
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
	// The slug was checked against the platform asked for, so the payload had
	// better be that platform's, or the record and the check disagree.
	if built.Platform != platform {
		return nil, cli.Failf(cli.CodeInternal, "asked for an %s build and got an %s payload", platform, built.Platform)
	}

	// Metadata always comes from the payload, never the build tree, so every
	// builder converges on one path, and the platform's reader is the only
	// thing that knows how the payload is packaged.
	payload, err := appmeta.Open(platform, built.PayloadPath)
	if err != nil {
		return nil, cli.Failf(cli.CodeBuildFailed, "%v", err)
	}
	defer payload.Close()
	info, err := payload.Info()
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
	if s, err := payload.Signing(held); err == nil {
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

	payloadName := sanitizeFilename(info.Name) + platform.PayloadExt()
	payloadPath := a.Store.PayloadPath(slug, payloadName)
	if err := a.Store.CopyInto(payloadPath, built.PayloadPath); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "could not stage the payload: %v", err)
	}

	// The icon ships only when the reader could produce a standard PNG; no
	// icon is a clean placeholder on the page where a broken image is not.
	hasIcon := false
	tmpIcon := a.Store.TmpFile("icon-" + slug + ".png")
	if payload.Icon(tmpIcon) == nil {
		if a.Store.CopyInto(a.Store.IconPath(slug), tmpIcon) == nil {
			hasIcon = true
		}
	}
	// Remove unconditionally because a failed write may have left a partial file.
	_ = os.Remove(tmpIcon)

	stat, err := os.Stat(payloadPath)
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
	if err := a.Store.PruneStalePayloads(slug, payloadName, platform.PayloadExt()); err != nil {
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

// processAlive reports whether a pid still exists. Signal 0 performs the
// existence and permission checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid) // never fails on Unix
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	// EPERM is a process that exists and belongs to someone else. That is
	// alive: a marker is stale only when its process is gone, and reading a
	// refused signal as "gone" cleared the marker of a publish another user
	// was still running.
	return err == nil || errors.Is(err, syscall.EPERM)
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
