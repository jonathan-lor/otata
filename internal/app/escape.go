package app

import (
	"bytes"
	"encoding/xml"
)

// xmlText escapes a value for an XML text node.
//
// Every plist here is assembled by hand from untrusted values: an app's
// CFBundleDisplayName, a home directory, a signing team read out of an archive.
// Without this, an app name containing an ampersand produces a manifest iOS can't
// parse, so the install fails on the phone while the otata reports success, and
// a crafted name can inject a whole extra install item.
func xmlText(s string) string {
	var b bytes.Buffer
	// EscapeText only fails if the writer fails, and a Buffer cannot.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
