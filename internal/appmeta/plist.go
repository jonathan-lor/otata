package appmeta

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

/*
parseProfile decodes the plist inside a provisioning profile into Go values.

It parses in-process rather than extracting field-by-field through plutil because
each plutil call is a subprocess fed the entire document, so reading one
profile would cost 5+N spawns for N certificates, and otata doctor reads a profile per
published app. decodePlist cannot serve here either because a profile holds dates
and certificate data that JSON cannot represent, which is exactly what the
XML form can. Apple writes the payload as XML, so a binary one would be a
first, and goes through plutil once instead of being guessed at.
*/
func parseProfile(raw []byte) (map[string]any, error) {
	if bytes.HasPrefix(raw, []byte("bplist")) {
		converted, err := plutilToXML(raw)
		if err != nil {
			return nil, err
		}
		raw = converted
	}
	v, err := parsePlistXML(raw)
	if err != nil {
		return nil, err
	}
	dict, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the profile plist is not a dictionary")
	}
	return dict, nil
}

// plutilToXML converts a binary plist to the XML form this package parses.
func plutilToXML(raw []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "plutil", "-convert", "xml1", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(raw)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not convert a binary plist: %v %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// parsePlistXML reads an XML property list into Go values: map[string]any,
// []any, string, time.Time, []byte, int64, float64 and bool. An element
// outside that set is an error, so a document this can't faithfully represent is refused.
func parsePlistXML(raw []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	root, err := nextStart(dec)
	if err != nil {
		return nil, fmt.Errorf("not an XML plist: %w", err)
	}
	if root.Name.Local != "plist" {
		return nil, fmt.Errorf("root element is <%s>, want <plist>", root.Name.Local)
	}
	se, err := nextStart(dec)
	if err != nil {
		return nil, fmt.Errorf("the plist holds no value: %w", err)
	}
	return parseValue(dec, se)
}

// nextStart returns the next start element, skipping whitespace, comments,
// the XML declaration, and the DOCTYPE. Anything else arriving first means the
// value a caller requires isn't there.
func nextStart(dec *xml.Decoder) (xml.StartElement, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, nil
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return xml.StartElement{}, fmt.Errorf("unexpected text %q", string(bytes.TrimSpace(t)))
			}
		case xml.EndElement:
			return xml.StartElement{}, fmt.Errorf("unexpected </%s>", t.Name.Local)
		}
	}
}

func parseValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		return parseDict(dec)
	case "array":
		return parseArray(dec)
	case "string":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return s, nil
	case "date":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		// The plist DTD requires ISO 8601, which is how Apple writes them.
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("bad <date> %q: %w", strings.TrimSpace(s), err)
		}
		return t, nil
	case "data":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		// Base64 in a plist is wrapped across lines; Fields drops the whitespace.
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, fmt.Errorf("bad <data>: %w", err)
		}
		return b, nil
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad <integer> %q", strings.TrimSpace(s))
		}
		return i, nil
	case "real":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("bad <real> %q", strings.TrimSpace(s))
		}
		return f, nil
	case "true":
		return true, dec.Skip()
	case "false":
		return false, dec.Skip()
	}
	return nil, fmt.Errorf("unsupported plist element <%s>", se.Name.Local)
}

// parseDict reads alternating <key> and value elements until </dict>.
func parseDict(dec *xml.Decoder) (map[string]any, error) {
	out := map[string]any{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return out, nil
		case xml.StartElement:
			if t.Name.Local != "key" {
				return nil, fmt.Errorf("<dict> holds <%s> where a <key> belongs", t.Name.Local)
			}
			var key string
			if err := dec.DecodeElement(&key, &t); err != nil {
				return nil, err
			}
			valSE, err := nextStart(dec)
			if err != nil {
				return nil, fmt.Errorf("key %q has no value: %w", key, err)
			}
			v, err := parseValue(dec, valSE)
			if err != nil {
				return nil, err
			}
			out[key] = v
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, fmt.Errorf("stray text %q inside <dict>", string(bytes.TrimSpace(t)))
			}
		}
	}
}

func parseArray(dec *xml.Decoder) ([]any, error) {
	out := []any{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return out, nil
		case xml.StartElement:
			v, err := parseValue(dec, t)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, fmt.Errorf("stray text %q inside <array>", string(bytes.TrimSpace(t)))
			}
		}
	}
}
