package appmeta

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeTestPNG writes a small valid PNG and returns its bytes.
func writeTestPNG(t *testing.T, path string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			img.Set(x, y, color.NRGBA{R: uint8(x * 32), G: uint8(y * 32), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPNGFirstChunk(t *testing.T) {
	dir := t.TempDir()
	std := filepath.Join(dir, "std.png")
	writeTestPNG(t, std)
	if got := pngFirstChunk(std); got != "IHDR" {
		t.Errorf("standard PNG: first chunk %q, want IHDR", got)
	}
	// The header bytes of a genuinely crushed icon: signature, then a CgBI
	// chunk where IHDR would be.
	cg := filepath.Join(dir, "cgbi.png")
	head := append(append([]byte(nil), pngSignature...), 0, 0, 0, 4)
	head = append(head, []byte("CgBIP\x00 \x06")...)
	if err := os.WriteFile(cg, head, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pngFirstChunk(cg); got != "CgBI" {
		t.Errorf("crushed PNG: first chunk %q, want CgBI", got)
	}
	notPNG := filepath.Join(dir, "not.png")
	if err := os.WriteFile(notPNG, []byte("just some text, long enough to read"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pngFirstChunk(notPNG); got != "" {
		t.Errorf("non-PNG: first chunk %q, want empty", got)
	}
}

// A standard PNG must pass through byte-identical. Most icons (and anything
// arriving via --artifact from another toolchain) were never crushed.
func TestNormalizeIconLeavesStandardPNGAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "icon.png")
	orig := writeTestPNG(t, path)
	if err := normalizeIcon(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Error("a standard PNG was rewritten")
	}
}

// The fixture is crushed by the same tool and flag Xcode's packaging uses, so
// the revert is proven against the real transformation without embedding any
// real app's asset. The bar is decodability by a standard decoder, which is
// the position a browser is in.
func TestNormalizeIconRevertsAppleOptimization(t *testing.T) {
	if exec.Command("xcrun", "-f", "pngcrush").Run() != nil {
		t.Skip("pngcrush not available")
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.png")
	writeTestPNG(t, plain)
	crushed := filepath.Join(dir, "icon.png")
	if out, err := exec.Command("xcrun", "pngcrush", "-iphone", "-q", plain, crushed).CombinedOutput(); err != nil {
		t.Fatalf("could not crush the fixture: %v\n%s", err, out)
	}
	if got := pngFirstChunk(crushed); got != "CgBI" {
		t.Fatalf("fixture was not crushed: first chunk %q", got)
	}

	if err := normalizeIcon(crushed); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(crushed)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := png.Decode(f); err != nil {
		t.Errorf("reverted icon does not decode as standard PNG: %v", err)
	}
	// The staging file must not survive success.
	if _, err := os.Stat(crushed + ".standard"); err == nil {
		t.Error("the staging file was left behind")
	}
}
