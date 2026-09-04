package app

import (
	"net/url"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/render"
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

// hostOf is the host the index page names in its masthead, port included.
// A base URL was validated when it was set, so an unparseable one is an
// empty masthead, not a failed reindex.
func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
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
	if err := a.Store.WriteFile(a.Store.IndexPath(), page); err != nil {
		return err
	}
	// Per-app pages restate the build marker, so a bookmarked link is as honest
	// as the index. Manifests are regenerated here too. They embed the base URL,
	// so a new transport or serve path would otherwise leave every app pointing
	// at a URL that no longer resolves. Only a platform that installs from a
	// manifest gets one; an Android page links the payload itself.
	for _, r := range records {
		if r.Platform.InstallsFromManifest() {
			if err := a.Store.WriteFile(a.Store.ManifestPath(r.Slug), Manifest(r, baseURL)); err != nil {
				return err
			}
		}
		var b *artifact.Building
		if m, ok := building[r.Slug]; ok {
			b = &m
		}
		appPage, err := render.App(r, baseURL, b)
		if err != nil {
			return err
		}
		if err := a.Store.WriteFile(a.Store.AppIndexPath(r.Slug), appPage); err != nil {
			return err
		}
	}
	return nil
}
