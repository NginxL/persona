package node

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

const agyMaxImageBytes = 1152 << 10

type agyMedia struct {
	Mime string `json:"mime_type"`
	URI  string `json:"uri"`
	Data string `json:"data"`
}

func (media agyMedia) isImage() bool {
	switch media.Mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return media.URI != "" || media.Data != ""
	}
	return false
}

func (media agyMedia) image(brain *os.Root) (*proto.Image, error) {
	var data []byte
	var err error
	if media.URI != "" {
		data, err = agyReadImageFile(brain, media.URI)
	} else if len(media.Data) <= base64.StdEncoding.EncodedLen(agyMaxImageBytes) {
		data, err = base64.StdEncoding.Strict().DecodeString(media.Data)
	}
	if err != nil || len(data) == 0 || len(data) > agyMaxImageBytes || http.DetectContentType(data) != media.Mime ||
		(media.Mime == "image/webp" && (len(data) < 15 || string(data[8:15]) != "WEBPVP8")) {
		return nil, fmt.Errorf("Antigravity image unavailable")
	}
	return &proto.Image{Mime: media.Mime, Data: base64.StdEncoding.EncodeToString(data)}, nil
}

func agyImagePath(ref string) string {
	name := ref
	if strings.HasPrefix(strings.ToLower(ref), "file://") {
		parsed, err := url.Parse(ref)
		if err != nil || parsed.User != nil || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) ||
			strings.ContainsAny(ref, "?#\\") || strings.Contains(strings.ToLower(parsed.EscapedPath()), "%2f") {
			return ""
		}
		name = parsed.Path
		if runtime.GOOS == "windows" {
			if strings.Contains(strings.ToLower(parsed.EscapedPath()), "%5c") {
				return ""
			}
			name = strings.TrimPrefix(name, "/")
		}
		if !filepath.IsAbs(name) {
			return ""
		}
	}
	name = filepath.FromSlash(name)
	if name == "" || strings.ContainsRune(name, 0) || strings.HasPrefix(filepath.ToSlash(name), "//") || strings.HasPrefix(name, `\\`) ||
		(!filepath.IsAbs(name) && !filepath.IsLocal(name)) {
		return ""
	}
	if runtime.GOOS == "windows" {
		if volume := filepath.VolumeName(name); volume != "" && !filepath.IsLocal(strings.TrimLeft(name[len(volume):], `\`)) {
			return ""
		}
	} else if first, _, _ := strings.Cut(name, "/"); strings.ContainsRune(first, ':') {
		if parsed, err := url.Parse(first); err == nil && parsed.Scheme != "" {
			return ""
		}
	}
	return filepath.Clean(name)
}

func agyReadImageFile(brain *os.Root, ref string) ([]byte, error) {
	name := agyImagePath(ref)
	if name == "" {
		return nil, os.ErrInvalid
	}
	lstat, open := os.Lstat, openNoBlock
	if !filepath.IsAbs(name) {
		lstat = brain.Lstat
		open = func(name string) (*os.File, error) { return openKnowledgeFile(brain, name) }
	}
	info, err := lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > agyMaxImageBytes {
		return nil, os.ErrInvalid
	}
	file, err := open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() > agyMaxImageBytes {
		return nil, os.ErrInvalid
	}
	return io.ReadAll(io.LimitReader(file, agyMaxImageBytes+1))
}
