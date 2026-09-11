package builder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Runs against a real Android project named by OTATA_TEST_GRADLE, skipped
// otherwise so the suite stays hermetic. It detects, lists, resolves the
// default selection with OTATA_TEST_FLAVOR when the project asks for one,
// and builds a Debug APK into a temporary work directory, which is the one
// proof the init script and the metadata reading work against the
// project's own Gradle and plugin versions.
func TestBuildsRealGradleProject(t *testing.T) {
	dir := os.Getenv("OTATA_TEST_GRADLE")
	if dir == "" {
		t.Skip("OTATA_TEST_GRADLE not set")
	}
	g := &Gradle{}
	root, err := g.Detect(dir)
	if err != nil {
		t.Fatalf("did not detect the project: %v", err)
	}
	work := t.TempDir()
	res, err := g.Build(context.Background(), Options{
		Container: root, Config: "Debug", Flavor: os.Getenv("OTATA_TEST_FLAVOR"), Work: work,
		Log: func(line string) { t.Log(line) },
	})
	if err != nil {
		t.Fatalf("build: %v (log at %s)", err, res.LogPath)
	}
	info, err := os.Stat(res.PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("built %s (%d bytes), log at %s", filepath.Base(res.PayloadPath), info.Size(), res.LogPath)
}
