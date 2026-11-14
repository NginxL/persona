package proto

import (
	"bytes"
	"unicode/utf8"
)

// What a .ts, .mts or .cts file is (docs/protocol/protocol.md "File preview", #7974): TypeScript as often as an MPEG
// transport stream (.mts is also AVCHD video), so the name says nothing and the machine's extension table is only a
// guess (video/mp2t on a Mac, text/vnd.qt.linguist on a Linux with shared-mime-info). The node and the hub's file.read
// relay both go by the bytes.
const (
	MimeTransportStream = "video/mp2t"
	MimeTypeScript      = "text/typescript"
)

// ScriptOrStreamProbe is how much of a file's start ScriptOrStream needs: three packets of the larger size.
const ScriptOrStreamProbe = 3 * 192

// ScriptOrStreamExt: a name (its lower-case extension, with the dot) only ScriptOrStream can type.
func ScriptOrStreamExt(ext string) bool {
	return ext == ".ts" || ext == ".mts" || ext == ".cts"
}

// ScriptOrStream types such a file from its first bytes (head): MimeTransportStream for an MPEG transport stream,
// MimeTypeScript for text (an empty file too), "" for anything else, which is sniffed as usual.
//
// A transport stream is 188-byte packets, each starting with the sync byte 0x47 (192-byte packets with it at 4 for
// M2TS / AVCHD): every packet start head holds must have it, head must hold at least two, and it must be binary.
func ScriptOrStream(head []byte) string {
	if bytes.IndexByte(head, 0) < 0 { // text never holds a NUL; a stream's tables and payload always do
		if textHead(head) {
			return MimeTypeScript
		}
		return ""
	}
	if syncEvery(head, 0, 188) || syncEvery(head, 4, 192) {
		return MimeTransportStream
	}
	return ""
}

// syncEvery: the sync byte starts every size-byte packet of head from at, and head holds two packets or more.
func syncEvery(head []byte, at, size int) bool {
	if len(head) <= at+size {
		return false
	}
	for i := at; i < len(head); i += size {
		if head[i] != 0x47 {
			return false
		}
	}
	return true
}

// textHead: UTF-8, but for a character the end of head may cut (a probe stops where it stops).
func textHead(head []byte) bool {
	for cut := 0; cut < utf8.UTFMax && cut <= len(head); cut++ {
		if utf8.Valid(head[:len(head)-cut]) {
			return true
		}
	}
	return false
}
