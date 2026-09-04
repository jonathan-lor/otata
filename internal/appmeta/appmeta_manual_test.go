package appmeta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Reads a real .ipa, named by OTATA_TEST_IPA; a staged payload under
// ~/.otata/public/<slug>/ is the natural one. Skipped when it is unset, so the
// suite stays hermetic.
func TestReadsRealIPA(t *testing.T) {
	ipa := os.Getenv("OTATA_TEST_IPA")
	if ipa == "" {
		t.Skip("OTATA_TEST_IPA not set")
	}
	if _, err := os.Stat(ipa); err != nil {
		t.Fatalf("OTATA_TEST_IPA: %v", err)
	}
	payload, err := Open(artifact.IOS, ipa)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	info, err := payload.Info()
	if err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(t.TempDir(), "icon.png")
	iconErr := payload.Icon(icon)
	t.Logf("app=%s bundle=%s title=%q version=%s build=%s icon=%v",
		info.Name, info.BundleID, info.Title, info.Version, info.Build, iconErr)
	if info.BundleID == "" {
		t.Error("no bundle id")
	}
	if iconErr == nil && pngFirstChunk(icon) != "IHDR" {
		t.Error("the icon written is not a standard PNG")
	}
}
