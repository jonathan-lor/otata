package app

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/server"
	"golang.org/x/net/html"
)

// Reindexing must update install links to the current base URL for Tailscale
// and for manual proxies that strip or forward the configured prefix.
func TestReindexedInstallURLsFollowTransportRouting(t *testing.T) {
	t.Setenv("OTATA_PATH", "/obsolete-service-path")
	for _, tc := range []struct {
		name, transport, base, proxyStrip string
		keep                              bool
	}{
		{"tailscale", "tailscale", "https://otata.example.ts.net", "", false},
		{"manual root", "manual", "https://builds.example.com", "", true},
		{"manual stripping", "manual", "https://tools.example.com/otata", "/otata", false},
		{"manual forwarding", "manual", "https://tools.example.com/otata", "", true},
		{"manual nested", "manual", "https://tools.example.com/tools/builds", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := freshApp(t)
			data, err := json.Marshal(map[string]any{
				"transport": tc.transport, "serve_path": "/old",
				"manual": config.Manual{BaseURL: tc.base, KeepPrefix: tc.keep},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config.Path(a.Root), data, 0600); err != nil {
				t.Fatal(err)
			}
			a.Config, err = config.Load(a.Root)
			if err != nil {
				t.Fatal(err)
			}
			// App slugs are independent of the transport; "otata" must route to its app.
			for _, r := range []artifact.Record{
				{Slug: "otata", Platform: artifact.IOS, PayloadName: "App.ipa", BuiltAt: time.Now(), HasIcon: true},
				{Slug: "droid", Platform: artifact.Android, PayloadName: "App.apk", BuiltAt: time.Now()},
			} {
				if err := a.Store.PutRecord(r); err != nil {
					t.Fatal(err)
				}
				if err := a.Store.WriteFile(a.Store.PayloadPath(r.Slug, r.PayloadName), []byte(r.Slug)); err != nil {
					t.Fatal(err)
				}
				if r.HasIcon {
					if err := a.Store.WriteFile(a.Store.IconPath(r.Slug, r.IconFile()), []byte("icon")); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, base := range []string{"https://old.example.com/old", tc.base} {
				if err := a.Reindex(base); err != nil {
					t.Fatal(err)
				}
			}
			srv, err := server.New(a.Store.Public(), a.IncomingPrefix(), a.RootDigest(), log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			get := func(target string) string {
				t.Helper()
				if !strings.HasPrefix(target, tc.base+"/") {
					t.Fatalf("generated URL uses the wrong base: %s", target)
				}
				req := httptest.NewRequest(http.MethodGet, target, nil)
				req.URL.Path = strings.TrimPrefix(req.URL.Path, tc.proxyStrip)
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s returned %d", target, rec.Code)
				}
				return rec.Body.String()
			}
			get(tc.base + "/")
			for _, slug := range []string{"otata", "droid"} {
				page, err := html.Parse(strings.NewReader(get(tc.base + "/" + slug + "/")))
				if err != nil {
					t.Fatal(err)
				}
				installFound := false
				for n := range page.Descendants() {
					for _, attr := range n.Attr {
						if attr.Key != "href" && attr.Key != "src" {
							continue
						}
						u, err := url.Parse(attr.Val)
						if err != nil {
							t.Fatal(err)
						}
						if u.Scheme == "https" {
							body := get(u.String())
							if strings.HasSuffix(u.Path, ".apk") {
								installFound = body == "droid"
							}
						}
						if u.Scheme == "itms-services" {
							manifest := xml.NewDecoder(strings.NewReader(get(u.Query().Get("url"))))
							for {
								token, err := manifest.Token()
								if err == io.EOF {
									break
								}
								if err != nil {
									t.Fatal(err)
								}
								if text, ok := token.(xml.CharData); ok && strings.HasPrefix(string(text), "https://") {
									body := get(string(text))
									if body == "otata" {
										installFound = true
									}
								}
							}
						}
					}
				}
				if !installFound {
					t.Fatalf("%s has no working install link", slug)
				}
			}
			if tc.transport == "tailscale" {
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/otata/otata/", nil))
				if rec.Code != http.StatusNotFound {
					t.Fatalf("obsolete Tailscale path is still served: %d", rec.Code)
				}
			}
		})
	}
}
