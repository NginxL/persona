package imessage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// About half of the messages in chat.db have no `text`, only `attributedBody`: an NSAttributedString archived with
// NSArchiver (the "typedstream" format, `\x04\x0bstreamtyped…`). Its string comes first: the class chain of the
// attributed string, then the class NSString (or NSMutableString, whose superclass NSString follows it), then the
// type of its contents, `+` (a new shared string "+", written 84 01 2B), then the UTF-8 bytes with their length.
// Only that string is read; the attributes after it (mentions, links) are not needed for the text.

var (
	typedMagic   = []byte("\x04\x0bstreamtyped")
	stringClass  = []byte("\x08NSString")
	cStringType  = []byte{0x84, 0x01, '+'}
	errNotTyped  = errors.New("not a typedstream")
	errNoString  = errors.New("no string in the typedstream")
	errBadLength = errors.New("the string runs past the end")
)

// bodyText is the text in an attributedBody.
func bodyText(blob []byte) (string, error) {
	if !bytes.HasPrefix(blob, typedMagic) {
		return "", errNotTyped
	}
	at := bytes.Index(blob, stringClass)
	if at < 0 {
		return "", errNoString
	}
	// The version and the superclass take a few bytes; the type comes right after them.
	rest := blob[at+len(stringClass):]
	t := bytes.Index(rest[:min(len(rest), 16)], cStringType)
	if t < 0 {
		return "", errNoString
	}
	rest = rest[t+len(cStringType):]
	n, rest, err := typedLength(rest)
	if err != nil {
		return "", err
	}
	if n > len(rest) {
		return "", errBadLength
	}
	s := rest[:n]
	if !utf8.Valid(s) {
		return "", errNoString
	}
	return string(s), nil
}

// typedLength reads a typedstream integer used as a length: one byte below 0x80, or 0x81 and two bytes, or 0x82 and
// four bytes, little-endian.
func typedLength(b []byte) (int, []byte, error) {
	if len(b) == 0 {
		return 0, nil, errBadLength
	}
	switch tag := b[0]; {
	case tag < 0x80:
		return int(tag), b[1:], nil
	case tag == 0x81 && len(b) >= 3:
		return int(binary.LittleEndian.Uint16(b[1:3])), b[3:], nil
	case tag == 0x82 && len(b) >= 5:
		n := binary.LittleEndian.Uint32(b[1:5])
		if n > 1<<26 {
			return 0, nil, errBadLength
		}
		return int(n), b[5:], nil
	}
	return 0, nil, errBadLength
}
