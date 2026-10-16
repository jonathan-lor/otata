package appmeta

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Signing is who signed a build and, on iOS, when it stops installing: the
// earlier of the profile's and the signing certificate's expiry.
type Signing struct {
	ProfileName string `json:"profile_name,omitempty"`

	// Team is the iOS signing team, which a profile's name doesn't say.
	Team string `json:"team,omitempty"`

	// Signer is an APK certificate's CN, like "Android Debug". Fingerprint is
	// its SHA-256, which is what Android compares.
	Signer      string `json:"signer,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`

	ProfileExpires time.Time `json:"profile_expires,omitzero"`

	// CertExpires is the expiry of the profile's certificate that this machine
	// holds, or zero if it holds none.
	CertExpires time.Time `json:"cert_expires,omitzero"`

	// Expires is the earlier of the two, or zero with no deadline.
	Expires time.Time `json:"expires,omitzero"`

	// Binder names whichever runs out first, since the remedies differ.
	Binder string `json:"binder,omitempty"`

	// Free is a personal-team profile, which iOS won't install over the air.
	Free bool `json:"free,omitempty"`
}

const (
	BinderProfile     = "profile"
	BinderCertificate = "certificate"
)

// ErrNoProfile means an .ipa has no provisioning profile, which is fine.
var ErrNoProfile = errors.New("no embedded provisioning profile")

// ErrUnsigned means an APK is unsigned or its signature doesn't verify, so
// Android won't install it.
var ErrUnsigned = errors.New("the APK is unsigned, or its signature does not verify")

// readSigning reads the signing deadline out of app's embedded profile. held
// comes from HeldIdentities, and nil means none.
func readSigning(app fs.FS, held map[string]bool) (Signing, error) {
	// The security tool exists only on macOS.
	if runtime.GOOS != "darwin" {
		return Signing{}, fmt.Errorf("a provisioning profile %w: it needs macOS's security tool", ErrUnsupported)
	}
	raw, err := readLimited(app, "embedded.mobileprovision", maxProfileBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Signing{}, ErrNoProfile
		}
		return Signing{}, fmt.Errorf("could not read embedded.mobileprovision: %w", err)
	}
	plist, err := decodeCMS(raw)
	if err != nil {
		return Signing{}, err
	}
	profile, err := parseProfile(plist)
	if err != nil {
		return Signing{}, fmt.Errorf("could not parse the provisioning profile: %w", err)
	}
	return signingFromProfile(profile, held)
}

// signingFromProfile builds Signing from a decoded profile.
func signingFromProfile(profile map[string]any, held map[string]bool) (Signing, error) {
	profileExpires, ok := profile["ExpirationDate"].(time.Time)
	if !ok {
		return Signing{}, fmt.Errorf("the provisioning profile carries no readable ExpirationDate")
	}
	name, _ := profile["Name"].(string)

	s := newSigning(name, profileExpires, heldCertExpiry(profile, held))
	s.Team = profileTeam(profile)
	s.Free = localProvision(profile)
	return s, nil
}

// profileTeam is the first entry of the TeamIdentifier array.
func profileTeam(profile map[string]any) string {
	ids, _ := profile["TeamIdentifier"].([]any)
	if len(ids) == 0 {
		return ""
	}
	team, _ := ids[0].(string)
	return team
}

// localProvision reports a profile Xcode made locally, which only personal
// teams get. Across five real profiles, LocalProvision was the one key that
// told them apart. A paid team's profile doesn't have it.
func localProvision(profile map[string]any) bool {
	v, _ := profile["LocalProvision"].(bool)
	return v
}

// newSigning takes the earlier of the two expiries as the deadline.
func newSigning(name string, profileExpires, certExpires time.Time) Signing {
	s := Signing{
		ProfileName:    name,
		ProfileExpires: profileExpires,
		CertExpires:    certExpires,
		Expires:        profileExpires,
		Binder:         BinderProfile,
	}
	if !certExpires.IsZero() && certExpires.Before(profileExpires) {
		s.Expires = certExpires
		s.Binder = BinderCertificate
	}
	return s
}

