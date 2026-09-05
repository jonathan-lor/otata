package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
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
		// except when that root's own unit would respawn whatever restart stops.
		// In that case, the remedy is the unit's removal, and the check says so directly.
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
		// With no unit installed there is nothing --fix can start, and the remedy is the setup step.
		if !a.AutostartEnabled() {
			fail("server", "not running, and autostart is not set up; "+a.autostart().StartHint())
			return res, nil
		}
		// A disabled unit is likewise beyond repair from here. Only the user
		// can re-enable what they switched off.
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
	} else if _, err := os.Stat(a.Store.IndexPath()); err != nil {
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
	// scale with the store. Only an iOS build's deadline is joined against
	// it, so a store of Android builds never asks.
	var held map[string]bool
	var heldErr error
	if slices.ContainsFunc(records, func(r artifact.Record) bool { return r.Platform == artifact.IOS }) {
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
	// first[i] is where record i's probes start; they run to first[i+1].
	// Records differ in how many they have: an iOS app installs from a
	// manifest, which must answer too, while an Android app is its payload alone.
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
	// app's manifest (where it has one), payload and signing.
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
	payload, err := appmeta.Open(r.Platform, a.Store.PayloadPath(r.Slug, r.PayloadName))
	if err != nil {
		// The payload probe already reports a payload that cannot be read, and
		// saying it twice would imply two problems. A platform with no reader
		// yet has nothing to say either.
		return Check{}, false
	}
	defer payload.Close()

	sig, err := payload.Signing(held)
	switch {
	// Neither is a fault. A stripped payload carries no profile, and a node
	// that only serves has no business auditing what another machine signed.
	case errors.Is(err, appmeta.ErrNoProfile), errors.Is(err, appmeta.ErrUnsupported):
		return Check{}, false
	// An APK that does not verify is served and cannot be installed, which
	// is as broken as an expired profile. Publishing refuses these.
	case errors.Is(err, appmeta.ErrUnsigned):
		return Check{Name: name, Detail: err.Error() + "; Android refuses to install it"}, true
	case err != nil:
		// Present but unreadable is worth saying quietly. nothing published is broken by it, just unverifiable.
		return Check{Name: name, OK: true, Warn: true, Detail: err.Error()}, true
	}
	// With the keychain unlistable an iOS deadline is unverifiable, and the
	// same quiet warning is the honest report. The free developer profile
	// wall stands either way, since it only reads the profile. Signing with
	// no deadline was never joined against the keychain.
	if heldErr != nil && sig.HasDeadline() && !sig.Free {
		return Check{Name: name, OK: true, Warn: true, Detail: heldErr.Error()}, true
	}
	return signingCheck(name, sig, now), true
}

// signingCheck maps signing onto a severity: a deadline by how near it is,
// and an identity with none as a passing check that names the signer. Split
// out from the reading so the mapping, which is the whole point of the
// check, is testable without a staged payload, a keychain or a particular
// date.
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
