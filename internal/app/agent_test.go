package app

import (
	"os"
	"path/filepath"
	"testing"
)

// One launch agent exists per user. Whether it belongs to this root is decided by what it embeds.
func TestAgentMatches(t *testing.T) {
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".otata")
	other := t.TempDir()

	cases := []struct {
		name string
		spec agentSpec
		root string
		port int
		want bool
	}{
		{"same root and port", agentSpec{Root: other, Port: 8787}, other, 8787, true},
		{"same root, other port", agentSpec{Root: other, Port: 8787}, other, 9000, false},
		{"other root", agentSpec{Root: other, Port: 8787}, defaultRoot, 8787, false},
		// Every plist this binary writes embeds the port, so one without is hand-made and claims nothing.
		{"no embedded port never matches", agentSpec{Root: other}, other, 9000, false},
		{"no embedded root means the default root", agentSpec{Port: 8787}, defaultRoot, 8787, true},
		{"no embedded root is not a scratch root", agentSpec{Port: 8787}, other, 8787, false},
		{"nothing embedded matches no invocation", agentSpec{}, defaultRoot, 8787, false},
		{"trailing slash is the same root", agentSpec{Root: other + "/", Port: 1}, other, 1, true},
	}
	for _, c := range cases {
		if got := agentMatches(c.spec, c.root, c.port); got != c.want {
			t.Errorf("%s: agentMatches = %v, want %v", c.name, got, c.want)
		}
	}

	// A root reached through a symlink is the same store.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(other, link); err == nil {
		if !agentMatches(agentSpec{Root: link, Port: 1}, other, 1) {
			t.Error("a symlinked spelling of the root was treated as a different root")
		}
	}
}

func TestDescribeAgent(t *testing.T) {
	if got := describeAgent(agentSpec{Root: "/x", Port: 8787}); got != "/x on port 8787" {
		t.Errorf("got %q", got)
	}
	if got := describeAgent(agentSpec{}); got != "the default root" {
		t.Errorf("got %q", got)
	}
}

// disabledIn parses `launchctl print-disabled` output, whose lines this is
// copied from a real machine: `=> disabled`/`=> enabled` on current macOS,
// with `=> true` the older spelling of disabled. Reading it wrong either
// hides a Login Items toggle-off or reports every agent as disabled.
func TestDisabledIn(t *testing.T) {
	const current = `	disabled services = {
		"com.apple.Siri.agent" => disabled
		"com.ollama.ollama" => enabled
		"com.anakepha.otata" => disabled
	}`
	const enabled = `	disabled services = {
		"com.anakepha.otata" => enabled
		"com.anakepha.otata2" => disabled
	}`
	const legacy = `	disabled services = {
		"com.anakepha.otata" => true
		"com.example.other" => false
	}`
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"disabled", current, true},
		{"enabled entry is not disabled", enabled, false},
		{"legacy true means disabled", legacy, true},
		{"absent means enabled", `	disabled services = {
		"com.example.other" => disabled
	}`, false},
		{"empty output", "", false},
	}
	for _, c := range cases {
		if got := disabledIn(c.out, "com.anakepha.otata"); got != c.want {
			t.Errorf("%s: disabledIn = %v, want %v", c.name, got, c.want)
		}
	}
	// A label that merely prefixes another must not borrow its state.
	if disabledIn(enabled, "com.anakepha.otata2") != true {
		t.Error("the longer label's own state was not read")
	}
}

// The drift check hashes two binaries only when nothing cheaper settles it:
// one file under two names is no drift, a size difference is, and the same
// size and time is the same content, which is what the staged copy promises
// by carrying its source's time.
func TestFilesDiffer(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orig := write("otata", "binary-v1")
	if filesDiffer(orig, orig) {
		t.Error("a file differs from itself")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(orig, link); err == nil && filesDiffer(link, orig) {
		t.Error("a symlink differs from its target")
	}
	if !filesDiffer(orig, write("shorter", "v1")) {
		t.Error("a size difference went unnoticed")
	}
	// Same size, different content, different time: only hashing tells.
	if !filesDiffer(orig, write("rebuilt", "binary-v2")) {
		t.Error("a same-size rebuild went unnoticed")
	}
	// A copy that kept its source's time is the same content without a hash.
	copied := write("copy", "binary-v1")
	info, _ := os.Stat(orig)
	if err := os.Chtimes(copied, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if filesDiffer(orig, copied) {
		t.Error("a faithful copy with its source's time was called different")
	}
	if !filesDiffer(orig, filepath.Join(dir, "missing")) {
		t.Error("a missing program was not reported as drifted")
	}
}

// agentRootDigest fills the default root exactly as agentMatches judges it,
// so StopServer's foreign-agent check identifies the same store the match
// logic would.
func TestAgentRootDigest(t *testing.T) {
	home, _ := os.UserHomeDir()
	if agentRootDigest(agentSpec{}) != rootDigest(filepath.Join(home, ".otata")) {
		t.Error("an empty root is not read as the default root")
	}
	other := t.TempDir()
	if agentRootDigest(agentSpec{Root: other + "/"}) != rootDigest(other) {
		t.Error("a trailing slash reads as a different store")
	}
}
