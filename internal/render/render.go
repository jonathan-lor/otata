// Package render writes the install surface.
package render

import (
	"embed"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

//go:embed templates
var files embed.FS

// tpl holds the page templates plus the stylesheet, parsed once as the
// "style" template both pages inline. Inlined instead of linked on purpose because
// the pages will usually be fetched over a tailnet that can stall mid-load, and a
// stylesheet arriving by a second request can render unstyled HTML whenever
// that request was the one to fail.
var tpl = func() *template.Template {
	t := template.Must(template.ParseFS(files, "templates/*.html"))
	css, err := files.ReadFile("templates/style.css")
	if err != nil {
		panic(err)
	}
	return template.Must(t.New("style").Parse("<style>\n" + string(css) + "</style>"))
}()

type appView struct {
	Slug, Title, Version, Build, Config string
	BundleID, Commit, Branch            string
	Dirty, HasIcon                      bool
	Size                                string
	// Platform selects the per-platform parts of a page: the install note,
	// and which of the two link shapes InstallURL took. PlatformLabel is how
	// the page names it.
	Platform      artifact.Platform
	PlatformLabel string
	InstallURL    template.URL
	// Built is the build's timestamp, as the stamp the HTML carries. The page
	// is a static file written at publish and read at any later time, so it
	// can say WHEN the build happened but never how long ago that was.
	// That is computed on the phone by the age script from this value.
	Built    stamp
	Building bool
	Since    stamp // when the in-flight build started; meaningful only if Building

	// PageURL is this app's page, IconURL its icon, IndexURL the index. All
	// absolute, for the reason on viewFor.
	PageURL, IconURL, IndexURL string
}

type pendingView struct {
	Slug, Config string
	Since        stamp
}

// stamp is one instant in the two forms a page needs: machine-readable for the
// script that turns it into "40 seconds ago", and an absolute time for the
// reader when script does not run, which is true, just less convenient.
type stamp struct {
	ISO  string // RFC 3339, for the datetime attribute; empty if unknown
	Text string // what the HTML says before script touches it
}

func stampOf(t time.Time) stamp {
	if t.IsZero() {
		return stamp{Text: "unknown"}
	}
	return stamp{ISO: t.UTC().Format(time.RFC3339), Text: t.Local().Format("2006-01-02 15:04 MST")}
}

type indexView struct {
	Host         string
	Apps         []appView
	BuildingOnly []pendingView
	AnyBuilding  bool
	// AnyIOS and AnyAndroid select which install notes the page carries:
	// each platform's advice is noise on a page with none of its apps.
	AnyIOS, AnyAndroid bool
	// AnyInstallable is whether any row carries an install link, which is not the
	// negation of AnyBuilding. A store can hold one app mid-build and another
	// ready to tap. The latch script ships only where there is a link to latch.
	AnyInstallable bool
	// Empty is computed rather than derived from Apps in the template because a first
	// build has no record yet, and saying "nothing published" beside a running
	// build is exactly wrong at the moment someone is most likely watching.
	Empty bool
}

// Index renders the list of everything published.
//
// It takes no clock on purpose. The output is a file that outlives the moment
// it was written, so nothing in it may depend on when that was.
func Index(host, baseURL string, records []artifact.Record, building map[string]artifact.Building) ([]byte, error) {
	view := indexView{Host: host}
	published := map[string]bool{}
	for _, r := range records {
		published[r.Slug] = true
		a := viewFor(r, baseURL)
		if b, ok := building[r.Slug]; ok {
			a.Building = true
			a.Since = stampOf(b.Started)
			view.AnyBuilding = true
		}
		if !a.Building {
			view.AnyInstallable = true
		}
		switch r.Platform {
		case artifact.Android:
			view.AnyAndroid = true
		default:
			view.AnyIOS = true
		}
		view.Apps = append(view.Apps, a)
	}
	// A first build has no record yet, so it would otherwise be invisible
	// exactly when someone is waiting for it.
	for slug, b := range building {
		if published[slug] {
			continue
		}
		view.BuildingOnly = append(view.BuildingOnly, pendingView{
			Slug: slug, Config: b.Config, Since: stampOf(b.Started),
		})
		view.AnyBuilding = true
	}
	view.Empty = len(view.Apps) == 0 && len(view.BuildingOnly) == 0
	return execute("index.html", view)
}

// App renders one app's page, for a link that can be bookmarked.
func App(r artifact.Record, baseURL string, building *artifact.Building) ([]byte, error) {
	a := viewFor(r, baseURL)
	if building != nil {
		a.Building = true
		a.Since = stampOf(building.Started)
	}
	return execute("app.html", a)
}

func viewFor(r artifact.Record, baseURL string) appView {
	base := strings.TrimSuffix(baseURL, "/")
	appBase := base + "/" + r.Slug
	v := appView{
		Slug: r.Slug, Title: r.Title, Version: r.Version, Build: r.Build,
		Config: r.Config, BundleID: r.BundleID, Commit: r.Commit, Branch: r.Branch,
		Dirty: r.Dirty, HasIcon: r.HasIcon,
		Platform: r.Platform, PlatformLabel: platformLabel(r.Platform),
		Built: stampOf(r.BuiltAt),
		Size:  Size(r.SizeMB()),
		// Absolute like the manifest's URLs, and regenerated with them when the
		// base changes. Tailscale serves its mount path with and without the
		// trailing slash and never redirects, so on a hand-typed URL missing the
		// slash a relative reference resolves one level too high, off the mount
		// entirely: unstyled pages while the stylesheet was a link, broken
		// icons after.
		PageURL:  appBase + "/",
		IconURL:  appBase + "/" + r.IconFile(),
		IndexURL: base + "/",
	}
	// How the phone installs is the platform's. iOS is handed a manifest through
	// the itms-services scheme; Android fetches the payload itself, so the link
	// is the payload, with the same cache key so a new build is never served
	// from cache. template.URL is required either way: html/template rejects
	// unknown schemes in an href and would replace itms-services:// with
	// #ZgotmplZ, breaking every install link silently.
	if r.Platform.InstallsFromManifest() {
		manifest := fmt.Sprintf("%s/manifest.plist?v=%s", appBase, r.CacheKey())
		v.InstallURL = template.URL("itms-services://?action=download-manifest&url=" + escape(manifest))
	} else {
		v.InstallURL = template.URL(fmt.Sprintf("%s/%s?v=%s", appBase, r.PayloadName, r.CacheKey()))
	}
	return v
}

// platformLabel is how a page names a platform.
func platformLabel(p artifact.Platform) string {
	if p == artifact.Android {
		return "Android"
	}
	return "iOS"
}

func execute(name string, data any) ([]byte, error) {
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, name, data); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// escape percent-encodes everything outside the RFC 3986 unreserved set. The
// manifest URL rides as a query-parameter value inside the href, so '/', ':',
// '?' and '&' must all be encoded or iOS cannot follow the link. QueryEscape
// does exactly that except for a space, which it writes as '+', a form the
// receiving side would read back as a plus.
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Size states a payload size in units that keep it visible.
func Size(mb float64) string {
	if mb < 0.95 {
		return fmt.Sprintf("%d KB", int(math.Round(mb*1024)))
	}
	return fmt.Sprintf("%.1f MB", mb)
}

// Age states how long ago a build happened. It serves the CLI, where the output is read the moment
// it's written. The pages get the same wording from the age script in templates/agescript.html,
// which must be kept in step with this. The phone is the one place the two could be compared.
func Age(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := max(now.Sub(t), 0)
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < 2*time.Minute:
		return "a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(math.Floor(d.Hours()/24)))
	}
}
