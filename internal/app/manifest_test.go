package app

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// decodeManifest parses the generated plist the way iOS would, via plutil.
// Parsing rather than substring-matching is the point because escaped hostile text
// legitimately appears inside a title node, and only the parsed structure shows
// whether it stayed there. plutil is macOS's, so a machine without it skips
// rather than fails; the suite stays hermetic.
func decodeManifest(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil is not installed")
	}
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(raw)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("plutil rejected the manifest (%v): %s\n%s", err, errBuf.String(), raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("could not parse decoded manifest: %v", err)
	}
	return parsed
}

// A title is arbitrary text from CFBundleDisplayName. Unescaped, an ampersand
// alone makes the manifest unparseable, so every install of that app then
// fails on the phone with a generic error while the tool reports success, and
// a crafted title injects an entire extra install item.
func TestManifestSurvivesHostileValues(t *testing.T) {
	hostile := []string{
		`A & B`,
		`Notes <Beta>`,
		`He said "hi" & 'bye'`,
		`X</string></dict></dict><dict><key>assets</key><array><dict><key>kind</key>` +
			`<string>software-package</string><key>url</key><string>https://evil.example/m.ipa</string>` +
			`</dict></array><key>metadata</key><dict><key>title</key><string>Y`,
	}
	for _, title := range hostile {
		rec := artifact.Record{
			Slug: "app", Title: title, BundleID: "com.x.y", Version: "1.0", Build: "1",
			PayloadName: "App.ipa", BuiltAt: time.Now(), HasIcon: true, Commit: "abc1234",
		}
		parsed := decodeManifest(t, Manifest(rec, "https://host/otata"))

		items, _ := parsed["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("title %q produced %d install items, want 1; structure was injected", title, len(items))
		}
		item, _ := items[0].(map[string]any)
		meta, _ := item["metadata"].(map[string]any)
		if got := meta["title"]; got != title {
			t.Errorf("title round-tripped as %q, want %q", got, title)
		}
		assets, _ := item["assets"].([]any)
		for _, a := range assets {
			url, _ := a.(map[string]any)["url"].(string)
			if !strings.HasPrefix(url, "https://host/otata/") {
				t.Errorf("title %q produced an off-host asset URL: %s", title, url)
			}
		}
	}
}

// The manifest links the icon under the name the record gives it, and links
// none for a build that shipped none.
func TestManifestLinksTheIconByName(t *testing.T) {
	rec := artifact.Record{Slug: "app", Title: "App", BundleID: "com.x.y", Version: "1", Build: "1",
		PayloadName: "App.ipa", BuiltAt: time.Now(), HasIcon: true, IconName: "icon.webp"}
	if !strings.Contains(string(Manifest(rec, "https://host/otata")), "https://host/otata/app/icon.webp") {
		t.Error("the icon is not linked under its name")
	}
	rec.HasIcon, rec.IconName = false, ""
	if strings.Contains(string(Manifest(rec, "https://host/otata")), "icon") {
		t.Error("a build with no icon still links one")
	}
}

// The ordinary case that was broken before escaping: a real app name.
func TestManifestForEverydayName(t *testing.T) {
	rec := artifact.Record{
		Slug: "notes", Title: "Notes & Tasks", BundleID: "com.x.notes",
		Version: "2.1", Build: "44", PayloadName: "Notes.ipa", BuiltAt: time.Now(),
	}
	parsed := decodeManifest(t, Manifest(rec, "https://host/otata"))
	item := parsed["items"].([]any)[0].(map[string]any)
	if got := item["metadata"].(map[string]any)["title"]; got != "Notes & Tasks" {
		t.Errorf("title = %q", got)
	}
}
