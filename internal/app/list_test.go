package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/artifact"
)

func TestListNamesTheSigner(t *testing.T) {
	var buf bytes.Buffer
	ListResult{Apps: []artifact.Record{
		{Slug: "signed", Title: "Signed", Version: "1.0", Build: "1", Config: "Release", Team: "WDT3B55TUP"},
		{Slug: "droid", Title: "Droid", Version: "1.0", Build: "1", Config: "Debug", Platform: artifact.Android, Signer: "Android Debug"},
		{Slug: "older", Title: "Older", Version: "1.0", Build: "1", Config: "Release"},
	}}.Human(&buf)
	out := buf.String()
	if !strings.Contains(out, "WDT3B55TUP") {
		t.Errorf("the team is not in the listing:\n%s", out)
	}
	if !strings.Contains(out, "Android Debug") {
		t.Errorf("the Android signer is not in the listing:\n%s", out)
	}
	if !strings.Contains(out, " - ") {
		t.Errorf("a record with no team recorded shows blank rather than absent:\n%s", out)
	}
}

func TestListHeaderAppearsOnlyWithRows(t *testing.T) {
	var withRows, empty bytes.Buffer
	ListResult{Apps: []artifact.Record{{Slug: "app", Title: "App", Config: "Release"}}}.Human(&withRows)
	ListResult{}.Human(&empty)

	if !strings.Contains(withRows.String(), "SLUG") || !strings.Contains(withRows.String(), "SIGNER") {
		t.Errorf("no header above the rows:\n%s", withRows.String())
	}
	if strings.Contains(empty.String(), "SLUG") {
		t.Errorf("labeled columns that are not there:\n%s", empty.String())
	}
	if !strings.Contains(empty.String(), "nothing published yet") {
		t.Errorf("an empty listing says nothing at all:\n%s", empty.String())
	}
}
