package appmeta

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// The real shape of `security find-identity -v -p codesigning`, including the
// revoked entries the flag lists anyway and the trailing count line.
const identityOutput = `
  1) 0A1B2C3D4E5F60718293A4B5C6D7E8F901234567 "Apple Development: someone@example.com (AB12CD34EF)" (CSSMERR_TP_CERT_REVOKED)
  2) 1B2C3D4E5F60718293A4B5C6D7E8F90123456789 "Apple Development: someone@example.com (CD34EF56GH)"
  3) 2C3D4E5F60718293A4B5C6D7E8F9012345678901 "Apple Development: A Person (EF56GH78JK)"
     3 valid identities found
`

func TestParseIdentityFingerprintsKeepsOnlyUsableOnes(t *testing.T) {
	held := parseIdentityFingerprints([]byte(identityOutput))
	if len(held) != 2 {
		t.Fatalf("got %d identities, want 2: %v", len(held), held)
	}
	if !held["2C3D4E5F60718293A4B5C6D7E8F9012345678901"] {
		t.Error("dropped an identity that can sign")
	}
	// A revoked certificate cannot sign. Counting it as held would report a
	// deadline that has effectively already passed.
	if held["0A1B2C3D4E5F60718293A4B5C6D7E8F901234567"] {
		t.Error("kept a revoked identity")
	}
}

func TestParseIdentityFingerprintsIgnoresNoise(t *testing.T) {
	held := parseIdentityFingerprints([]byte("no identities found\n\n  not) a hash\n  1) ZZZZ \"bad\"\n"))
	if len(held) != 0 {
		t.Fatalf("got %v, want none", held)
	}
}

func TestNewSigningTakesTheEarlierClock(t *testing.T) {
	profile := time.Date(2026, 11, 11, 0, 27, 46, 0, time.UTC)
	cert := time.Date(2026, 11, 11, 0, 7, 35, 0, time.UTC)

	s := newSigning("iOS Team Provisioning Profile: *", profile, cert)
	if !s.Expires.Equal(cert) {
		t.Errorf("Expires = %s, want the certificate's %s", s.Expires, cert)
	}
	if s.Binder != BinderCertificate {
		t.Errorf("Binder = %q, want %q", s.Binder, BinderCertificate)
	}
}

func TestNewSigningFallsBackToTheProfile(t *testing.T) {
	profile := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

	// A certificate outliving the profile does not extend anything.
	s := newSigning("p", profile, profile.Add(90*24*time.Hour))
	if !s.Expires.Equal(profile) || s.Binder != BinderProfile {
		t.Errorf("got %s via %q, want the profile's %s", s.Expires, s.Binder, profile)
	}

	// Holding no authorized certificate at all, the normal state on a node
	// that only serves what another machine built.
	s = newSigning("p", profile, time.Time{})
	if !s.Expires.Equal(profile) || s.Binder != BinderProfile {
		t.Errorf("got %s via %q, want the profile's %s", s.Expires, s.Binder, profile)
	}
	if !s.CertExpires.IsZero() {
		t.Errorf("CertExpires = %s, want zero", s.CertExpires)
	}
}

func TestDetailReadsAsADate(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) Signing {
		return newSigning("p", now.Add(d), time.Time{})
	}
	cases := []struct {
		name string
		in   Signing
		want string
	}{
		{"far off", at(83 * 24 * time.Hour), "profile expires 2026-11-11, in 83 days"},
		{"tomorrow", at(30 * time.Hour), "profile expires tomorrow, 2026-08-21"},
		{"today", at(6 * time.Hour), "profile expires today, 2026-08-20"},
		{"expired", at(-48 * time.Hour), "profile expired 2026-08-18, signing no longer works"},
	}
	for _, c := range cases {
		if got := c.in.Detail(now); got != c.want {
			t.Errorf("%s: Detail = %q, want %q", c.name, got, c.want)
		}
	}
}

// Signing with no deadline, which is every Android build, is neither expired
// nor about to be: a zero Expires must not read as the year 0001 having passed.
func TestNoDeadlineNeverExpires(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	s := Signing{Signer: "Android Debug", Fingerprint: "4f9c1a2b3c4d5e6f"}
	if s.HasDeadline() || s.Expired(now) || s.Within(365*24*time.Hour, now) {
		t.Error("identity-only signing reported a deadline")
	}
	if got := s.Detail(now); got != "signed by Android Debug, sha256 4f9c1a2b3c4d5e6f" {
		t.Errorf("Detail = %q", got)
	}
	if got := s.SignerName(); got != "Android Debug" {
		t.Errorf("SignerName = %q", got)
	}
	// A certificate that names nobody is still an identity, by fingerprint.
	anon := Signing{Fingerprint: "4f9c1a2b3c4d5e6f"}
	if got := anon.SignerName(); got != "sha256:4f9c1a2b3c4d" {
		t.Errorf("SignerName with no CN = %q", got)
	}
	if got := anon.Detail(now); got != "signed by sha256:4f9c1a2b3c4d" {
		t.Errorf("Detail with no CN = %q", got)
	}
	// An iOS profile still has its deadline.
	if !newSigning("p", now.Add(time.Hour), time.Time{}).HasDeadline() {
		t.Error("a profile's expiry was not a deadline")
	}
}

