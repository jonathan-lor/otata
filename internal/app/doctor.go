package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/version"
)

type DoctorResult struct {
	Repaired []string `json:"repaired,omitempty"`
	Checks   []Check  `json:"checks"`
	// Healthy is carried by the envelope's ok and the exit code instead.
	Healthy  bool   `json:"-"`
	IndexURL string `json:"index_url,omitempty"`
}

// Failure is the error for an unhealthy result, naming the failed checks.
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
	// Warn marks a passing check that needs attention soon.
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// record adds a check. Only a failing one clears Healthy, so a warning never
// fails doctor.
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
		// Warn first, since a warning is also OK.
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

// reconcileMarkers clears stale build markers and returns their slugs.
func (a *App) reconcileMarkers() []string {
	var cleared []string
	for _, slug := range a.staleMarkers() {
		if a.Store.ClearBuilding(slug) == nil {
			cleared = append(cleared, slug)
		}
	}
	return cleared
}

// Doctor verifies the server, transport, build markers, every published URL
// and signing. With fix it repairs what it can first.
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
		// Never replace another root's server here. If its own unit keeps it
		// alive, removing that unit is the only fix.
		if p, ok := a.otherRootServer(); ok {
			detail := fmt.Sprintf("port %d is held by %s; 'otata restart' replaces it with one for %s, or set OTATA_PORT to a free port",
				a.Config.Port, p.describe(), a.Root)
			if spec, loaded := a.foreignAgentLoaded(); loaded && agentRootDigest(spec) == p.Root {
				detail = fmt.Sprintf("port %d is held by an otata server for %s, kept alive by its %s; run 'otata autostart off' to remove it (there is one per user), then 'otata autostart on' here",
					a.Config.Port, describeAgent(spec), a.autostart().Kind())
			}
			fail("server", detail)
			return res, nil
		}
		// With no unit, --fix has nothing to start.
		if !a.AutostartEnabled() {
			fail("server", "not running, and autostart is not set up; "+a.autostart().StartHint())
			return res, nil
		}
		// Only the user can re-enable a disabled unit.
		if a.autostart().Disabled() {
			fail("server", "not running; "+failureDetail(a.errAgentDisabled()))
			return res, nil
		}
		if !fix {
			needsFix("server", "not running", "'otata start' or 'otata doctor --fix' reloads the "+a.autostart().Kind())
			return res, nil
		}
		if err := a.StartServer(); err != nil {
			fail("server", failureDetail(err))
			return res, nil
		}
		res.Repaired = append(res.Repaired, "reloaded the "+a.autostart().Kind())
	}
	status := tr.Status(a.Config.Port)
	if !status.Ready && !fix {
		fail("transport", status.Detail)
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
			res.Repaired = append(res.Repaired, "prepared the "+tr.Name()+" transport")
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
	} else if _, err := os.Stat(a.Store.IndexPath()); err != nil {
		// No pages yet, which every URL probe would just repeat.
		needsFix("index", "the index page has not been generated", "'otata doctor --fix' writes it")
		return res, nil
	}

	// A server that answers but can't serve the index holds a deleted public/
	// (os.Root keeps it open) or a stale prefix. Only a restart fixes either.
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
				// A foreground `otata serve` is for its own terminal to restart.
				fail("server", brokenIndex+"; restart 'otata serve'")
				return res, nil
			}
		case p.Version != version.String():
			// An upgrade left the old binary running. Warn rather than restart a
			// working server.
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

	// One keychain read serves every iOS signing check.
	var held map[string]bool
	var heldErr error
	if slices.ContainsFunc(records, func(r artifact.Record) bool { return r.Platform == artifact.IOS }) {
		held, heldErr = appmeta.HeldIdentities()
	}

	// A timeout, so a wedged connection can't hang doctor.
	client := &http.Client{Timeout: 20 * time.Second}

	// Probe concurrently, at most 8 at a time.
	type urlProbe struct{ name, url string }
	probes := []urlProbe{{"index", res.IndexURL}}
	// Record i's probes are probes[first[i]:first[i+1]]. Only iOS apps have a
	// manifest to probe.
	first := make([]int, len(records)+1)
	for i, r := range records {
		first[i] = len(probes)
		base := strings.TrimSuffix(baseURL, "/") + "/" + r.Slug
		if r.Platform.InstallsFromManifest() {
			probes = append(probes, urlProbe{r.Slug + " manifest", base + "/manifest.plist"})
		}
		probes = append(probes, urlProbe{r.Slug + " payload", base + "/" + r.PayloadName})
	}
	first[len(records)] = len(probes)
	probed := make([]Check, len(probes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, p := range probes {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if embedded, ok := tr.(interface {
				Probe(context.Context, string) (int, error)
			}); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				code, err := embedded.Probe(ctx, p.url)
				c := Check{Name: p.name, OK: err == nil && code == http.StatusOK}
				if err != nil {
					c.Detail = err.Error()
				} else if !c.OK {
					c.Detail = fmt.Sprintf("HTTP %d", code)
				}
				probed[i] = c
			} else {
				probed[i] = probeURL(client, p.name, p.url)
			}
		})
	}

	// Check signing locally while the probes run.
	now := time.Now()
	signing := make([]Check, len(records))
	signingPresent := make([]bool, len(records))
	for i, r := range records {
		signing[i], signingPresent[i] = a.checkSigning(r, held, heldErr, now)
	}
	wg.Wait()

	// Report the index, then each app's manifest, payload and signing.
	res.record(probed[0])
	for i := range records {
		for _, c := range probed[first[i]:first[i+1]] {
			res.record(c)
		}
		if signingPresent[i] {
			res.record(signing[i])
		}
	}
	return res, nil
}

