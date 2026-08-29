package appmeta

import (
	"os"
	"testing"
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
	app, closer, name, err := FromIPA(ipa)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	info, err := Read(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("app=%s bundle=%s title=%q version=%s build=%s icon=%q",
		name, info.BundleID, info.Title, info.Version, info.Build, info.IconName)
	if info.BundleID == "" {
		t.Error("no bundle id")
	}
}
