package node

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// maxFileBytes caps what the phone can pull in one request: a bigger text file comes in pages (`offset`, `next`).
const maxFileBytes = 2 << 20

type fileReadHandle interface {
	io.Reader
	io.ReaderAt
	io.Closer
	Stat() (os.FileInfo, error)
	Fd() uintptr
}

type fileReadDependencies struct {
	size    func(os.FileInfo) int64
	open    func(string) (fileReadHandle, error)
	isSysfs func(fileReadHandle) (bool, error)
}

// FileContent is a file read for the phone's file viewer (paths mentioned in a conversation).
type FileContent struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Size      int64    `json:"size"`
	Mime      string   `json:"mime"`
	Text      string   `json:"text,omitempty"` // UTF-8 text files
	Data      string   `json:"data,omitempty"` // base64 for binary files (images, PDFs), unless asked with stream
	Truncated bool     `json:"truncated"`      // there is more after this page
	Offset    int64    `json:"offset"`         // where this page starts (bytes)
	Next      int64    `json:"next,omitempty"` // where the next page starts; 0 at the end
	IsDir     bool     `json:"is_dir"`
	Entries   []string `json:"entries,omitempty"` // directory listing (names, dirs end with /)
	// Stream: a binary file asked for with stream; its bytes come from file/download (Range), Revision pins them.
	Stream   bool   `json:"stream,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// resolvePath turns a path as the conversation wrote it into one on this machine: ~ is the home, a relative path is
// in cwd. "" when there is nothing to resolve it against.
func (n *Node) resolvePath(path, cwd string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return ""
	case path == "~" || strings.HasPrefix(path, "~/"):
		path = filepath.Join(n.home, strings.TrimPrefix(path, "~"))
	case !filepath.IsAbs(path):
		if cwd == "" {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// readFile reads a file for the phone's viewer, a text file maxFileBytes at a time from offset (the page before's
// `next`): a page that stops short of the end stops after a line's end. Directories are listed, binary files sent
// whole up to the cap (they are not paged), or with stream only described: the client fetches them from
// file/download by Range, however big.
func (n *Node) readFile(path, cwd string, offset int64, stream bool) (*FileContent, error) {
	open := func(path string) (fileReadHandle, error) { return os.Open(path) }
	isSysfs := fileReadIsSysfs
	if n.fileReadDeps != nil && n.fileReadDeps.open != nil {
		open = n.fileReadDeps.open
	}
	if n.fileReadDeps != nil && n.fileReadDeps.isSysfs != nil {
		isSysfs = n.fileReadDeps.isSysfs
	}
	asked := strings.TrimSpace(path)
	if asked == "" {
		return nil, fmt.Errorf("empty path")
	}
	path = n.resolvePath(asked, cwd)
	if path == "" {
		path = filepath.Clean(asked) // relative with no cwd: as the node's own working directory sees it
	}
	real, st, err := n.statSafe(path)
	if err != nil {
		return nil, explainDenied(path, n.home, err)
	}
	size := st.Size()
	if n.fileReadDeps != nil && n.fileReadDeps.size != nil {
		size = n.fileReadDeps.size(st)
	}
	fc := &FileContent{Path: path, Name: filepath.Base(path), Size: size}
	if st.IsDir() {
		fc.IsDir = true
		ents, err := os.ReadDir(real)
		if err != nil {
			return nil, explainDenied(path, n.home, err)
		}
		for i, e := range ents {
			if i >= 500 {
				fc.Truncated = true
				break
			}
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			fc.Entries = append(fc.Entries, name)
		}
		return fc, nil
	}
	// A regular file whose size is 0 can still have content: Linux's /proc and /sys make theirs up as they are read
	// (#4172). Such a file is read to its end, a page at a time like any other; its size says nothing about the offset.
	madeUp := size == 0 && st.Mode().IsRegular()
	if offset < 0 || (!madeUp && offset > size) {
		return nil, fmt.Errorf("offset %d is outside the file (%d bytes)", offset, size)
	}
	f, err := open(real)
	if err != nil {
		return nil, explainDenied(path, n.home, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !sameFileRead(st, opened) {
		return nil, errFileChanged
	}
	// Validate both the opened object and the checked path. A rename, symlink replacement or truncation between
	// the path check and the read must not make this page (or its stream revision) describe a different file.
	validate := func() error {
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if !sameFileRead(st, after) {
			return errFileChanged
		}
		_, current, err := n.statSafe(path)
		if err != nil {
			return explainDenied(path, n.home, err)
		}
		if !sameFileRead(st, current) {
			return errFileChanged
		}
		return nil
	}
	fc.Offset = offset
	if stream {
		fc.Revision = fileRevision(st)
		if offset == 0 && st.Mode().IsRegular() {
			head := make([]byte, sniffBytes)
			nr, err := f.ReadAt(head, 0)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			if m := fileMime(path, head[:nr]); streamedMime(m) {
				if err := validate(); err != nil {
					return nil, err
				}
				fc.Mime, fc.Stream = m, true
				return fc, nil
			}
		}
	}
	var buf []byte
	var more bool
	var shortSysfs bool
	if madeUp {
		// One byte past the page says whether there is more; a device (size 0, not regular) is never read: /dev/zero
		// does not end.
		var r io.Reader = io.NewSectionReader(f, offset, maxFileBytes+1)
		if offset == 0 {
			r = io.LimitReader(f, maxFileBytes+1) // a plain read from the start, which every such file allows
		}
		buf, err = io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		if more = len(buf) > maxFileBytes; more {
			buf = buf[:maxFileBytes]
		} else {
			fc.Size = offset + int64(len(buf)) // as far as it goes: what the viewer shows as its size
		}
	} else {
		buf = make([]byte, min(size-offset, maxFileBytes))
		want := len(buf)
		nr, readErr := f.ReadAt(buf, offset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		buf = buf[:nr]
		if nr < want {
			if !errors.Is(readErr, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}
			if !st.Mode().IsRegular() {
				return nil, io.ErrUnexpectedEOF
			}
			virtual, err := isSysfs(f)
			if err != nil {
				return nil, err
			}
			if !virtual {
				if err := validate(); err != nil {
					return nil, err
				}
				return nil, io.ErrUnexpectedEOF
			}
			if nr == 0 && offset > 0 {
				if err := checkFileEnd(f, offset); err != nil {
					return nil, err
				}
			}
			fc.Size = offset + int64(nr)
			shortSysfs = true
		} else {
			more = size > offset+int64(nr)
			// A sysfs offset at the reported size may already be beyond the actual end.
			if len(buf) == 0 && offset > 0 && st.Mode().IsRegular() {
				virtual, err := isSysfs(f)
				if err != nil {
					return nil, err
				}
				if virtual {
					if err := checkFileEnd(f, offset); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := validate(); err != nil {
		return nil, err
	}
	fc.Mime = fileMime(path, buf[:min(len(buf), sniffBytes)])
	// Text is valid UTF-8 (a page may cut the last character) with no NUL bytes.
	if bytes.IndexByte(buf, 0) < 0 && (utf8.Valid(buf) || (more && utf8.Valid(trimPartialRune(buf)))) {
		text := trimPartialRune(buf)
		if more {
			// Stop after the last whole line, so the next page starts at a line (one longer than a page is cut).
			if i := bytes.LastIndexByte(text, '\n'); i >= 0 {
				text = text[:i+1]
			}
			fc.Next = offset + int64(len(text))
			fc.Truncated = true
		}
		fc.Text = string(text)
		if !strings.HasPrefix(fc.Mime, "text/") && !strings.Contains(fc.Mime, "json") {
			fc.Mime = "text/plain; charset=utf-8"
		}
		return fc, nil
	}
	if offset > 0 {
		return nil, fmt.Errorf("binary files are read whole, not from an offset")
	}
	if stream {
		if shortSysfs {
			// The download endpoint still uses the stat size and revision in its stream descriptor.
			fc.Size = size
		}
		fc.Stream = true
		return fc, nil
	}
	fc.Truncated = more
	fc.Data = base64.StdEncoding.EncodeToString(buf)
	return fc, nil
}

func sameFileRead(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// A zero-byte read does not distinguish the end from an offset past it. Check the preceding byte, without reading
// the whole file or assuming that the reported size is the true size.
func checkFileEnd(f fileReadHandle, offset int64) error {
	var last [1]byte
	n, err := f.ReadAt(last[:], offset-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n == 0 {
		return fmt.Errorf("offset %d is outside the file", offset)
	}
	return nil
}

// maxStatPaths bounds one fs.stat: a page of conversation mentions a few dozen paths at most.
const maxStatPaths = 200

// PathStat is whether a path mentioned in a conversation is there on this machine: file, dir, denied (macOS keeps
// the folder from personad; opening it explains what to do) or missing (also: not resolvable, iCloud Drive).
type PathStat struct {
	Path string `json:"path"` // as asked
	Kind string `json:"kind"` // with detail also "link": a symlink whose target is not there (LinkBroken)
	Size int64  `json:"size,omitempty"`

	// With detail (the Info sheet, files_info.go): permission bits as chmod takes them, owner and group names (numbers
	// when unknown), the times the system keeps, a folder's item count, and for a symlink what it says, the real path
	// it leads to and whether that is missing. Absent where the platform has no such thing.
	Mode       *int       `json:"mode,omitempty"`
	Owner      string     `json:"owner,omitempty"`
	Group      string     `json:"group,omitempty"`
	ModTime    *time.Time `json:"mtime,omitempty"`
	ChangeTime *time.Time `json:"ctime,omitempty"`
	BirthTime  *time.Time `json:"birthtime,omitempty"`
	Items      *int       `json:"items,omitempty"`
	ItemsMore  bool       `json:"items_more,omitempty"`
	Symlink    bool       `json:"symlink,omitempty"`
	LinkTarget string     `json:"link_target,omitempty"`
	LinkReal   string     `json:"link_real,omitempty"`
	LinkBroken bool       `json:"link_broken,omitempty"`
	Mime       string     `json:"mime,omitempty"` // a regular file's type, as file.read would give it
}

// statTimeout bounds one fs.stat: a path on a mount that does not answer (a network share, a sleeping disk) is
// left "missing" rather than holding up the rest; statWorkers look at paths side by side.
var statTimeout = 3 * time.Second

const statWorkers = 8

// statOne looks at one path (a variable for the test of a stat that hangs).
var statOne = (*Node).statPath

// statPaths answers fs.stat: the phone links only the paths that are there (a path in a message may be another
// machine's, or gone). Secrets and iCloud Drive count as missing (pathguard.go). detail: the Info sheet's fields too.
func (n *Node) statPaths(paths []string, cwd string, detail ...bool) []PathStat {
	if len(paths) > maxStatPaths {
		paths = paths[:maxStatPaths]
	}
	out := make([]PathStat, len(paths))
	for i, asked := range paths {
		out[i] = PathStat{Path: asked, Kind: "missing"}
	}
	type answer struct {
		i int
		s PathStat
	}
	// Buffered for every path, so a stat that answers after the deadline does not block its goroutine.
	answers := make(chan answer, len(paths))
	slots := make(chan struct{}, statWorkers)
	// Read once: a stat past the deadline keeps running after this returns, and must not read the variables again.
	stat, timeout := statOne, statTimeout
	if len(detail) > 0 && detail[0] {
		stat = (*Node).statDetail
	}
	for i, asked := range paths {
		go func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			answers <- answer{i, stat(n, asked, cwd)}
		}()
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for range paths {
		select {
		case a := <-answers:
			out[a.i] = a.s
		case <-deadline.C:
			return out
		}
	}
	return out
}

func (n *Node) statPath(asked, cwd string) PathStat {
	s := PathStat{Path: asked, Kind: "missing"}
	p := n.resolvePath(asked, cwd)
	if p == "" {
		return s
	}
	_, st, err := n.statSafe(p)
	switch {
	case err == nil && st.IsDir():
		s.Kind = "dir"
	case err == nil:
		s.Kind, s.Size = "file", st.Size()
	case errors.Is(err, fs.ErrPermission):
		s.Kind = "denied"
	}
	return s
}

// protectedDirs are the folders macOS keeps from programs the user has not allowed (TCC): personad, started over ssh or
// by launchd without Full Disk Access, gets "operation not permitted" there even though the files are the user's own.
var protectedDirs = map[string]string{"Desktop": "桌面", "Documents": "文稿", "Downloads": "下载"}

// explainDenied turns macOS refusing a protected folder into what to do about it; other errors pass through. Who has
// to be given Full Disk Access depends on what started this node (launchedVia).
func explainDenied(path, home string, err error) error {
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	exe, _ := os.Executable()
	via := launchedVia(os.Getenv)
	return explainDeniedFor(path, home, via, fullDiskAccessProgram(via, home, os.Getenv("XPC_SERVICE_NAME"), exe), err)
}

// explainDeniedFor is explainDenied for a node started via ("ssh", "launchd" or ""), exe the program macOS holds it
// to under launchd (fullDiskAccessProgram).
func explainDeniedFor(path, home, via, exe string, err error) error {
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	name := protectedFolderOf(path, home)
	if name == "" {
		return err
	}
	var how string
	switch via {
	case "ssh": // TCC holds what ssh starts to account against sshd-keygen-wrapper, not to personad
		how = "打开 sshd-keygen-wrapper（Persona 经 ssh 在这台 Mac 上运行）"
	case "launchd": // a LaunchAgent's program answers for itself and what it starts (fullDiskAccessProgram)
		if exe == "" {
			exe = "personad"
		}
		how = "点「+」加上 " + exe + " 并打开它（Persona 的 node 由 launchd 服务启动，macOS 按这个服务运行的程序给权限；它更新后又读不了的话，把开关关了再打开）"
	default:
		how = "打开运行 Persona node 的终端或 App"
	}
	return fmt.Errorf("macOS 不让 Persona 读「%s」文件夹里的文件。要读的话，在这台 Mac 的「系统设置 › 隐私与安全性 › 完全磁盘访问权限」里"+
		"%s，或者把文件放到别的文件夹（%w）", name, how, err)
}

// trimPartialRune drops a UTF-8 sequence cut in half at the end of a truncated read.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < 4 && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// DirEntry is one row of the phone's file tree.
type DirEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Symlink bool      `json:"symlink,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	// Ignored: Git ignores it (the tree dims it); never set outside a repository or without git (fsignore.go).
	Ignored bool `json:"ignored,omitempty"`
	// LinkTarget: what a symlink says (its text, as readlink gives it); LinkBroken: what it points to is not there, or
	// a loop of links. A link is never followed for the listing beyond telling a folder from a file; Size and ModTime
	// are its target's when that can be read, otherwise the link's own (#5827).
	LinkTarget string `json:"link_target,omitempty"`
	LinkBroken bool   `json:"link_broken,omitempty"`
}

