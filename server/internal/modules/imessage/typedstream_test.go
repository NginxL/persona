package imessage

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// blobs are the synthetic archives of testdata/gen/blobs.swift (made on a Mac with NSArchiver).
func blobs(t *testing.T) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/gen/blobs.json")
	if err != nil {
		t.Fatal(err)
	}
	var hexes map[string]string
	if err := json.Unmarshal(raw, &hexes); err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for k, v := range hexes {
		b, err := hex.DecodeString(v)
		if err != nil {
			t.Fatal(err)
		}
		out[k] = b
	}
	return out
}

func TestBodyText(t *testing.T) {
	b := blobs(t)
	for name, want := range map[string]string{
		"plain":   "示例消息：周五下午三点见",
		"emoji":   "👍 好的 🎉",
		"long":    strings.Repeat("这是一条很长的示例消息。", 20), // 720 bytes: a two-byte length
		"mention": "@张三 明天的示例会议改到十点",                  // an NSMutableAttributedString with a mention
		"link":    "示例链接 https://example.com/a",
		"mutable": "示例：可变的字符串",
	} {
		got, err := bodyText(b[name])
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}

	// Over 65535 bytes the length takes four bytes (0x82): "plain" with its string swapped for a long one.
	plain := b["plain"]
	at := bytes.Index(plain, cStringType) + len(cStringType)
	n := int(plain[at])
	long := strings.Repeat("example ", 9000)
	huge := append([]byte{}, plain[:at]...)
	huge = append(huge, 0x82, byte(len(long)), byte(len(long)>>8), byte(len(long)>>16), byte(len(long)>>24))
	huge = append(huge, long...)
	huge = append(huge, plain[at+1+n:]...)
	if got, err := bodyText(huge); err != nil || got != long {
		t.Errorf("four-byte length: %d bytes, %v", len(got), err)
	}
}

// What is not an archive, or breaks off, is an error, never a panic or a wrong string.
func TestBodyTextRefusesBrokenArchives(t *testing.T) {
	plain := blobs(t)["plain"]
	at := bytes.Index(plain, cStringType) + len(cStringType)
	for name, blob := range map[string][]byte{
		"empty":        nil,
		"not typed":    []byte("bplist00\x01\x02"),
		"header only":  typedMagic,
		"no string":    []byte("\x04\x0bstreamtyped\x81\xe8\x03\x84\x01\x40\x84\x84\x84\x12NSAttribu"),
		"cut length":   plain[:at+1-1],
		"cut text":     plain[:at+5],
		"long and cut": append(append([]byte{}, plain[:at]...), 0x82, 0xff, 0xff, 0xff, 0x7f, 'a'),
		"bad utf8":     append(append([]byte{}, plain[:at]...), 0x02, 0xff, 0xfe),
	} {
		if got, err := bodyText(blob); err == nil {
			t.Errorf("%s: %q, want an error", name, got)
		}
	}
	for i := range len(plain) { // every truncation of a good archive
		if _, err := bodyText(plain[:i]); err == nil && i < at+1+int(plain[at]) {
			t.Fatalf("cut at %d decoded", i)
		}
	}
}
