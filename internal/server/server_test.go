package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer builds a served tree plus a sibling secret that must never be
// reachable, mirroring the real layout where state/ sits beside public/.
func newTestServer(t *testing.T, incomingPrefix string) *Server {
	t.Helper()
	base := t.TempDir()
	public := filepath.Join(base, "public")
	if err := os.MkdirAll(filepath.Join(public, "myapp"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		if err := os.WriteFile(filepath.Join(public, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<h1>apps</h1>")
	write("style.css", "body{}")
	write("myapp/index.html", "<h1>myapp</h1>")
	write("myapp/manifest.plist", "<plist/>")
	write("myapp/MyApp.ipa", strings.Repeat("A", 4096))

	// The thing an attacker wants, one level above the served root.
	if err := os.WriteFile(filepath.Join(base, "secret.json"), []byte(`{"token":"leaked"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(public, incomingPrefix, "test-root", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func get(t *testing.T, s *Server, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

func TestServesPublicPaths(t *testing.T) {
	s := newTestServer(t, "")
	for _, target := range []string{"/", "/style.css", "/myapp/", "/myapp/index.html", "/myapp/manifest.plist", "/myapp/MyApp.ipa"} {
		if resp := get(t, s, target); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: got %d, want 200", target, resp.StatusCode)
		}
	}
}

// The security-critical test. Percent-decoding turns %2F into a separator and
// %2e%2e into "..", so these must be refused after decoding, not before.
func TestRefusesTraversal(t *testing.T) {
	s := newTestServer(t, "")
	attacks := []string{
		"/../secret.json",
		"/../../secret.json",
		"/myapp/../../secret.json",
		"/%2e%2e/secret.json",
		"/%2e%2e%2fsecret.json",
		"/..%2fsecret.json",
		"/..%2F..%2Fsecret.json",
		"/....//secret.json",
		"/%2E%2E%2Fsecret.json",
		"//secret.json",
		"/./../secret.json",
	}
	for _, target := range attacks {
		resp := get(t, s, target)
		if resp.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("%s: SERVED %d bytes, traversal succeeded", target, len(body))
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "leaked") {
			t.Errorf("%s: leaked secret in a %d response", target, resp.StatusCode)
		}
	}
}

// The traversal test above passes even with os.Root replaced by a naive
// os.Open(filepath.Join(...)), because path.Clean collapses ".." before the
// root ever sees it. This is the case only os.Root defends, and without it the
// whole confinement mechanism could be deleted with the suite still green.
func TestRefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	public := filepath.Join(base, "public")
	if err := os.MkdirAll(filepath.Join(public, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"token":"leaked"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Every shape of symlink that could be planted inside the served tree.
	links := map[string]string{
		"abs.json":      secret,              // absolute, outside the root
		"rel.json":      "../secret.json",    // relative escape
		"up":            base,                // directory escape
		"sub/deep.json": "../../secret.json", // escape from a subdirectory
		"hosts":         "/etc/hosts",        // system file
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(public, name)); err != nil {
			t.Fatal(err)
		}
	}

	s, err := New(public, "", "test-root", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, target := range []string{"/abs.json", "/rel.json", "/up/secret.json", "/sub/deep.json", "/hosts"} {
		resp := get(t, s, target)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s: SERVED through a symlink (%d bytes)", target, len(body))
		}
		if strings.Contains(string(body), "leaked") {
			t.Errorf("%s: leaked the secret in a %d response", target, resp.StatusCode)
		}
	}
}

func TestNoDirectoryListing(t *testing.T) {
	// A directory without an index must 404 instead of enumerate its contents.
	base := t.TempDir()
	public := filepath.Join(base, "public")
	if err := os.MkdirAll(filepath.Join(public, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	s2, err := New(public, "", "test-root", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if resp := get(t, s2, "/bare/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bare directory: got %d, want 404", resp.StatusCode)
	}
}

func TestMIMEOverrides(t *testing.T) {
	s := newTestServer(t, "")
	want := map[string]string{
		"/myapp/manifest.plist": "application/xml",
		"/myapp/MyApp.ipa":      "application/octet-stream",
	}
	for target, ct := range want {
		if got := get(t, s, target).Header.Get("Content-Type"); got != ct {
			t.Errorf("%s: Content-Type %q, want %q", target, got, ct)
		}
	}
}

func TestNoStore(t *testing.T) {
	s := newTestServer(t, "")
	if got := get(t, s, "/myapp/MyApp.ipa").Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", got)
	}
}

func TestRangeRequests(t *testing.T) {
	s := newTestServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/myapp/MyApp.ipa", nil)
	req.Header.Set("Range", "bytes=100-199")
	s.ServeHTTP(rec, req)
	resp := rec.Result()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("got %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 100-199/4096" {
		t.Errorf("Content-Range %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 100 {
		t.Errorf("got %d bytes, want 100", len(body))
	}
}

func TestUnsatisfiableRange(t *testing.T) {
	s := newTestServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/myapp/MyApp.ipa", nil)
	req.Header.Set("Range", "bytes=999999-")
	s.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("got %d, want 416", rec.Result().StatusCode)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, "")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/myapp/MyApp.ipa", nil))
	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", rec.Result().StatusCode)
	}
}

// Under a proxy that forwards the path unchanged, every request carries the
// prefix and the server strips exactly it. The bare form is NOT also accepted.
// Tolerating both made an app whose slug equals the prefix unreachable, since
// "/otata/manifest.plist" was stripped to the root and 404'd.
func TestIncomingPrefixIsStrippedExactly(t *testing.T) {
	s := newTestServer(t, "/otata")
	for _, target := range []string{"/otata/", "/otata/style.css", "/otata/myapp/", "/otata/myapp/manifest.plist"} {
		if resp := get(t, s, target); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: got %d, want 200", target, resp.StatusCode)
		}
	}
	// A bare request cannot come from a proxy that forwards the prefix, so it
	// is not ours to answer.
	for _, target := range []string{"/", "/myapp/manifest.plist", "/style.css"} {
		if resp := get(t, s, target); resp.StatusCode == http.StatusOK {
			t.Errorf("%s: served without the prefix the transport forwards", target)
		}
	}
	// Prefix stripping must not become a traversal vector of its own.
	if resp := get(t, s, "/otata/../secret.json"); resp.StatusCode == http.StatusOK {
		t.Error("prefix strip allowed traversal")
	}
}

// An app slugged after the serve path, which is what `otata publish` produces
// from a directory named otata, must be reachable under both contracts.
func TestSlugEqualToPrefixIsReachable(t *testing.T) {
	mk := func(prefix string) *Server {
		base := t.TempDir()
		public := filepath.Join(base, "public")
		if err := os.MkdirAll(filepath.Join(public, "otata"), 0o755); err != nil {
			t.Fatal(err)
		}
		for p, body := range map[string]string{
			"index.html": "ROOT", "otata/index.html": "APP", "otata/manifest.plist": "MANIFEST",
		} {
			if err := os.WriteFile(filepath.Join(public, p), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		s, err := New(public, prefix, "test-root", log.New(io.Discard, "", 0))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	check := func(s *Server, target, want string) {
		t.Helper()
		resp := get(t, s, target)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Errorf("%s: got %d %q, want 200 %q", target, resp.StatusCode, body, want)
		}
	}
	// Tailscale: it strips the mount path, so the server sees bare paths.
	bare := mk("")
	check(bare, "/", "ROOT")
	check(bare, "/otata/", "APP")
	check(bare, "/otata/manifest.plist", "MANIFEST")
	// A forwarding proxy: every path carries /otata once more.
	prefixed := mk("/otata")
	check(prefixed, "/otata/", "ROOT")
	check(prefixed, "/otata/otata/", "APP")
	check(prefixed, "/otata/otata/manifest.plist", "MANIFEST")
}

// A directory without its trailing slash redirects rather than serving the
// index in place. The pages link icons and the index relatively, and from
// "/myapp" those resolve one level too high. The Location must be relative:
// behind a stripping proxy the server does not know the absolute path the
// phone used, and "/myapp/" would point outside the mount.
func TestDirectoryWithoutSlashRedirectsRelatively(t *testing.T) {
	s := newTestServer(t, "")
	resp := get(t, s, "/myapp")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("got %d, want 301", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "myapp/" {
		t.Errorf("Location = %q, want relative %q", got, "myapp/")
	}
	// The query survives the redirect.
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/myapp?v=1", nil))
	if got := rec.Header().Get("Location"); got != "myapp/?v=1" {
		t.Errorf("Location = %q, want %q", got, "myapp/?v=1")
	}
	// The root and a file are untouched.
	if resp := get(t, s, "/"); resp.StatusCode != http.StatusOK {
		t.Errorf("/: got %d", resp.StatusCode)
	}
	if resp := get(t, s, "/myapp/MyApp.ipa"); resp.StatusCode != http.StatusOK {
		t.Errorf("file: got %d", resp.StatusCode)
	}
	// Under a forwarded prefix the bare mount path redirects to itself + "/".
	p := newTestServer(t, "/otata")
	if got := get(t, p, "/otata").Header.Get("Location"); got != "otata/" {
		t.Errorf("prefixed mount Location = %q, want %q", got, "otata/")
	}
}

// Identity on every response, including errors: port occupancy is not
// process identity, and an otata server is not necessarily the one for this
// root. The version rides with it so a stale running server is visible after
// an upgrade replaces the binary on disk.
func TestIdentityHeadersOnEveryResponse(t *testing.T) {
	s := newTestServer(t, "")
	for _, target := range []string{"/", "/missing", "/../secret.json"} {
		resp := get(t, s, target)
		if resp.Header.Get("X-Otata") != "1" || resp.Header.Get("X-Otata-Root") != "test-root" || resp.Header.Get("X-Otata-Pid") == "" {
			t.Errorf("%s (%d): identity headers missing: %v", target, resp.StatusCode, resp.Header)
		}
		if resp.Header.Get("X-Otata-Version") == "" {
			t.Errorf("%s (%d): no version header", target, resp.StatusCode)
		}
	}
}
