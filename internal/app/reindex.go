package app

import (
	"net/url"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/render"
)

// IncomingPrefix is the path prefix requests carry when they reach the
// server, straight from the transport so the two can't disagree.
func (a *App) IncomingPrefix() string {
	if tr := a.selectTransport(); tr != nil {
		return tr.IncomingPrefix()
	}
	return ""
}

// hostOf is the host:port the index masthead shows, or "" for a bad URL.
func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// Reindex regenerates every page and manifest from the records and build markers.
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
	// Manifests embed the base URL, so they're rewritten with the pages.
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
