package node

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// sniffBytes is how much of a file's start decides its type (what http.DetectContentType looks at).
const sniffBytes = 512

// previewMimes are the types the apps' viewers switch on (docs/protocol/protocol.md "File preview"). Go's own extension table is
// read from the machine (/etc/mime.types, the registry) and differs between machines, so these are fixed here.
var previewMimes = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".txt": "text/plain", ".log": "text/plain",
	".csv": "text/csv", ".tsv": "text/tab-separated-values", ".json": "application/json",
	".ipynb": "application/x-ipynb+json", ".html": "text/html", ".htm": "text/html",
	".pdf": "application/pdf", ".rtf": "application/rtf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".doc":  "application/msword", ".xls": "application/vnd.ms-excel", ".ppt": "application/vnd.ms-powerpoint",
	".odt": "application/vnd.oasis.opendocument.text", ".ods": "application/vnd.oasis.opendocument.spreadsheet",
	".odp":   "application/vnd.oasis.opendocument.presentation",
	".pages": "application/vnd.apple.pages", ".numbers": "application/vnd.apple.numbers", ".key": "application/vnd.apple.keynote",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".heic": "image/heic", ".heif": "image/heif", ".avif": "image/avif", ".svg": "image/svg+xml", ".bmp": "image/bmp",
	".tif": "image/tiff", ".tiff": "image/tiff", ".ico": "image/x-icon",
	".mp4": "video/mp4", ".m4v": "video/x-m4v", ".mov": "video/quicktime", ".webm": "video/webm", ".mkv": "video/x-matroska",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".wav": "audio/wav", ".flac": "audio/flac",
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".caf": "audio/x-caf",
	".zip": "application/zip",
}

// fileMime is a file's type from its name, else from its first bytes (head; may be empty).
func fileMime(name string, head []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	if proto.ScriptOrStreamExt(ext) { // TypeScript or video: only the bytes tell (#7974)
		if m := proto.ScriptOrStream(head); m != "" {
			return m
		}
		return sniff(head)
	}
	if m, ok := previewMimes[ext]; ok {
		// .key is also a PEM key: a Keynote document is a zip.
		if ext == ".key" && len(head) > 0 && !bytes.HasPrefix(head, []byte("PK\x03\x04")) {
			return "text/plain; charset=utf-8"
		}
		return m
	}
	if m := mime.TypeByExtension(ext); m != "" {
		return m
	}
	return sniff(head)
}

// sniff is a type from a file's first bytes; "" for none.
func sniff(head []byte) string {
	if len(head) == 0 {
		return ""
	}
	if m := sniffMore(head); m != "" {
		return m
	}
	return http.DetectContentType(head)
}

// sniffMore knows ISO media (HEIC, MP4 / MOV, M4A), which http.DetectContentType mostly calls octet-stream.
func sniffMore(head []byte) string {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return ""
	}
	switch string(head[8:12]) {
	case "heic", "heix", "hevc", "heim", "heis", "mif1", "msf1":
		return "image/heic"
	case "avif", "avis":
		return "image/avif"
	case "qt  ":
		return "video/quicktime"
	case "M4A ", "M4B ":
		return "audio/mp4"
	}
	return "video/mp4"
}

// readHead is the start of an open file, for fileMime.
func readHead(f *os.File) []byte {
	buf := make([]byte, sniffBytes)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return nil
	}
	return buf[:n]
}

// mimeAt is the type of the regular file at path, named name (a link's own name), from the name and its first
// bytes; "" when it cannot be read.
func mimeAt(name, path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := readHead(f)
	if head == nil {
		return ""
	}
	return fileMime(name, head)
}

// streamedMime: a type whose bytes are never shown as text, so file.read with stream skips reading a page of it.
func streamedMime(m string) bool {
	switch {
	case strings.HasPrefix(m, "image/svg"):
		return false
	case strings.HasPrefix(m, "image/"), strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return true
	}
	for _, p := range []string{"application/pdf", "application/zip", "application/msword", "application/vnd.ms-",
		"application/vnd.openxmlformats", "application/vnd.oasis", "application/vnd.apple."} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// fileRevision names one version of a file (size and modification time): a download's chunks, a thumbnail and
// file.read with stream all carry it, so a client knows the bytes it fetches belong together.
func fileRevision(st os.FileInfo) string {
	return fmt.Sprintf("%x-%x", st.Size(), st.ModTime().UnixNano())
}