// probeURL checks that url answers 200 to a HEAD, which skips downloading
// the payload.
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

// checkSigning reads the signing of the staged payload, which is what the
// phone installs. ok is false when there is nothing to report.
func (a *App) checkSigning(r artifact.Record, held map[string]bool, heldErr error, now time.Time) (c Check, ok bool) {
	name := r.Slug + " signing"
	payload, err := appmeta.Open(r.Platform, a.Store.PayloadPath(r.Slug, r.PayloadName))
	if err != nil {
		// The payload probe already reports an unreadable payload.
		return Check{}, false
	}
	defer payload.Close()

	sig, err := payload.Signing(held)
	switch {
	// No profile, or no tools to read it here, is nothing to report.
	case errors.Is(err, appmeta.ErrNoProfile), errors.Is(err, appmeta.ErrUnsupported):
		return Check{}, false
	// An APK that doesn't verify can't be installed.
	case errors.Is(err, appmeta.ErrUnsigned):
		return Check{Name: name, Detail: err.Error() + "; Android refuses to install it"}, true
	case err != nil:
		// Unreadable signing is unverified, not broken.
		return Check{Name: name, OK: true, Warn: true, Detail: err.Error()}, true
	}
	// Without the keychain an iOS deadline can't be verified. A free profile
	// still fails.
	if heldErr != nil && sig.HasDeadline() && !sig.Free {
		return Check{Name: name, OK: true, Warn: true, Detail: heldErr.Error()}, true
	}
	return signingCheck(name, sig, now), true
}

// signingCheck rates signing by how near its deadline is. Signing with no
// deadline passes.
func signingCheck(name string, sig appmeta.Signing, now time.Time) Check {
	c := Check{Name: name, OK: true, Detail: sig.Detail(now)}
	if sig.Free {
		c.OK = false
		c.Detail = "signed by a free provisioning profile; iOS refuses to install it over the air"
		return c
	}
	switch {
	case sig.Expired(now):
		c.OK = false
	case sig.Within(signingWindow, now):
		c.Warn = true
	}
	return c
}

// failureDetail joins a Failure's message and hint into one line.
func failureDetail(err error) string {
	f := cli.AsFailure(err)
	if f.Hint == "" {
		return f.Message
	}
	return f.Message + "; " + f.Hint
}