const maxDirEntries = 2000

// resolveDir turns a listing request's path (empty: cwd, ~/, relative to cwd) into the clean path shown to the app and
// the real path behind it, refused when the path guard says so.
func (n *Node) resolveDir(path, cwd string) (shown, real string, err error) {
	path = n.expandPath(path, cwd)
	if path == "" {
		return "", "", fmt.Errorf("empty path")
	}
	real, err = n.realPath(absPath(path))
	if err != nil {
		return "", "", explainDenied(path, n.home, err)
	}
	return path, real, nil
}

// expandPath turns a path as the app gives it (~/…, relative to cwd, empty for cwd itself) into a clean one; "" when
// there is nothing to go by. Links are not followed.
func (n *Node) expandPath(path, cwd string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		path = cwd
	case path == "~" || strings.HasPrefix(path, "~/"):
		path = filepath.Join(n.home, strings.TrimPrefix(path, "~"))
	case !filepath.IsAbs(path) && cwd != "":
		path = filepath.Join(cwd, path)
	}
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// listDir lists a directory for the file tree in treeOrder. A folder over maxDirEntries keeps the first maxDirEntries in
// that order, so the order is decided on every entry before the cap; only the kept ones pay for a stat (e.Info).
func (n *Node) listDir(path, cwd string) (map[string]any, error) {
	path, real, err := n.resolveDir(path, cwd)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil, explainDenied(path, n.home, err)
	}
	all := n.treeOrder(real, ents)
	list := make([]DirEntry, min(len(all), maxDirEntries))
	for i := range list {
		list[i] = all[i].DirEntry
		if info, err := all[i].e.Info(); err == nil {
			list[i].Size, list[i].ModTime = info.Size(), info.ModTime()
		}
		if list[i].Symlink {
			if target, err := os.Readlink(filepath.Join(real, list[i].Name)); err == nil {
				list[i].LinkTarget = target
			}
			if t := all[i].target; t != nil {
				list[i].Size, list[i].ModTime = t.Size(), t.ModTime()
			}
		}
	}
	markIgnored(real, list)
	return map[string]any{"path": path, "entries": list, "truncated": len(ents) > maxDirEntries}, nil
}

