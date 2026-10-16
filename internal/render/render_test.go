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

// html/template replaces unknown URL schemes with #ZgotmplZ.
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
		if !strings.Contains(stripScripts(body), "Android") {
			t.Errorf("the %s page does not name the platform", name)
		}
	}
}

func TestIconIsLinkedUnderItsOwnName(t *testing.T) {
	webp := rec()
	webp.Platform, webp.PayloadName, webp.IconName = artifact.Android, "MyApp.apk", "icon.webp"
	legacy := rec()
	legacy.Slug = "legacy"
	out, err := Index("host", "https://host/otata", []artifact.Record{webp, legacy}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`src="https://host/otata/myapp/icon.webp"`, `src="https://host/otata/legacy/icon.png"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("index lacks %s:\n%s", want, excerpt(string(out), "icon"))
		}
	}
	app, err := App(webp, "https://host/otata", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `src="https://host/otata/myapp/icon.webp"`) {
		t.Error("the app page does not link the WebP icon under its name")
	}
}

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
	// The script's own source has the words, so check the page without it.
	markup := stripScripts(body)
	for _, phrase := range []string{" ago", "just now", "Building for"} {
		if strings.Contains(markup, phrase) {
			t.Errorf("a relative age %q was rendered into a file that outlives it", phrase)
		}
	}
}

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
	// The phone computes the elapsed time from the start, as it does the age.
	since := `<time datetime="` + building["myapp"].Started.UTC().Format(time.RFC3339) + `" data-since>`
	if !strings.Contains(body, since) {
		t.Errorf("build not reported as in progress with its start instant:\n%s", excerpt(body, "Building"))
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("page does not refresh while a build is running")
	}
}

func TestFirstBuildIsVisible(t *testing.T) {
	building := map[string]artifact.Building{
		"newapp": {Slug: "newapp", Started: time.Now().Add(-5 * time.Second), Config: "Debug"},
	}
	out, _ := Index("host", "https://host/otata", nil, building)
	body := string(out)
	if !strings.Contains(body, "newapp") {
		t.Error("in-flight first build is invisible")
	}
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

// Links are absolute, so they still resolve when the page's URL lacks its
// trailing slash.
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

// latch is the install-latch script's tag.
const latch = `<script id="latch">`

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
