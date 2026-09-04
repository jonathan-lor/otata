package artifact

import "testing"

// The set is closed and exact: the spelling is the one the record stores and
// every switch keys on, so a near miss is refused rather than guessed at.
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
