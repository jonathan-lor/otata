// Package artifact defines the one thing that moves through the system: a
// built app plus what is needed to install and describe it.
package artifact

import (
	"fmt"
	"time"
)

// Platform is what a payload runs on. It is the switch for everything that
// differs between them: how a build is packaged, how the phone installs it,
// and what the pages and manifests say. One record holds one platform's
// payload; a cross-platform project publishes one record per platform.
type Platform string

const (
	IOS     Platform = "ios"
	Android Platform = "android"
)

// PayloadExt is the extension a platform's payload file carries.
func (p Platform) PayloadExt() string {
	if p == Android {
		return ".apk"
	}
	return ".ipa"
}

// InstallsFromManifest reports whether the phone installs from a manifest
// that names the payload, which is iOS's itms-services route, rather than
// by fetching the payload itself, which is Android's. It decides whether a
// manifest is written, linked and probed at all.
func (p Platform) InstallsFromManifest() bool { return p == IOS }

// ParsePlatform reads a platform off the command line. The set is closed:
// a platform is a case in every switch that keys on one, so an unknown name
// is refused here rather than falling through all of them.
func ParsePlatform(s string) (Platform, error) {
	switch p := Platform(s); p {
	case IOS, Android:
		return p, nil
	}
	return "", fmt.Errorf("unknown platform %q; ios or android", s)
}

// Record is everything known about one published build. It is the unit stored
// on disk and the unit rendered on the browser install surface.
type Record struct {
	Slug     string   `json:"slug"`
	Platform Platform `json:"platform"`

	Title    string `json:"title"`
	BundleID string `json:"bundle_id"`
	// Team is who signed it, copied off the payload's profile at publish. It is
	// identity, not provenance: iOS installs TEAM.bundle-id, so two teams signing
	// one bundle identifier are two different apps on the phone. Empty on a
	// payload with no readable profile.
	Team    string `json:"team,omitempty"`
	Version string `json:"version"`
	Build   string `json:"build"`

	Config string `json:"config"`

	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
	Dirty  bool   `json:"dirty"`

	BuiltAt     time.Time `json:"built_at"`
	PayloadName string    `json:"payload_name"`
	SizeBytes   int64     `json:"size_bytes"`
	HasIcon     bool      `json:"has_icon"`
	// IconName is the icon's file in the app's directory, and its extension
	// is the format: a PNG out of an iOS payload, WebP out of an Android one,
	// served as it is because nothing here converts. Empty on a record from
	// before the field existed, when every icon was icon.png.
	IconName string `json:"icon_name,omitempty"`

	// Recorded so a second project cannot silently claim an existing slug.
	ProjectPath string `json:"project_path,omitempty"`
}

// IconFile is the icon's name in the app's directory, or "" when the build
// shipped none. Reading it here rather than the field is what keeps a record
// written before IconName existed pointing at the icon it has.
func (r Record) IconFile() string {
	switch {
	case !r.HasIcon:
		return ""
	case r.IconName == "":
		return "icon.png"
	}
	return r.IconName
}

// CacheKey defeats caching of a URL that is otherwise stable across builds.
func (r Record) CacheKey() string {
	commit := r.Commit
	if commit == "" {
		commit = "nocommit"
	}
	return fmt.Sprintf("%s-%s", commit, r.Build)
}

// SizeMB is what humans and pages want; disk reports bytes.
func (r Record) SizeMB() float64 {
	return float64(r.SizeBytes) / (1024 * 1024)
}

// Building marks a build in flight. It's what lets the install
// surface refuse to misrepresent freshness.
type Building struct {
	Slug    string    `json:"slug"`
	Started time.Time `json:"started"`
	// PID of the publishing process. A marker whose process is gone is
	// unambiguously stale, which is what lets doctor --fix clear one left
	// behind by an interrupted build.
	PID    int    `json:"pid,omitempty"`
	Config string `json:"config,omitempty"`
	Commit string `json:"commit,omitempty"`
}
