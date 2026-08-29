package cli

import (
	"bytes"
	"testing"
)

func TestStripANSI(t *testing.T) {
	var buf bytes.Buffer
	w := &stripANSI{w: &buf}
	Section(w, "Checks")
	Line(w, "\033[1;32mOK\033[0m    %s", "myapp")
	Line(w, "\033[1;33mWARN\033[0m  %s: %s", "myapp signing", "expires soon")
	want := "\n==> Checks\n    OK    myapp\n    WARN  myapp signing: expires soon\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestStripANSISplitSequence(t *testing.T) {
	var buf bytes.Buffer
	w := &stripANSI{w: &buf}
	w.Write([]byte("a\033[1;"))
	w.Write([]byte("32mb\033[0m"))
	if got := buf.String(); got != "ab" {
		t.Fatalf("got %q, want %q", got, "ab")
	}
}

func TestStripANSILeavesUnknownEscape(t *testing.T) {
	var buf bytes.Buffer
	w := &stripANSI{w: &buf}
	w.Write([]byte("x\033yz"))
	if got := buf.String(); got != "x\033yz" {
		t.Fatalf("got %q", got)
	}
}
