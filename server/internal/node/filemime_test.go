package node

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestFileMimeNamesWhatTheViewersOpen(t *testing.T) {
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	for _, test := range []struct {
		name string
		head []byte
		want string
	}{
		{"a.DOCX", nil, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"b.xlsx", nil, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"c.ipynb", nil, "application/x-ipynb+json"},
		{"d.heic", nil, "image/heic"},
		{"e.m4a", nil, "audio/mp4"},
		{"slides.key", []byte("PK\x03\x04rest"), "application/vnd.apple.keynote"},
		{"id.key", []byte("-----BEGIN PRIVATE KEY-----"), "text/plain; charset=utf-8"},
		{"photo", heic, "image/heic"}, // no extension: sniffed
		{"clip", []byte("\x00\x00\x00\x18ftypqt  "), "video/quicktime"},
		{"doc", []byte("%PDF-1.7\n"), "application/pdf"},
		{"notes", []byte("plain words\n"), "text/plain; charset=utf-8"},
		{"empty", nil, ""},
	} {
		if got := fileMime(test.name, test.head); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}

func TestReadFileStreamDescribesBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	video := bytes.Repeat([]byte{0, 1, 2, 3}, maxFileBytes) // bigger than a page
	os.WriteFile(filepath.Join(dir, "clip.mp4"), video, 0600)
	os.WriteFile(filepath.Join(dir, "blob"), []byte{0, 1, 2, 0xff}, 0600)
	os.WriteFile(filepath.Join(dir, "a.csv"), []byte("a,b\n1,2\n"), 0600)
	fc, err := n.readFile("~/clip.mp4", "", 0, true)
	if err != nil || !fc.Stream || fc.Data != "" || fc.Mime != "video/mp4" || fc.Size != int64(len(video)) || fc.Revision == "" || fc.Truncated {
		t.Fatalf("video = %+v, %v", fc, err)
	}
	chunk, err := n.readFileChunk(proto.FileChunkRequest{Path: "~/clip.mp4"})
	if err != nil || chunk.Revision != fc.Revision {
		t.Fatalf("download revision %+v differs from %q", chunk, fc.Revision)
	}
	fc, err = n.readFile("~/blob", "", 0, true) // not known by name: read, found binary, still described only
	if err != nil || !fc.Stream || fc.Data != "" || fc.Mime != "application/octet-stream" {
		t.Fatalf("blob = %+v, %v", fc, err)
	}
	fc, err = n.readFile("~/a.csv", "", 0, true) // text stays text
	if err != nil || fc.Stream || fc.Text != "a,b\n1,2\n" || fc.Mime != "text/csv" {
		t.Fatalf("csv = %+v, %v", fc, err)
	}
	fc, err = n.readFile("~/clip.mp4", "", 0, false) // older apps: base64 as before
	if err != nil || fc.Stream || fc.Data == "" || !fc.Truncated {
		t.Fatalf("legacy = stream %v data %d truncated %v, %v", fc.Stream, len(fc.Data), fc.Truncated, err)
	}
}

func TestDownloadSniffsWhatTheNameDoesNotSay(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	os.WriteFile(filepath.Join(dir, "scan"), []byte("%PDF-1.4\n%rest"), 0600)
	info, err := n.readFileChunk(proto.FileChunkRequest{Path: "~/scan"})
	if err != nil || info.Mime != "application/pdf" {
		t.Fatalf("%+v, %v", info, err)
	}
}

// A .ts file is TypeScript or an MPEG transport stream, and only its bytes tell (#7974): every way a client gets at a
// file (the viewer's file.read with and without stream, the download's type, the Info sheet) says the same.
func TestTypeScriptAndTransportStreamByTheirBytes(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	fixtures := filepath.Join("..", "..", "..", "spec", "fixtures", "file-types")
	script, err := os.ReadFile(filepath.Join(fixtures, "typescript.ts"))
	if err != nil {
		t.Fatal(err)
	}
	segment, err := os.ReadFile(filepath.Join(fixtures, "segment.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"app.ts": script, "esm.mts": script, "cjs.cts": script, "clip.ts": segment,
		"camera.mts": segment, "empty.ts": nil} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"app.ts", "esm.mts", "cjs.cts", "empty.ts"} {
		for _, stream := range []bool{true, false} {
			fc, err := n.readFile("~/"+name, "", 0, stream)
			if err != nil || fc.Stream || fc.Text != string(script[:len(fc.Text)]) || fc.Mime != proto.MimeTypeScript {
				t.Fatalf("%s (stream %v) = %+v, %v", name, stream, fc, err)
			}
		}
	}
	for _, name := range []string{"clip.ts", "camera.mts"} {
		fc, err := n.readFile("~/"+name, "", 0, true)
		if err != nil || !fc.Stream || fc.Text != "" || fc.Data != "" || fc.Mime != proto.MimeTransportStream || fc.Revision == "" {
			t.Fatalf("%s = %+v, %v", name, fc, err)
		}
	}
	for name, want := range map[string]string{"app.ts": proto.MimeTypeScript, "clip.ts": proto.MimeTransportStream} {
		chunk, err := n.readFileChunk(proto.FileChunkRequest{Path: "~/" + name})
		if err != nil || chunk.Mime != want {
			t.Fatalf("download %s = %+v, %v", name, chunk, err)
		}
		if info := n.statDetail("~/"+name, ""); info.Kind != "file" || info.Mime != want {
			t.Fatalf("Info %s = %+v", name, info)
		}
	}
	if info := n.statDetail("~", ""); info.Kind != "dir" || info.Mime != "" {
		t.Fatalf("a folder has no type: %+v", info)
	}
}
