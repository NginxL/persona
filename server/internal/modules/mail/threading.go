package mail

import (
	"strings"
	"unicode"

	gomail "github.com/emersion/go-message/mail"
)

// msgIDs reads a Message-ID, In-Reply-To or References field as message ids without the angle brackets, in order.
// go-message's parser gives up on the whole field for one comma between ids, an obsolete phrase (RFC 5322 4.5.4,
// which a receiver must still accept) or an id it does not like, such as one without '@'; then every <...> group of
// the value counts on its own, so the rest of the chain survives.
func msgIDs(h gomail.Header, key string) []string {
	if ids, err := h.MsgIDList(key); err == nil {
		return ids
	}
	var ids []string
	v := h.Get(key)
	for {
		start := strings.IndexByte(v, '<')
		if start < 0 {
			return ids
		}
		v = v[start+1:]
		end := strings.IndexAny(v, "<>")
		if end < 0 {
			return ids
		}
		if v[end] == '<' {
			// An unclosed '<': the id starts at the next one.
			v = v[end:]
			continue
		}
		id := strings.TrimSpace(v[:end])
		v = v[end+1:]
		if id != "" && strings.IndexFunc(id, notInID) < 0 {
			ids = append(ids, id)
		}
	}
}

// notInID reports the runes a kept id may not hold, so a reply never writes a broken or extra header line.
func notInID(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}
