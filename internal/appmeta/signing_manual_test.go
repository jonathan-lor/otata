package appmeta

import (
	"os"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Reads the profile out of a real .ipa named by OTATA_TEST_IPA, which is the
// only way to prove the CMS unwrap, the per-field plist extract and the
// keychain join agree. The build must be one this machine signed, or the join
// legitimately finds nothing. Skipped when unset, so the suite stays hermetic.
func TestReadsRealSigning(t *testing.T) {
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

	held, err := HeldIdentities()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := payload.Signing(held)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("profile=%q profile_expires=%s cert_expires=%s effective=%s binder=%s",
		sig.ProfileName, sig.ProfileExpires.Format(time.RFC3339),
		sig.CertExpires.Format(time.RFC3339), sig.Expires.Format(time.RFC3339), sig.Binder)
	t.Logf("detail: %s", sig.Detail(time.Now()))

	if sig.ProfileExpires.IsZero() {
		t.Error("no profile expiry")
	}
	if sig.Expires.After(sig.ProfileExpires) {
		t.Errorf("effective deadline %s outlives the profile %s", sig.Expires, sig.ProfileExpires)
	}
	// A development build is signed by a certificate this machine holds, so the
	// keychain join must have found one. Its absence would mean the join is
	// silently matching nothing and every deadline is really the profile's.
	if sig.CertExpires.IsZero() {
		t.Error("no held certificate matched: the keychain join found nothing")
	}
}