func TestExpiredAndWithin(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	window := 30 * 24 * time.Hour

	far := newSigning("p", now.Add(83*24*time.Hour), time.Time{})
	near := newSigning("p", now.Add(10*24*time.Hour), time.Time{})
	gone := newSigning("p", now.Add(-time.Hour), time.Time{})

	if far.Expired(now) || far.Within(window, now) {
		t.Error("a deadline months out should say nothing")
	}
	if !near.Within(window, now) || near.Expired(now) {
		t.Error("a deadline inside the window should warn")
	}
	if !gone.Expired(now) {
		t.Error("a past deadline is expired")
	}
	// An expired deadline is a failure, not a warning; reporting both would
	// render it twice at two different severities.
	if gone.Within(window, now) {
		t.Error("an expired deadline should not also warn")
	}
}

// A profile's own plist, trimmed to the keys this asks about. The free one is
// what a personal team gets. The paid one simply has no such key, which is how
// absence has to be read. A missing LocalProvision IS the paid answer.
const freeProfilePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>Name</key><string>iOS Team Provisioning Profile: com.example.app</string>
	<key>ExpirationDate</key><date>2026-09-01T10:00:00Z</date>
	<key>LocalProvision</key><true/>
	<key>TeamIdentifier</key><array><string>SKH7V8HMR6</string></array>
	<key>TimeToLive</key><integer>7</integer>
</dict></plist>`

const paidProfilePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>Name</key><string>iOS Team Provisioning Profile: *</string>
	<key>ExpirationDate</key><date>2026-11-11T00:27:46Z</date>
	<key>PPQCheck</key><true/>
	<key>TeamIdentifier</key><array><string>WDT3B55TUP</string></array>
	<key>TimeToLive</key><integer>365</integer>
</dict></plist>`

func mustParseProfile(t *testing.T, raw string) map[string]any {
	t.Helper()
	profile, err := parseProfile([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestFreeProfileIsRecognizedByLocalProvision(t *testing.T) {
	if !localProvision(mustParseProfile(t, freeProfilePlist)) {
		t.Error("a personal-team profile was not recognized as free")
	}
	// The paid case is the one that matters. A false positive here refuses a
	// build that installs perfectly well.
	if localProvision(mustParseProfile(t, paidProfilePlist)) {
		t.Error("a paid profile was reported as free")
	}
}

// The team is the first entry of an array, not a bare string. Get that wrong
// and every publish reports no team at all while everything else about signing
// still works.
func TestTeamIsReadFromTheProfilesArray(t *testing.T) {
	if got := profileTeam(mustParseProfile(t, paidProfilePlist)); got != "WDT3B55TUP" {
		t.Errorf("team = %q, want WDT3B55TUP", got)
	}
}

// The whole assembly against the fixture: dates, team and free flag out of one
// parsed profile, with no keychain and no subprocess involved.
func TestSigningFromProfile(t *testing.T) {
	s, err := signingFromProfile(mustParseProfile(t, freeProfilePlist), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if !s.Expires.Equal(want) || s.Binder != BinderProfile {
		t.Errorf("deadline = %s via %q, want %s via profile", s.Expires, s.Binder, want)
	}
	if !s.Free || s.Team != "SKH7V8HMR6" {
		t.Errorf("free=%v team=%q, want a free SKH7V8HMR6 profile", s.Free, s.Team)
	}

	// A profile without an expiry has no deadline to report, which is an
	// error, not a zero time an agent would read as the year 0001.
	if _, err := signingFromProfile(map[string]any{"Name": "p"}, nil); err == nil {
		t.Error("a profile with no ExpirationDate was accepted")
	}
}

// testCert returns a self-signed certificate expiring at notAfter, DER-encoded
// the way a profile's DeveloperCertificates entries are.
func testCert(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Apple Development: test"},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func fingerprintOf(t *testing.T, der []byte) string {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint(cert)
}

// The join used to run inside the keychain read and could only be proven
// against a real machine. Held certificates bound the deadline. Everyone
// else's, however soon they expire, say nothing about this build.
func TestHeldCertExpiryJoinsAgainstHeldOnly(t *testing.T) {
	early := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	mine := testCert(t, early)
	mineToo := testCert(t, late)
	theirs := testCert(t, early.Add(-time.Hour))

	profile := map[string]any{"DeveloperCertificates": []any{theirs, mine, mineToo}}
	held := map[string]bool{fingerprintOf(t, mine): true, fingerprintOf(t, mineToo): true}

	// The latest held expiry wins: any held certificate can sign.
	if got := heldCertExpiry(profile, held); !got.Equal(late) {
		t.Errorf("expiry = %s, want the later held certificate's %s", got, late)
	}
	// Holding none is the zero time, not somebody else's date.
	if got := heldCertExpiry(profile, nil); !got.IsZero() {
		t.Errorf("held nothing but reported %s", got)
	}
	// Junk entries are skipped, not fatal.
	junk := map[string]any{"DeveloperCertificates": []any{"not data", []byte("not der"), mine}}
	if got := heldCertExpiry(junk, held); !got.Equal(early) {
		t.Errorf("junk entries broke the join: got %s, want %s", got, early)
	}
	// No certificate list at all is malformed but not fatal.
	if got := heldCertExpiry(map[string]any{}, held); !got.IsZero() {
		t.Errorf("no list but reported %s", got)
	}
}

// The list's length comes out of the profile, so it is bounded. A held
// certificate past the ceiling is deliberately not examined.
func TestHeldCertExpiryIsBounded(t *testing.T) {
	mine := testCert(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	over := make([]any, maxProfileCerts+1)
	for i := range over {
		over[i] = []byte("padding")
	}
	over[maxProfileCerts] = mine
	got := heldCertExpiry(map[string]any{"DeveloperCertificates": over},
		map[string]bool{fingerprintOf(t, mine): true})
	if !got.IsZero() {
		t.Errorf("an entry beyond the ceiling was examined: %s", got)
	}
}
