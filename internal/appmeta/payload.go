package appmeta

import (
	"archive/zip"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Payload is a built app opened for reading.
type Payload interface {
	Info() (Info, error)
	// Signing is who signed the build and, on iOS, when it stops installing.
	// held comes from HeldIdentities, and only iOS uses it. It can fail with
	// ErrNoProfile, ErrUnsigned or ErrUnsupported.
	Signing(held map[string]bool) (Signing, error)
	// Icon writes an icon a browser can show to dest and returns its
	// extension, or ErrNoIcon.
	Icon(dest string) (ext string, err error)
	Close() error
}

// ErrNoIcon reports that a payload has no icon a page could show.
var ErrNoIcon = errors.New("the payload carries no usable icon")

// ErrUnsupported means this machine lacks the tools to read the payload,
// which is normal on one that only serves.
var ErrUnsupported = errors.New("cannot be read on this machine")

// Open returns the reader for a platform's payload at path.
func Open(platform artifact.Platform, path string) (Payload, error) {
	switch platform {
	case artifact.IOS:
		return openIPA(path)
	case artifact.Android:
		return openAPK(path)
	}
	return nil, fmt.Errorf("an %s payload %w: no reader for it", platform, ErrUnsupported)
}

// ipa reads the .app inside an .ipa's Payload/ without unpacking it.
type ipa struct {
	zr   *zip.ReadCloser
	app  fs.FS
	name string
	// plist caches the decoded Info.plist, which takes a plutil run.
	plist map[string]any
}

func openIPA(ipaPath string) (*ipa, error) {
	zr, err := zip.OpenReader(ipaPath)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", ipaPath, err)
	}
	appDir := ""
	for _, f := range zr.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) >= 2 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app") {
			appDir = "Payload/" + parts[1]
			break
		}
	}
	if appDir == "" {
		zr.Close()
		return nil, fmt.Errorf("no Payload/*.app inside %s", ipaPath)
	}
	sub, err := fs.Sub(zr, appDir)
	if err != nil {
		zr.Close()
		return nil, err
	}
	return &ipa{zr: zr, app: sub, name: strings.TrimSuffix(path.Base(appDir), ".app")}, nil
}

func (p *ipa) Close() error { return p.zr.Close() }

func (p *ipa) infoPlist() (map[string]any, error) {
	if p.plist != nil {
		return p.plist, nil
	}
	raw, err := readLimited(p.app, "Info.plist", maxPlistBytes)
	if err != nil {
		return nil, fmt.Errorf("could not read Info.plist: %w", err)
	}
	plist, err := decodePlist(raw)
	if err != nil {
		return nil, err
	}
	p.plist = plist
	return plist, nil
}

func (p *ipa) Info() (Info, error) {
	plist, err := p.infoPlist()
	if err != nil {
		return Info{}, err
	}
	return infoFrom(p.name, plist), nil
}

func (p *ipa) Signing(held map[string]bool) (Signing, error) {
	return readSigning(p.app, held)
}

func (p *ipa) Icon(dest string) (string, error) {
	plist, err := p.infoPlist()
	if err != nil {
		return "", err
	}
	name := findIcon(p.app, plist)
	if name == "" {
		return "", ErrNoIcon
	}
	if err := copyOut(p.app, name, dest); err != nil {
		return "", err
	}
	// A crushed icon is a broken image. No icon shows the placeholder.
	if err := normalizeIcon(dest); err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoIcon, err)
	}
	return ".png", nil
}
