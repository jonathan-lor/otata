// Package artifact defines the record of a published build.
package artifact

import (
	"fmt"
	"time"
)

// Platform is what a payload runs on. Each record holds one platform's build.
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

// InstallsFromManifest reports whether the phone installs through a manifest
// (iOS) rather than by downloading the payload (Android).
func (p Platform) InstallsFromManifest() bool { return p == IOS }

// ParsePlatform reads a platform off the command line, refusing unknown ones.
func ParsePlatform(s string) (Platform, error) {
	switch p := Platform(s); p {
	case IOS, Android:
		return p, nil
	}
	return "", fmt.Errorf("unknown platform %q; ios or android", s)
}

// Record is everything known about one published build.
type Record struct {
	Slug     string   `json:"slug"`
	Platform Platform `json:"platform"`

	Title    string `json:"title"`
	BundleID string `json:"bundle_id"`
	// Team is who signed an iOS build, and Signer an Android one. Both are
	// empty for a payload with no readable signature.
	Team    string `json:"team,omitempty"`
	Signer  string `json:"signer,omitempty"`
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
	// IconName is the icon's file name, which carries its format. Older records
	// leave it empty, meaning icon.png.
	IconName string `json:"icon_name,omitempty"`

	// Recorded so a second project cannot silently claim an existing slug.
	ProjectPath string `json:"project_path,omitempty"`
}

// SignedBy is Team or Signer, whichever is set.
func (r Record) SignedBy() string {
	if r.Team != "" {
		return r.Team
	}
	return r.Signer
}

// IconFile is the icon's file name, or "" if there is none. Unlike IconName,
// it covers older records.
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

// SizeMB is SizeBytes in megabytes.
func (r Record) SizeMB() float64 {
	return float64(r.SizeBytes) / (1024 * 1024)
}

// Building marks a build in progress.
type Building struct {
	Slug    string    `json:"slug"`
	Started time.Time `json:"started"`
	// PID is the publishing process, so a marker it left behind can be told stale.
	PID    int    `json:"pid,omitempty"`
	Config string `json:"config,omitempty"`
	Commit string `json:"commit,omitempty"`
}
