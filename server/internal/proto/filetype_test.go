package proto

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileTypeFixture is one of spec/fixtures/file-types: a real TypeScript module and a real MPEG-TS segment (ffmpeg).
func fileTypeFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "file-types", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScriptOrStreamGoesByTheBytes(t *testing.T) {
	script, segment := fileTypeFixture(t, "typescript.ts"), fileTypeFixture(t, "segment.ts")
	if len(segment)%188 != 0 || len(segment) < ScriptOrStreamProbe {
		t.Fatalf("segment fixture is %d bytes, not whole packets", len(segment))
	}
	// M2TS / AVCHD: a 4-byte timestamp before every 188-byte packet.
	var m2ts []byte
	for i := 0; i < len(segment); i += 188 {
		m2ts = append(append(m2ts, 0, 0, 0, byte(i)), segment[i:i+188]...)
	}
	// Text that has the sync byte where packets would start is still text.
	letters := []byte(strings.Repeat("G", 600))
	cut := []byte("const greeting = \"你好\";\n") // a probe can stop inside a character
	cut = cut[:bytes.IndexRune(cut, '好')+1]
	for _, test := range []struct {
		name string
		head []byte
		want string
	}{
		{"TypeScript", script, MimeTypeScript},
		{"TypeScript, probed", script[:min(len(script), ScriptOrStreamProbe)], MimeTypeScript},
		{"MPEG-TS", segment, MimeTransportStream},
		{"MPEG-TS, probed", segment[:ScriptOrStreamProbe], MimeTransportStream},
		{"MPEG-TS, node head", segment[:512], MimeTransportStream},
		{"M2TS", m2ts[:ScriptOrStreamProbe], MimeTransportStream},
		{"G every 188 bytes", letters, MimeTypeScript},
		{"cut character", cut, MimeTypeScript},
		{"empty", nil, MimeTypeScript},
		{"one packet", segment[:188], ""},
		{"lost sync", append(append([]byte{}, segment[:376]...), 0x48), ""},
		{"other binary", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), ""},
	} {
		if got := ScriptOrStream(test.head); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
	for ext, want := range map[string]bool{".ts": true, ".mts": true, ".cts": true, ".tsx": false, ".m2ts": false, ".js": false} {
		if ScriptOrStreamExt(ext) != want {
			t.Errorf("ScriptOrStreamExt(%q) = %v", ext, !want)
		}
	}
}