// HasDeadline is true on iOS. Android certificates aren't counted down.
func (s Signing) HasDeadline() bool { return !s.Expires.IsZero() }

// SignerName is the APK signer's CN, or the start of its fingerprint if it
// has none.
func (s Signing) SignerName() string {
	switch {
	case s.Signer != "":
		return s.Signer
	case len(s.Fingerprint) >= 12:
		return "sha256:" + s.Fingerprint[:12]
	}
	return s.Fingerprint
}

// Detail is the line doctor and publish print: the deadline and the days
// left, or the signer.
func (s Signing) Detail(now time.Time) string {
	if !s.HasDeadline() {
		if s.Fingerprint != "" && s.Signer != "" {
			return fmt.Sprintf("signed by %s, sha256 %s", s.Signer, s.Fingerprint)
		}
		return "signed by " + s.SignerName()
	}
	// Date and count in now's zone, so the word and the date agree.
	expires := s.Expires.In(now.Location())
	date := expires.Format("2006-01-02")
	days := calendarDays(now, expires)
	switch {
	case !s.Expires.After(now):
		return fmt.Sprintf("%s expired %s, signing no longer works", s.Binder, date)
	case days == 0:
		return fmt.Sprintf("%s expires today, %s", s.Binder, date)
	case days == 1:
		return fmt.Sprintf("%s expires tomorrow, %s", s.Binder, date)
	default:
		return fmt.Sprintf("%s expires %s, in %d days", s.Binder, date, days)
	}
}

// calendarDays counts calendar dates from a to b, which share a zone.
func calendarDays(a, b time.Time) int {
	// UTC midnights, so a DST change can't make a day 23 hours.
	midnight := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	return int(midnight(b).Sub(midnight(a)).Hours() / 24)
}

// Expired reports whether the deadline has passed.
func (s Signing) Expired(now time.Time) bool { return s.HasDeadline() && !s.Expires.After(now) }

// Within reports whether the deadline is less than d away.
func (s Signing) Within(d time.Duration, now time.Time) bool {
	return s.HasDeadline() && !s.Expired(now) && s.Expires.Sub(now) < d
}

// decodeCMS unwraps a provisioning profile's PKCS#7 envelope without
// verifying it, since only its dates are read.
func decodeCMS(raw []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "cms", "-D", "-i", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(raw)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not decode the provisioning profile: %v %s",
			err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// heldCertExpiry returns the latest expiry among the profile's certificates
// this machine holds a key for, since any of them can sign. Zero if none.
func heldCertExpiry(profile map[string]any, held map[string]bool) time.Time {
	certs, _ := profile["DeveloperCertificates"].([]any)
	if len(certs) > maxProfileCerts {
		certs = certs[:maxProfileCerts]
	}

	var latest time.Time
	for _, c := range certs {
		der, ok := c.([]byte)
		if !ok {
			continue
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}
		if !held[fingerprint(cert)] {
			continue
		}
		if cert.NotAfter.After(latest) {
			latest = cert.NotAfter
		}
	}
	return latest
}

// fingerprint is the certificate's SHA-1, the hash `security find-identity` prints.
func fingerprint(cert *x509.Certificate) string {
	sum := sha1.Sum(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// HeldIdentities returns the fingerprints of the keychain's code-signing
// identities. Callers read it once and pass it to every Signing call.
func HeldIdentities() (map[string]bool, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("the keychain %w: it needs macOS's security tool", ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return nil, fmt.Errorf("could not list code-signing identities: %w", err)
	}
	return parseIdentityFingerprints(out), nil
}

// parseIdentityFingerprints reads the hashes out of `find-identity` output,
// skipping revoked certificates, which -v still lists.
func parseIdentityFingerprints(out []byte) map[string]bool {
	held := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "CSSMERR") {
			continue
		}
		fields := strings.Fields(line)
		// The shape is: `1) <40 hex> "name"`.
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ")") {
			continue
		}
		hash := strings.ToUpper(fields[1])
		if len(hash) != 40 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		held[hash] = true
	}
	return held
}
