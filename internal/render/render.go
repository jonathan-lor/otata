// Package render writes the install pages.
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

// tpl holds the page templates and the stylesheet, which pages inline so a
// stalled second request can't leave them unstyled.
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
	// Platform selects the platform icon and bundle/package label.
	Platform   artifact.Platform
	InstallURL template.URL
	// Built is when the build happened. The page's script works out how long ago.
	Built    stamp
	Building bool
	Since    stamp // when the in-flight build started; meaningful only if Building

	// PageURL, IconURL and IndexURL are absolute.
	PageURL, IconURL, IndexURL string
}

type pendingView struct {
	Slug, Config string
	Since        stamp
}

// stamp is an instant as the page needs it: ISO for the script, text for the reader.
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
	// AnyInstallable means some row has an install link, so the latch script ships.
	AnyInstallable bool
	// Empty also counts first builds, which have no record yet.
	Empty bool
}

// Index renders the list of everything published. It takes no clock, since
// the page outlives the moment it's written.
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
		view.Apps = append(view.Apps, a)
	}
	// Show first builds, which have no record yet.
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

// App renders one app's page.
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
		Platform: r.Platform,
		Built:    stampOf(r.BuiltAt),
		Size:     Size(r.SizeMB()),
		// Absolute, so they survive manual proxies. Reindex rewrites them.
		PageURL:  appBase + "/",
		IconURL:  appBase + "/" + r.IconFile(),
		IndexURL: base + "/",
	}
	// iOS installs from a manifest through itms-services, Android from the
	// payload. template.URL keeps html/template from replacing itms-services://
	// with #ZgotmplZ.
	if r.Platform.InstallsFromManifest() {
		manifest := fmt.Sprintf("%s/manifest.plist?v=%s", appBase, r.CacheKey())
		v.InstallURL = template.URL("itms-services://?action=download-manifest&url=" + escape(manifest))
	} else {
		v.InstallURL = template.URL(fmt.Sprintf("%s/%s?v=%s", appBase, r.PayloadName, r.CacheKey()))
	}
	return v
}

func execute(name string, data any) ([]byte, error) {
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, name, data); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// escape encodes the manifest URL for the itms-services query: QueryEscape,
// but with %20 for a space, since iOS reads a + back as a plus.
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Size formats a payload size, in KB below 1 MB.
func Size(mb float64) string {
	if mb < 0.95 {
		return fmt.Sprintf("%d KB", int(math.Round(mb*1024)))
	}
	return fmt.Sprintf("%.1f MB", mb)
}

// Age says how long ago t was, for the CLI. templates/agescript.html words it
// the same way on the pages, so change both together.
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
