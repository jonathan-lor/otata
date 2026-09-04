package builder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// For is the one place a platform's builders are chosen, so its table is
// pinned: the two iOS modes by name, a typo refused, and a platform with no
// builder refused rather than handed the wrong one.
func TestForSelectsTheBuilderByPlatformAndMode(t *testing.T) {
	if b, err := For(artifact.IOS, ""); err != nil {
		t.Errorf("default mode: %v", err)
	} else if _, ok := b.(*XcodeBuild); !ok {
		t.Errorf("default mode is %T, want *XcodeBuild", b)
	}
	if b, err := For(artifact.IOS, "build"); err != nil {
		t.Errorf("build mode: %v", err)
	} else if _, ok := b.(*XcodeBuild); !ok {
		t.Errorf("build mode is %T, want *XcodeBuild", b)
	}
	if b, err := For(artifact.IOS, "archive"); err != nil {
		t.Errorf("archive mode: %v", err)
	} else if _, ok := b.(*Xcode); !ok {
		t.Errorf("archive mode is %T, want *Xcode", b)
	}
	if _, err := For(artifact.IOS, "bogus"); err == nil {
		t.Error("an unknown mode was accepted")
	}
	if _, err := For(artifact.Android, ""); err == nil {
		t.Error("a platform with no builder was handed one")
	}
}

// Detect answers with the container or with why there is none; a caller
// never has to know what an Xcode project looks like to report the miss.
func TestDetectNamesWhatItLookedFor(t *testing.T) {
	dir := t.TempDir()
	if _, err := (&Xcode{}).Detect(dir); err == nil {
		t.Error("an empty directory detected as a project")
	}
	if err := os.MkdirAll(filepath.Join(dir, "ios", "App.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	container, err := (&Xcode{}).Detect(dir)
	if err != nil || container != filepath.Join(dir, "ios", "App.xcodeproj") {
		t.Errorf("Detect = %q, %v; want the project under ios/", container, err)
	}
}

// A prebuilt payload is checked, not built: it must exist, be a file, and be
// a kind otata serves.
func TestPrebuilt(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "App.ipa")
	if err := os.WriteFile(ipa, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Prebuilt(ipa)
	if err != nil || res.PayloadPath != ipa || res.Platform != artifact.IOS || res.Config != ConfigPrebuilt {
		t.Errorf("Prebuilt(ipa) = %+v, %v", res, err)
	}
	for name, path := range map[string]string{
		"missing":   filepath.Join(dir, "nope.ipa"),
		"directory": dir,
		"other":     ipa + ".txt",
	} {
		if name == "other" {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Prebuilt(path); err == nil {
			t.Errorf("%s was accepted as a payload", name)
		}
	}
}
