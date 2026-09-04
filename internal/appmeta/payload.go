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

// Payload is one platform's built app, opened for reading. Everything the
// install surface, the manifest and doctor want to know comes out of it, and
// nothing outside this package knows how a platform packages any of it.
type Payload interface {
	// Info is the app's identity.
	Info() (Info, error)
	// Signing is who signed the build and, on iOS, when it stops being
	// installable. held is this machine's code-signing identities, from
	// HeldIdentities, which the iOS reader joins the profile against and
	// the Android reader ignores. ErrNoProfile means an iOS payload carries
	// nothing to read; ErrUnsigned that an APK will not install; and
	// ErrUnsupported that this machine cannot read it.
	Signing(held map[string]bool) (Signing, error)
	// Icon writes the app's icon to dest in a form a browser decodes, and
	// reports that form as the extension the file is served under: ".png"
	// for an iOS icon, which the packaging optimizes and this reverts, and
	// whatever the launcher icon is for an Android one, WebP in every recent
	// template, which nothing converts. ErrNoIcon means there is none to
	// ship, or none that could be made standard; the page's placeholder is
	// the right answer to either.
	Icon(dest string) (ext string, err error)
	Close() error
}

// ErrNoIcon reports that a payload has no icon a page could show.
var ErrNoIcon = errors.New("the payload carries no usable icon")

// ErrUnsupported reports that this machine cannot read what was asked for.
// The tools are the platform's (plutil and security for an iOS payload,
// aapt2 and apksigner for an Android one), and so is every build that
// produces the payload; a node that only serves what another machine built
// has nothing to read it with, and nothing wrong with it either. It is
// wrapped with what exactly is missing.
var ErrUnsupported = errors.New("cannot be read on this machine")

// Open returns the reader for a platform's payload at path. This is the one
// place that knows which platform is packaged how, so a new platform is a
// new reader and a case here, and no caller changes.
func Open(platform artifact.Platform, path string) (Payload, error) {
	switch platform {
	case artifact.IOS:
		return openIPA(path)
	case artifact.Android:
		return openAPK(path)
	}
	return nil, fmt.Errorf("an %s payload %w: no reader for it", platform, ErrUnsupported)
}

// ipa reads an iOS payload: the .app inside Payload/, in place, without
// unpacking the archive.
type ipa struct {
	zr   *zip.ReadCloser
	app  fs.FS
	name string
	// plist is Info.plist decoded, kept once read: Info and Icon both need it
	// and decoding it is a subprocess.
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

// Icon picks the largest flat icon PNG in the bundle root and writes it to
// dest, reverting the packaging's iphone optimization so a browser decodes it.
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
	// The normalize must succeed for the icon to ship: a crushed icon is a
	// broken image on the page, where no icon is a clean placeholder.
	if err := normalizeIcon(dest); err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoIcon, err)
	}
	return ".png", nil
}
