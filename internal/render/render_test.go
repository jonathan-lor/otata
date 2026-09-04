package render

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

func rec() artifact.Record {
	return artifact.Record{
		Slug: "myapp", Platform: artifact.IOS, Title: "My App", BundleID: "com.x.myapp",
		Version: "1.2", Build: "7", Config: "Debug", Commit: "abc1234", Branch: "main",
		BuiltAt: time.Now().Add(-90 * time.Second), SizeBytes: 15 << 20, HasIcon: true,
	}
}

// The trap: html/template replaces unknown URL schemes in href with #ZgotmplZ,
// which would break every install link without erroring.
func TestInstallLinkSurvivesTemplateEscaping(t *testing.T) {
	out, err := Index("host.ts.net", "https://host.ts.net/otata", []artifact.Record{rec()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if strings.Contains(body, "ZgotmplZ") {
		t.Fatal("html/template mangled the itms-services URL")
	}
	if !strings.Contains(body, "itms-services://?action=download-manifest&amp;url=https%3A%2F%2Fhost.ts.net%2Fotata%2Fmyapp%2Fmanifest.plist") {
		t.Errorf("install URL missing or wrongly encoded:\n%s", excerpt(body, "itms-services"))
	}
}

// Everything outside the unreserved set is percent-encoded, a space and a
// plus included, or the manifest URL inside the itms-services href comes
// apart. Pinned so the encoding cannot drift with its implementation.
func TestEscape(t *testing.T) {
	cases := map[string]string{
		"https://host.ts.net/otata/myapp/manifest.plist?v=abc-7": "https%3A%2F%2Fhost.ts.net%2Fotata%2Fmyapp%2Fmanifest.plist%3Fv%3Dabc-7",
		"a b+c&d=e":      "a%20b%2Bc%26d%3De",
		"safe-._~09AZaz": "safe-._~09AZaz",
		"":               "",
	}
	for in, want := range cases {
		if got := escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

// An Android app installs by fetching the payload, so its link is the payload
// itself with the same cache key, with no itms-services scheme and no manifest
// anywhere on the page, and the page's advice is Android's rather than iOS's.
func TestAndroidInstallsByDirectDownload(t *testing.T) {
	r := rec()
	r.Platform, r.PayloadName = artifact.Android, "MyApp.apk"
	index, err := Index("host", "https://host/otata", []artifact.Record{r}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app, err := App(r, "https://host/otata", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index": string(index), "app": string(app)} {
		if !strings.Contains(body, `href="https://host/otata/myapp/MyApp.apk?v=abc1234-7"`) {
			t.Errorf("the %s page does not link the payload with its cache key:\n%s", name, excerpt(body, "install"))
		}
		if strings.Contains(body, "itms-services") || strings.Contains(body, "manifest.plist") {
			t.Errorf("the %s page refers to a manifest an Android app does not have", name)
		}
		if strings.Contains(body, "Developer Mode") {
			t.Errorf("the %s page gives iOS advice for an Android app", name)
		}
		if !strings.Contains(body, "allow installs") {
			t.Errorf("the %s page gives no Android install advice", name)
		}
		if !strings.Contains(stripScripts(body), "Android") {
			t.Errorf("the %s page does not name the platform", name)
		}
	}

	// A mixed index carries both platforms' advice, and an iOS-only one none of Android's.
	ios := rec()
	ios.Slug = "iosapp"
	mixed, _ := Index("host", "https://host/otata", []artifact.Record{ios, r}, nil)
	if !strings.Contains(string(mixed), "Developer Mode") || !strings.Contains(string(mixed), "allow installs") {
		t.Error("a mixed index dropped one platform's advice")
	}
	only, _ := Index("host", "https://host/otata", []artifact.Record{ios}, nil)
	if strings.Contains(string(only), "allow installs") {
		t.Error("an iOS-only index carries Android advice")
	}
}

// The page is a file. It is written once and read at any later moment, so it
// carries the build's instant and the phone works out the age.
func TestFreshnessIsStated(t *testing.T) {
	r := rec()
	out, _ := Index("host", "https://host/otata", []artifact.Record{r}, nil)
	body := string(out)
	want := `<time datetime="` + r.BuiltAt.UTC().Format(time.RFC3339) + `" data-age>`
	if !strings.Contains(body, want) {
		t.Errorf("entry does not carry its build instant for the script:\n%s", excerpt(body, "Built"))
	}
	if !strings.Contains(body, `id="ages"`) {
		t.Error("no age script to turn the instant into an age")
	}
	// The markup outside the script must not state an age. The script's own
	// source carries the words, so the check is on the page with it removed.
	markup := stripScripts(body)
	for _, phrase := range []string{" ago", "just now", "Building for"} {
		if strings.Contains(markup, phrase) {
			t.Errorf("a relative age %q was rendered into a file that outlives it", phrase)
		}
	}
}

// With no instant there is nothing to compute, so no datetime invites the script to try.
func TestUnknownBuildTimeHasNoInstant(t *testing.T) {
	r := rec()
	r.BuiltAt = time.Time{}
	out, _ := Index("host", "https://host/otata", []artifact.Record{r}, nil)
	body := string(out)
	if strings.Contains(stripScripts(body), "data-age") {
		t.Error("offered a datetime for a build whose time is unknown")
	}
	if !strings.Contains(stripScripts(body), "unknown") {
		t.Error("an unknown build time is not stated as unknown")
	}
}

func stripScripts(html string) string {
	for {
		start := strings.Index(html, "<script")
		if start < 0 {
			return html
		}
		end := strings.Index(html[start:], "</script>")
		if end < 0 {
			return html[:start]
		}
		html = html[:start] + html[start+end+len("</script>"):]
	}
}

// An app mid-build must not offer its stale payload as if it were current.
func TestBuildingSuppressesInstall(t *testing.T) {
	building := map[string]artifact.Building{
		"myapp": {Slug: "myapp", Started: time.Now().Add(-40 * time.Second)},
	}
	out, err := Index("host", "https://host/otata", []artifact.Record{rec()}, building)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if strings.Contains(body, `class="install"`) {
		t.Error("install link offered while a build is in flight")
	}
	// The elapsed time is computed on the phone from the start instant, for
	// the same reason the age is: the file is read long after it is written,
	// and the refresh while building reloads the same file.
	since := `<time datetime="` + building["myapp"].Started.UTC().Format(time.RFC3339) + `" data-since>`
	if !strings.Contains(body, since) {
		t.Errorf("build not reported as in progress with its start instant:\n%s", excerpt(body, "Building"))
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("page does not refresh while a build is running")
	}
}

// A first build has no record yet, the moment someone is most likely watching.
func TestFirstBuildIsVisible(t *testing.T) {
	building := map[string]artifact.Building{
		"newapp": {Slug: "newapp", Started: time.Now().Add(-5 * time.Second), Config: "Debug"},
	}
	out, _ := Index("host", "https://host/otata", nil, building)
	body := string(out)
	if !strings.Contains(body, "newapp") {
		t.Error("in-flight first build is invisible")
	}
	// It also used to say there was nothing published, beside the running
	// build; asserting only that the slug appears did not catch that.
	if strings.Contains(body, "Nothing published yet") {
		t.Error("claims nothing is published while a build is running")
	}
}

func TestEmptyStateWhenTrulyEmpty(t *testing.T) {
	out, _ := Index("host", "https://host/otata", nil, nil)
	if !strings.Contains(string(out), "Nothing published yet") {
		t.Error("no empty state on a genuinely empty index")
	}
}

func TestNoRefreshWhenIdle(t *testing.T) {
	out, _ := Index("host", "https://host/otata", []artifact.Record{rec()}, nil)
	if strings.Contains(string(out), `http-equiv="refresh"`) {
		t.Error("page refreshes with nothing building")
	}
}

// One format for a size everywhere a human reads one. It closes a trap of a
// fixed "%.1f MB" showing a small app as "0.0 MB".
func TestSize(t *testing.T) {
	cases := map[float64]string{
		21238.0 / (1 << 20): "21 KB", // arbitrary small value
		0.5:                 "512 KB",
		0.96:                "1.0 MB", // %.1f would round here anyway; KB would overshoot 1024
		6.4:                 "6.4 MB",
		1500:                "1500.0 MB",
		0:                   "0 KB",
	}
	for mb, want := range cases {
		if got := Size(mb); got != want {
			t.Errorf("Size(%v) = %q, want %q", mb, got, want)
		}
	}
}

func excerpt(s, around string) string {
	i := strings.Index(s, around)
	if i < 0 {
		return "(not present)"
	}
	end := i + 200
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

// The stylesheet rides inside the page rather than arriving by a second
// request. Over a tailnet that can stall mid-load, a linked stylesheet
// rendered unstyled HTML whenever its fetch was the one to fail.
func TestStylesAreInlined(t *testing.T) {
	index, err := Index("host", "https://host/otata", []artifact.Record{rec()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app, err := App(rec(), "https://host/otata", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index": string(index), "app": string(app)} {
		if strings.Contains(body, `rel="stylesheet"`) {
			t.Errorf("the %s page links an external stylesheet", name)
		}
		// A rule the sheet actually contains, so an empty <style> cannot pass.
		if !strings.Contains(body, "a.install") {
			t.Errorf("the %s page does not carry the styles", name)
		}
	}
}

// The card head is the index's only navigation to an app's page, so it must
// be a link. A first build must not be, since its page does not exist yet.
func TestCardHeadLinksTheAppPage(t *testing.T) {
	out, err := Index("host", "https://host/otata", []artifact.Record{rec()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<a class="head" href="https://host/otata/myapp/">`) {
		t.Error("the card head does not link the app page absolutely")
	}
	building := map[string]artifact.Building{"newapp": {Slug: "newapp", Started: time.Now()}}
	first, err := Index("host", "https://host/otata", nil, building)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), `<a class="head" href=`) {
		t.Error("a first build links a page that does not exist yet")
	}
}

// Every src and href on a page is absolute, built from the base URL as the
// manifest's URLs are. Tailscale serves its mount path with and without the
// trailing slash and never redirects, so from a hand-typed URL missing the
// slash a relative reference resolves one level too high, off the mount
// entirely: it served unstyled pages while the stylesheet was a link, and
// broken icons after that.
func TestPagesCarryNoRelativeURLs(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="([^"]*)"`)
	building := map[string]artifact.Building{"other": {Slug: "other", Started: time.Now()}}
	index, err := Index("host", "https://host/otata", []artifact.Record{rec()}, building)
	if err != nil {
		t.Fatal(err)
	}
	app, err := App(rec(), "https://host/otata", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, page := range map[string]string{"index": string(index), "app": string(app)} {
		for _, m := range ref.FindAllStringSubmatch(page, -1) {
			if !strings.HasPrefix(m[1], "https://") && !strings.HasPrefix(m[1], "itms-services://") {
				t.Errorf("the %s page carries a relative reference %s", name, m[0])
			}
		}
	}
}

// latch identifies the install-latch script, as distinct from the age script
// that every non-empty page carries.
const latch = `<script id="latch">`

// The script exists to stop a second tap restarting an install already running,
// so it belongs exactly where a tappable link does, and nowhere else, because
// a page with nothing to install has nothing to latch.
func TestInstallLatchShipsOnlyWithAnInstallLink(t *testing.T) {
	now := time.Now()
	building := map[string]artifact.Building{"myapp": {Slug: "myapp", Started: now.Add(-time.Minute)}}

	ready, _ := Index("host", "https://host/otata", []artifact.Record{rec()}, nil)
	if !strings.Contains(string(ready), latch) {
		t.Error("no latch on a page that offers an install")
	}
	mid, _ := Index("host", "https://host/otata", []artifact.Record{rec()}, building)
	if strings.Contains(string(mid), latch) {
		t.Errorf("latched a link that is not rendered while building:\n%s", excerpt(string(mid), latch))
	}
}

// A store mid-build on one app and ready on another still needs the script.
func TestInstallLatchSurvivesAMixedIndex(t *testing.T) {
	now := time.Now()
	other := rec()
	other.Slug, other.Title = "other", "Other"
	building := map[string]artifact.Building{"myapp": {Slug: "myapp", Started: now.Add(-time.Minute)}}

	out, _ := Index("host", "https://host/otata", []artifact.Record{rec(), other}, building)
	if !strings.Contains(string(out), latch) {
		t.Error("one app building suppressed the latch for another that is installable")
	}
}

// The app page is the bookmarkable surface and carries its own link.
func TestAppPageLatchesItsOwnLink(t *testing.T) {
	now := time.Now()
	ready, _ := App(rec(), "https://host/otata", nil)
	if !strings.Contains(string(ready), latch) {
		t.Error("no latch on the app page")
	}
	b := artifact.Building{Slug: "myapp", Started: now.Add(-time.Minute)}
	mid, _ := App(rec(), "https://host/otata", &b)
	if strings.Contains(string(mid), latch) {
		t.Error("latched the app page while it was building")
	}
}
