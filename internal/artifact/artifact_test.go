package artifact

import "testing"

func TestSignedBy(t *testing.T) {
	if got := (Record{Team: "WDT3B55TUP"}).SignedBy(); got != "WDT3B55TUP" {
		t.Errorf("iOS: %q", got)
	}
	if got := (Record{Signer: "Android Debug"}).SignedBy(); got != "Android Debug" {
		t.Errorf("Android: %q", got)
	}
	if got := (Record{}).SignedBy(); got != "" {
		t.Errorf("unsigned: %q", got)
	}
}

func TestParsePlatform(t *testing.T) {
	for _, s := range []string{"ios", "android"} {
		if p, err := ParsePlatform(s); err != nil || string(p) != s {
			t.Errorf("ParsePlatform(%q) = %q, %v", s, p, err)
		}
	}
	for _, s := range []string{"", "iOS", "IOS", "Android", "windows", "ios "} {
		if _, err := ParsePlatform(s); err == nil {
			t.Errorf("ParsePlatform(%q) accepted", s)
		}
	}
}
