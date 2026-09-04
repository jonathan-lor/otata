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

// Signing is when a published build stops working. Two clocks bound it: the
// provisioning profile expires, and so does the certificate that signed it.
// The earlier is the deadline, which is why Expires is not the profile's own
// date.
//
// Renewing either needs a Mac with Xcode and an unlocked keychain.
type Signing struct {
	ProfileName string `json:"profile_name,omitempty"`

	// Team is who signed it. Automatic signing picks, and on a machine with
	// several Apple accounts it picks silently: a profile name reads "iOS Team
	// Provisioning Profile: *" for every app a paid team signs, naming no team.
	Team string `json:"team,omitempty"`

	ProfileExpires time.Time `json:"profile_expires"`

	// CertExpires is the profile-authorized certificate whose private key this
	// machine holds. A profile lists every certificate its team may sign with,
	// and most belong to other machines, so their dates say nothing about this
	// build. Zero when none is held, which is normal on a node that only serves.
	CertExpires time.Time `json:"cert_expires,omitzero"`

	// Expires is the effective deadline: the earlier of the two above.
	Expires time.Time `json:"expires"`

	// Binder names the clock that runs out first, because the remedy differs:
	// a profile is regenerated, while a certificate is reissued and its private
	// key then has to land in this machine's keychain.
	Binder string `json:"binder"`

	// Free reports a personal-team profile, and iOS installs those only from a paired host.
	// See MIInstallerErrorDomain 111.
	Free bool `json:"free,omitempty"`
}

const (
	BinderProfile     = "profile"
	BinderCertificate = "certificate"
)

// ErrNoProfile reports that the payload carries no provisioning profile at
// all: an .ipa published through --artifact by a toolchain that strips it.
// Nothing is wrong in that case, so callers say nothing.
var ErrNoProfile = errors.New("no embedded provisioning profile")

// readSigning reports when the build in app stops being installable. held is
// this machine's code-signing identities from HeldIdentities, handed in
// rather than enumerated here: the keychain answer is a fact about the
// machine, identical for every payload a command inspects, and enumerating it
// per payload made doctor's cost scale with the number of published apps. A
// nil map reads as holding none.
func readSigning(app fs.FS, held map[string]bool) (Signing, error) {
	// The tools are macOS's: a Linux node serves artifacts some other machine
	// signed, and has no standing to audit them.
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

// signingFromProfile assembles the deadline from a decoded profile. Split from
// the reading so the assembly is testable without a CMS envelope or a bundle.
func signingFromProfile(profile map[string]any, held map[string]bool) (Signing, error) {
	profileExpires, ok := profile["ExpirationDate"].(time.Time)
	if !ok {
		return Signing{}, fmt.Errorf("the provisioning profile carries no readable ExpirationDate")
	}
	// Absent only in a malformed profile; the dates still stand without it.
	name, _ := profile["Name"].(string)

	s := newSigning(name, profileExpires, heldCertExpiry(profile, held))
	s.Team = profileTeam(profile)
	s.Free = localProvision(profile)
	return s, nil
}

// profileTeam is the first entry of the TeamIdentifier array: an array, not a
// bare string, and absent only in a malformed profile.
func profileTeam(profile map[string]any) string {
	ids, _ := profile["TeamIdentifier"].([]any)
	if len(ids) == 0 {
		return ""
	}
	team, _ := ids[0].(string)
	return team
}

// localProvision reports whether Xcode provisioned the profile locally rather
// than Apple's portal issuing it: what a personal team gets and a paid team
// never does.
//
// The alternative was TimeToLive: 7 against a paid team's 365, a number Apple
// can redefine that names a consequence rather than the thing. Measured across
// five profiles, this key was on exactly the two personal-team ones; every
// other field, IsXcodeManaged included, failed to separate them. Absence is
// the normal case and the answer: a paid profile simply has no such key.
func localProvision(profile map[string]any) bool {
	v, _ := profile["LocalProvision"].(bool)
	return v
}

// newSigning resolves the two clocks into one deadline. Split out from the
// reading so the resolution is testable without a keychain or a bundle.
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

// Detail is the one line doctor and publish both print. It leads with the date
// because that is what a person acts on, and carries the remaining days because
// that is what makes the date mean something at a glance.
func (s Signing) Detail(now time.Time) string {
	date := s.Expires.Format("2006-01-02")
	// Truncating toward zero would call 23 hours "0 days" and read as today.
	days := int(s.Expires.Sub(now).Hours() / 24)
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

// Expired reports whether signing has already stopped working.
func (s Signing) Expired(now time.Time) bool { return !s.Expires.After(now) }

// Within reports whether the deadline is close enough to be worth saying.
func (s Signing) Within(d time.Duration, now time.Time) bool {
	return !s.Expired(now) && s.Expires.Sub(now) < d
}

// ---------- reading ----------

// decodeCMS unwraps the PKCS#7 envelope a provisioning profile is wrapped in,
// yielding the XML plist inside. The signature is deliberately not verified:
// the profile came out of a bundle this machine already serves, and what is
// asked of it is a date, not a trust decision.
func decodeCMS(raw []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// -i /dev/stdin rather than a temp file: the profile is small, and a temp
	// file is one more thing to leak on a failure path.
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
// whose private key this machine holds, or the zero time if it holds none.
// A profile with no certificate list, or with entries that do not parse, is
// malformed but not fatal: the profile's own date still bounds the build.
//
// The latest, not the earliest: any held certificate can sign, so the
// longest-lived bounds the next build. The earliest, or ignoring which are
// held, reports a date set by somebody else's certificate.
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

// fingerprint is the SHA-1 of the DER certificate, the identifier
// `security find-identity` prints. It is the join key
// against what the keychain reports, and a stronger digest would not match.
func fingerprint(cert *x509.Certificate) string {
	sum := sha1.Sum(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// HeldIdentities is the set of code-signing identities this machine can
// actually sign with, an identity being a certificate whose private key is in
// the keychain, which is exactly what `find-identity` enumerates.
//
// Exported so a command enumerates the keychain once, however many payloads it
// inspects: the answer is per-machine, and the subprocess costs ~50ms.
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

// parseIdentityFingerprints reads the hashes out of `find-identity` output.
//
// Revoked certificates are listed by -v despite the flag's name, annotated
// rather than withheld. They cannot sign, so counting one as held would report
// a deadline that has already passed.
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
