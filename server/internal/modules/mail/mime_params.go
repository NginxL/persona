package mail

import (
	"io"
	"mime"
	"net/url"
	"strconv"
	"strings"

	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

// go-message reads parameters through mime.ParseMediaType, which drops RFC 2231 values in any charset but us-ascii
// and utf-8, and fails on the whole header when one value is unquoted and not a token (an unquoted RFC 2047 word, a
// raw UTF-8 name, a name with a space; several mailers send these). On a Content-Type it cannot parse, go-message
// returns the raw header as the type.

// partName is an attachment's file name: Content-Disposition's filename, else Content-Type's name. go-message's
// reading is kept when it parses, finds a name and no RFC 2231 value is in a charset it drops; otherwise both
// headers are read with mediaParams.
func partName(h *gomail.AttachmentHeader) string {
	disp, dispForeign := mediaParams(h.Get("Content-Disposition"))
	typ, typForeign := mediaParams(h.Get("Content-Type"))
	if name, err := h.Filename(); err == nil && name != "" && !dispForeign && !typForeign {
		return name
	}
	if name := disp["filename"]; name != "" {
		return name
	}
	return typ["name"]
}

// partType is an attachment's media type. When go-message cannot parse Content-Type, it is the lowercased type
// before the parameters, or application/octet-stream when that is not a valid type either: the raw header must not
// become a download's Content-Type.
func partType(h *gomail.AttachmentHeader) string {
	if t, _, err := h.ContentType(); err == nil {
		return t
	}
	base, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	if t, _, err := mime.ParseMediaType(base); err == nil && strings.Contains(t, "/") {
		return t
	}
	return "application/octet-stream"
}

// mediaParams reads a header's parameters leniently, keys lowercased. Values may be quoted or not; RFC 2231 values
// (key*, key*0*, key*1 ...) are converted from any charset go-message/charset knows and win over a plain key=value;
// RFC 2047 words are decoded in every value. A value that cannot be decoded is kept as is, unless a plain value
// stands in for it. foreign reports an RFC 2231 value decoded from a charset other than us-ascii and utf-8, which
// mime.ParseMediaType drops.
func mediaParams(v string) (params map[string]string, foreign bool) {
	params = map[string]string{}
	sections := map[string]map[string]string{} // base key -> section ("0", "0*", "1" ...) -> value
	for _, field := range splitParams(v)[1:] {
		key, value, ok := strings.Cut(field, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || key == "" {
			continue
		}
		value = unquoteParam(strings.TrimSpace(value))
		baseKey, section, extended := strings.Cut(key, "*")
		if !extended {
			params[key] = value
			continue
		}
		if section == "" {
			section = "0*"
		}
		if sections[baseKey] == nil {
			sections[baseKey] = map[string]string{}
		}
		sections[baseKey][section] = value
	}
	for key, parts := range sections {
		value, undecoded, decoded, other, found := rfc2231Value(parts)
		foreign = foreign || other
		_, plain := params[key]
		switch {
		case decoded:
			params[key] = value
		case found && !plain:
			params[key] = undecoded
		}
	}
	dec := mailWordDecoder()
	for key, value := range params {
		if s, err := dec.DecodeHeader(value); err == nil {
			params[key] = s
		}
	}
	return params, foreign
}

// rfc2231Value joins an RFC 2231 value's sections and converts it to UTF-8 (decoded). undecoded is the value as
// sent, without its charset and language. foreign: it was decoded from a charset other than us-ascii and utf-8.
// found: there is a first section.
func rfc2231Value(sections map[string]string) (value, undecoded string, decoded, foreign, found bool) {
	var raw, text strings.Builder
	cs := ""
	decoded = true
	for n := 0; ; n++ {
		part, ok := sections[strconv.Itoa(n)]
		if ok {
			raw.WriteString(part)
			text.WriteString(part)
			continue
		}
		if part, ok = sections[strconv.Itoa(n)+"*"]; !ok {
			found = n > 0
			break
		}
		if n == 0 {
			c, rest, _ := strings.Cut(part, "'")
			if _, rest, ok = strings.Cut(rest, "'"); ok {
				cs, part = strings.ToLower(c), rest
			}
			foreign = cs != "us-ascii" && cs != "utf-8"
		}
		text.WriteString(part)
		b, err := url.PathUnescape(part)
		decoded = decoded && err == nil
		raw.WriteString(b)
	}
	value = raw.String()
	if decoded && foreign && cs != "" {
		value, decoded = convertCharset(cs, value)
	}
	decoded = decoded && found
	return value, text.String(), decoded, decoded && foreign, found
}

// convertCharset converts s from charset cs to UTF-8 with go-message/charset.
func convertCharset(cs, s string) (string, bool) {
	r, err := charset.Reader(cs, strings.NewReader(s))
	if err != nil {
		return s, false
	}
	b, err := io.ReadAll(r)
	return string(b), err == nil
}

// splitParams splits a header value on the semicolons outside double quotes.
func splitParams(v string) []string {
	var fields []string
	quoted, escaped, start := false, false, 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case c == ';' && !quoted:
			fields = append(fields, v[start:i])
			start = i + 1
		}
	}
	return append(fields, v[start:])
}

// unquoteParam removes the quotes around a quoted value and its backslash escapes; other values are returned as is.
func unquoteParam(v string) string {
	if !strings.HasPrefix(v, `"`) {
		return v
	}
	var b strings.Builder
	for i := 1; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v):
			i++
		case v[i] == '"':
			return b.String()
		}
		b.WriteByte(v[i])
	}
	return b.String()
}
