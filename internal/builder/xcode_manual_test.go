package builder

import (
	"os"
	"path/filepath"
	"testing"
)

// Runs against a real project named by OTATA_TEST_PROJECT, skipped otherwise
// so the suite stays hermetic. OTATA_TEST_SCHEME is the scheme discovery must
// land on. The useful case is a project with many schemes of which only a few
// archive an app. There, neither "lone scheme" nor a naive ".app" match
// resolves correctly, and this is the only test that proves the narrowing
// against real xcscheme files.
func TestResolvesRealProject(t *testing.T) {
	dir := os.Getenv("OTATA_TEST_PROJECT")
	if dir == "" {
		t.Skip("OTATA_TEST_PROJECT not set")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("OTATA_TEST_PROJECT: %v", err)
	}
	x := &Xcode{}
	ok, container := x.Detect(dir)
	if !ok {
		t.Fatal("did not detect the project")
	}
	t.Logf("container = %s", filepath.Base(container))

	apps := appSchemes(container)
	t.Logf("app-producing schemes = %v", keys(apps))

	all, err := x.Schemes(container)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("narrowed candidates = %v", all)

	scheme, err := x.ResolveScheme(container, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved scheme = %s", scheme)
	if want := os.Getenv("OTATA_TEST_SCHEME"); want != "" && scheme != want {
		t.Errorf("resolved %q, want %s", scheme, want)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