// treeEntry is a folder entry as the file tree shows it, before its size and time are read.
type treeEntry struct {
	DirEntry
	key    string // lowercased name, computed once rather than in every comparison
	e      os.DirEntry
	target os.FileInfo // a link's target, when it could be read
}

// treeOrder sorts the entries of the folder real the way the file tree shows them: directories first, then files, each
// by name (case-insensitive, byte order breaking ties). Links are followed (statSafe) so linked directories sort and
// open like directories (not into iCloud Drive); nothing else is stat'ed. listDir and dirSignature both cap this order,
// so the folder watch looks at the entries the tree shows.
func (n *Node) treeOrder(real string, ents []os.DirEntry) []treeEntry {
	all := make([]treeEntry, len(ents))
	for i, e := range ents {
		d := DirEntry{Name: e.Name(), Dir: e.IsDir(), Symlink: e.Type()&os.ModeSymlink != 0}
		var target os.FileInfo
		if d.Symlink {
			_, st, err := n.statSafe(filepath.Join(real, d.Name))
			switch {
			case err == nil:
				d.Dir, target = st.IsDir(), st
			case errors.Is(err, fs.ErrNotExist) || errors.Is(err, errTooManyLinks):
				d.LinkBroken = true
			}
		}
		all[i] = treeEntry{DirEntry: d, key: strings.ToLower(d.Name), e: e, target: target}
	}
	slices.SortFunc(all, func(a, b treeEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.key, b.key); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return all
}
