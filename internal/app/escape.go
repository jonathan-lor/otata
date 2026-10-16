package app

import (
	"bytes"
	"encoding/xml"
)

// xmlText escapes s for an XML text node. The hand-built plists use it for
// every value, since app names and paths can hold & or <.
func xmlText(s string) string {
	var b bytes.Buffer
	// EscapeText only fails if the writer fails, and a Buffer cannot.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
