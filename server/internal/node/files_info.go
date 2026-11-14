package node

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The file page's Info sheet (#5827, docs/protocol/protocol.md "File details and links"): fs.stat with detail adds what
// Finder's Get Info shows: the permission bits, the owner and group, the times, a folder's number of items, and for a
// symlink what it says, where it leads and whether that is there, and for a file the type the file page opens it as (its
// first bytes read where the name cannot tell, #7974). Nothing is read that fs.stat would refuse: the path passes the
// same guard, and a link is looked at as itself first (its folder resolved, the link not followed).

// maxInfoItems bounds the count of a folder's entries (a folder of millions is "more than 10 000").
const maxInfoItems = 10000

// fileOwner is the uid, gid and the times only the system's own stat has (fileInfoSys, per platform).
type fileOwner struct {
	uid, gid   int
	ok         bool
	ctime      time.Time
	birth      time.Time // zero where the file system or the platform does not keep it
	birthKnown bool
}

func (n *Node) statDetail(asked, cwd string) PathStat {
	s := PathStat{Path: asked, Kind: "missing"}
	p := n.resolvePath(asked, cwd)
	if p == "" {
		return s
	}
	// The link itself: its folder through the guard, its own name not followed.
	dir, err := n.realPath(absPath(filepath.Dir(p)))
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			s.Kind = "denied"
		}
		return s
	}
	self := filepath.Join(dir, filepath.Base(p))
	if err := n.offLimitsResolved(self); err != nil {
		return s
	}
	lst, err := os.Lstat(self)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			s.Kind = "denied"
		}
		return s
	}
	info, at := lst, self
	if lst.Mode()&fs.ModeSymlink != 0 {
		s.Symlink = true
		if target, err := os.Readlink(self); err == nil {
			s.LinkTarget = target
		}
		real, st, err := n.statSafe(p)
		switch {
		case err == nil:
			s.LinkReal, info, at = real, st, real
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, errTooManyLinks):
			s.LinkBroken = true
		case errors.Is(err, fs.ErrPermission):
			s.Kind = "denied"
			return s
		default: // a link into a secret or iCloud Drive: what it says, nothing about what it leads to
			return s
		}
	}
	switch {
	case s.LinkBroken:
		s.Kind = "link"
	case info.IsDir():
		s.Kind = "dir"
		s.Items, s.ItemsMore = countItems(at)
	default:
		s.Kind = "file"
		s.Size = info.Size()
		if info.Mode().IsRegular() {
			s.Mime = mimeAt(p, at)
		}
	}
	mode := unixMode(info.Mode())
	s.Mode = &mode
	mt := info.ModTime()
	s.ModTime = &mt
	if o := fileInfoSys(info, at); o.ok {
		s.Owner, s.Group = userName(o.uid), groupName(o.gid)
		if !o.ctime.IsZero() {
			s.ChangeTime = &o.ctime
		}
		if o.birthKnown && !o.birth.IsZero() {
			s.BirthTime = &o.birth
		}
	} else if bt := o.birth; o.birthKnown && !bt.IsZero() { // Windows: no owner here, a creation time
		s.BirthTime = &bt
	}
	return s
}

// unixMode is a FileMode's permission bits as chmod takes them, with setuid, setgid and sticky.
func unixMode(m fs.FileMode) int {
	bits := int(m.Perm())
	if m&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

// countItems counts a folder's entries, up to maxInfoItems (more: true).
func countItems(dir string) (*int, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	count := 0
	for count <= maxInfoItems {
		names, err := f.Readdirnames(1000)
		count += len(names)
		if err != nil {
			break
		}
	}
	if count > maxInfoItems {
		c := maxInfoItems
		return &c, true
	}
	return &count, false
}

var (
	namesMu sync.Mutex
	users   = map[int]string{}
	groups  = map[int]string{}
)

// userName is a uid's name: this process's own user from user.Current (which also works without cgo), others from the
// system's tables, else the number.
func userName(uid int) string {
	namesMu.Lock()
	defer namesMu.Unlock()
	if name, ok := users[uid]; ok {
		return name
	}
	name := strconv.Itoa(uid)
	if uid == os.Getuid() {
		if u, err := user.Current(); err == nil && u.Username != "" {
			name = u.Username
		}
	} else if u, err := user.LookupId(name); err == nil && u.Username != "" {
		name = u.Username
	}
	users[uid] = name
	return name
}

func groupName(gid int) string {
	namesMu.Lock()
	defer namesMu.Unlock()
	if name, ok := groups[gid]; ok {
		return name
	}
	name := strconv.Itoa(gid)
	if g, err := user.LookupGroupId(name); err == nil && g.Name != "" {
		name = g.Name
	}
	groups[gid] = name
	return name
}
