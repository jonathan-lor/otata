package storage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Every path the app writes or serves is spelled here and nowhere else, all
// of it under the root, and a slug that fails validation yields no path at
// all, so a caller that forgot to validate cannot reach outside by accident.
func TestLayoutIsUnderTheRootAndRefusesBadSlugs(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"index":         store.IndexPath(),
		"app index":     store.AppIndexPath("app"),
		"manifest":      store.ManifestPath("app"),
		"icon":          store.IconPath("app"),
		"payload":       store.PayloadPath("app", "App.ipa"),
		"build dir":     store.BuildDir("app"),
		"tmp file":      store.TmpFile("icon-app.png"),
		"server log":    store.ServerLog(),
		"staged binary": store.StagedBinary(),
	}
	for name, p := range paths {
		rel, err := filepath.Rel(root, p)
		if p == "" || err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("%s = %q, want a path under %s", name, p, root)
		}
	}
	for name, p := range map[string]string{"app index": paths["app index"], "manifest": paths["manifest"], "icon": paths["icon"], "payload": paths["payload"]} {
		if filepath.Dir(p) != store.AppDir("app") {
			t.Errorf("%s = %q, want a file inside the app's directory", name, p)
		}
	}
	for _, bad := range []string{"..", "a/b", "", "../app"} {
		if store.AppIndexPath(bad) != "" || store.ManifestPath(bad) != "" || store.IconPath(bad) != "" ||
			store.PayloadPath(bad, "App.ipa") != "" || store.BuildDir(bad) != "" {
			t.Errorf("slug %q yielded a path", bad)
		}
	}
	// A payload name comes out of a record, which a hand can edit.
	for _, bad := range []string{"../escape.ipa", "sub/App.ipa", ""} {
		if store.PayloadPath("app", bad) != "" {
			t.Errorf("payload name %q yielded a path", bad)
		}
	}
}

// A slug becomes both a path component and a URL segment. An unvalidated one
// let `otata forget ..` run RemoveAll on the entire store and return success.
func TestValidateSlug(t *testing.T) {
	valid := []string{"a", "myapp", "my-app", "app2", "a-b-c-1"}
	for _, s := range valid {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{
		"", "..", ".", "/", "../x", "a/b", "a\\b", "A", "My App", "-lead",
		"trail-", "a..b", "app.json", "x\x00y", "app/../..",
	}
	for _, s := range invalid {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("ValidateSlug(%q) = nil, want an error", s)
		}
	}
}

// Remove is the destructive path. It must refuse before RemoveAll.
func TestRemoveRefusesTraversal(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(base, "root"))
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"..", "../victim", "../../", "."} {
		if err := store.Remove(slug); err == nil {
			t.Errorf("Remove(%q) succeeded; it should refuse", slug)
		}
	}
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Fatal("a sibling directory was deleted by a traversal slug")
	}
	if _, err := os.Stat(store.Public()); err != nil {
		t.Fatal("the store itself was deleted")
	}
}

func TestPutRecordRefusesBadSlug(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRecord(artifact.Record{Slug: "../escape"}); err == nil {
		t.Error("PutRecord accepted a traversal slug")
	}
	if _, _, err := store.ClaimBuilding(artifact.Building{Slug: "../escape"}); err == nil {
		t.Error("ClaimBuilding accepted a traversal slug")
	}
}

// Records must survive a corrupt neighbor.
func TestRecordsSkipsCorrupt(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good := artifact.Record{Slug: "good", Title: "Good", BuiltAt: time.Now()}
	if err := store.PutRecord(good); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.State(), "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Slug != "good" {
		t.Errorf("got %d records, want just the healthy one", len(records))
	}
}

// Newest first. After publishing, the app you probably want is on top.
func TestRecordsSortNewestFirst(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, slug := range []string{"old", "mid", "new"} {
		if err := store.PutRecord(artifact.Record{
			Slug: slug, BuiltAt: now.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	records, _ := store.Records()
	want := []string{"new", "mid", "old"}
	for i, r := range records {
		if r.Slug != want[i] {
			t.Errorf("position %d = %q, want %q", i, r.Slug, want[i])
		}
	}
}

// The atomicity claim: a reader must never see a partial file.
func TestWriteFileIsAtomic(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(store.Public(), "app", "payload.bin")
	if err := store.WriteFile(dest, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteFile(dest, []byte("second-and-longer")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second-and-longer" {
		t.Errorf("got %q", got)
	}
	// No staging files may survive a completed write.
	entries, _ := os.ReadDir(store.Tmp())
	if len(entries) != 0 {
		t.Errorf("%d temp files left behind", len(entries))
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644; a served file must not be 0600", info.Mode().Perm())
	}
}

// Pruning removes the payloads a rename left behind and nothing else: not the
// pages beside them, and not a file whose extension is another platform's,
// since the extension is what marks a payload as this platform's.
func TestPruneStalePayloadsRemovesOnlyThisPlatformsOldPayloads(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := store.AppDir("app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"New.ipa", "Old.ipa", "Older.ipa", "index.html", "manifest.plist", "icon.png", "Other.apk"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PruneStalePayloads("app", "New.ipa", ".ipa"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{"New.ipa", "Other.apk", "icon.png", "index.html", "manifest.plist"}
	if !slices.Equal(left, want) {
		t.Errorf("after pruning: %v, want %v", left, want)
	}
	if err := store.PruneStalePayloads("../x", "a", ".ipa"); err == nil {
		t.Error("PruneStalePayloads accepted a traversal slug")
	}
}

func TestClearBuildingIsIdempotent(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClearBuilding("nothing"); err != nil {
		t.Errorf("clearing an absent marker returned %v", err)
	}
}

// The marker is the lock between two publishes of one slug. Claiming must be
// exclusive, report who holds it, and leave the holder's marker untouched.
func TestClaimBuildingIsExclusive(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := artifact.Building{Slug: "app", Started: time.Now(), PID: 111, Config: "Debug"}
	existing, claimed, err := store.ClaimBuilding(first)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	if existing.PID != 0 {
		t.Errorf("a successful claim reported an existing marker: %+v", existing)
	}

	second := artifact.Building{Slug: "app", Started: time.Now(), PID: 222, Config: "Release"}
	existing, claimed, err = store.ClaimBuilding(second)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("second claim succeeded while the first marker exists")
	}
	if existing.PID != 111 || existing.Config != "Debug" || existing.Slug != "app" {
		t.Errorf("existing marker = %+v, want the first publish's", existing)
	}
	// The holder's marker was not overwritten.
	markers, _ := store.Building()
	if markers["app"].PID != 111 {
		t.Errorf("marker on disk = %+v, want pid 111", markers["app"])
	}

	// Once cleared, the slug can be claimed again.
	if err := store.ClearBuilding("app"); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err = store.ClaimBuilding(second); err != nil || !claimed {
		t.Errorf("claim after clear: claimed=%v err=%v", claimed, err)
	}
	if _, _, err := store.ClaimBuilding(artifact.Building{Slug: "../x"}); err == nil {
		t.Error("ClaimBuilding accepted a traversal slug")
	}
}
