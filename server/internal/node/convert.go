package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	convertTimeout = 90 * time.Second
	convertKeep    = 7 * 24 * time.Hour // converted copies not used for this long are removed on the next conversion
	convertMax     = 200 << 20          // bigger documents are not handed to LibreOffice

	// cacheTouchAfter: a hit refreshes a cached copy's modification time once it is this old, so pruning by age spares
	// the copies in use. Not on every hit: the time is part of the revision a download in progress is pinned to.
	cacheTouchAfter = time.Hour
)

// converting lets one LibreOffice run at a time: it is heavy, and two runs on one profile refuse each other.
var converting sync.Mutex

// renameConverted is os.Rename, for moving LibreOffice's PDF into the cache; tests make it fail as it does when the
// temp folder is on another file system.
var renameConverted = os.Rename

// sofficePath finds LibreOffice: on PATH (soffice, libreoffice), else the app bundle on a Mac.
func sofficePath() string {
	for _, name := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{"/Applications/LibreOffice.app/Contents/MacOS/soffice", filepath.Join(os.Getenv("HOME"), "Applications/LibreOffice.app/Contents/MacOS/soffice")} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// convertFile turns a document a browser cannot show (doc, ppt, rtf, odt, pages, key, or a docx / pptx the web's
// renderer could not draw) into a PDF with LibreOffice, when the machine has it (docs/protocol/protocol.md "File preview"). The
// PDF lands in the node's cache and is read back like any file (file/download).
func (n *Node) convertFile(ctx context.Context, p proto.FileConvertRequest) (*proto.FileConverted, error) {
	if p.To != "" && p.To != "pdf" {
		return nil, fmt.Errorf("can only convert to pdf")
	}
	path, f, st, err := n.openRegular(p.Path, p.Cwd)
	if err != nil {
		return nil, err
	}
	f.Close()
	if st.Size() > convertMax {
		return nil, fmt.Errorf("file is too big to convert (%d bytes)", st.Size())
	}
	// converted copies are kept, named by the file and its version, so opening it again is instant
	dir, err := privateCacheDir("convert")
	if err != nil {
		return nil, fmt.Errorf("conversion cache: %w", err)
	}
	sum := sha256.Sum256([]byte(path + "\x00" + fileRevision(st)))
	out := filepath.Join(dir, hex.EncodeToString(sum[:12])+".pdf")
	cached := func() *proto.FileConverted {
		info, err := os.Stat(out)
		if err != nil || info.Size() == 0 || !usedCached(out, info) {
			return nil
		}
		return &proto.FileConverted{Path: out, Size: info.Size(), Mime: "application/pdf"}
	}
	if c := cached(); c != nil {
		return c, nil
	}
	soffice := sofficePath()
	if soffice == "" {
		return nil, &rpc.Error{Code: proto.CodeNoConverter, Message: "LibreOffice is not installed on this machine"}
	}
	converting.Lock()
	defer converting.Unlock()
	if c := cached(); c != nil { // converted while this request waited for the lock
		return c, nil
	}
	pruneConverted(dir)
	work, err := os.MkdirTemp("", "persona-soffice-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	// LibreOffice names its output after the input: copy the input under a plain name, so an odd file name (or a
	// running LibreOffice of the user's own, through its own profile) never gets in the way.
	ext := strings.ToLower(filepath.Ext(path))
	in := filepath.Join(work, "document"+ext)
	if err := copyFile(path, in); err != nil {
		return nil, explainDenied(path, n.home, err)
	}
	ctx, cancel := context.WithTimeout(ctx, convertTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, soffice, "-env:UserInstallation="+fileURL(filepath.Join(work, "profile"), runtime.GOOS == "windows"),
		"--headless", "--norestore", "--nologo", "--convert-to", "pdf", "--outdir", work, in)
	cmd.WaitDelay = 2 * time.Second
	if msg, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("LibreOffice took longer than %s", convertTimeout)
		}
		return nil, fmt.Errorf("LibreOffice could not convert it: %v %s", err, strings.TrimSpace(string(msg)))
	}
	pdf := filepath.Join(work, "document.pdf")
	got, err := os.Stat(pdf)
	if err != nil || got.Size() == 0 {
		return nil, fmt.Errorf("LibreOffice did not write a PDF")
	}
	if err := renameConverted(pdf, out); err != nil {
		// the temp folder is on another file system: copy it in, never writing the cache's name in place
		src, err := os.Open(pdf)
		if err != nil {
			return nil, err
		}
		defer src.Close()
		if err := writeAtomic(out, src); err != nil {
			return nil, err
		}
	}
	return &proto.FileConverted{Path: out, Size: got.Size(), Mime: "application/pdf"}, nil
}

// fileURL is the absolute path p as a file URL, the form LibreOffice takes its profile in (-env:UserInstallation):
// file:///tmp/x on macOS and Linux. On Windows the drive is the path's first segment, file:///C:/Users/x, because
// whatever follows "file://" is the host (file://C:/x names a host "C:"), and \\server\share\x is file://server/share/x.
// What a URL can't carry as it is (a space, %, #) is escaped. windows is a parameter so tests check both shapes.
func fileURL(p string, windows bool) string {
	u := url.URL{Scheme: "file", Path: p}
	if windows {
		p = strings.ReplaceAll(p, `\`, "/") // filepath.ToSlash converts nothing when this runs elsewhere
		if unc, ok := strings.CutPrefix(p, "//"); ok {
			host, rest, _ := strings.Cut(unc, "/")
			u.Host, u.Path = host, "/"+rest
		} else {
			u.Path = "/" + p
		}
	}
	return u.String()
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// pruneConverted removes copies older than convertKeep.
func pruneConverted(dir string) { pruneOlder(dir, convertKeep) }

// usedCached marks a cached copy as used (cacheTouchAfter) and answers whether it is still there: another request's
// prune may have removed an old one since info was read. A copy whose time cannot be set is still handed out.
func usedCached(path string, info os.FileInfo) bool {
	if time.Since(info.ModTime()) < cacheTouchAfter {
		return true
	}
	now := time.Now()
	return !errors.Is(os.Chtimes(path, now, now), fs.ErrNotExist)
}

// pruneOlder removes the files in dir whose modification time is more than keep ago.
func pruneOlder(dir string, keep time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > keep {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
