//go:build windows

package node

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// longPath spells out the short (8.3) names in the absolute path p: C:\Users\EXAMPL~1 is C:\Users\Example Home, and
// PERSON~1 is .persona. A short name is another name for the same entry, and the path guard compares names, so the guard
// and the file operations must see the names the entries have (#9891). Only the longest part of p that exists is looked
// up; the rest is kept as written (nothing that is not there has a short name). Links and junctions keep their own names:
// realPath follows them, not this. A path without "~" has no short name and is returned without a look at the disk.
func longPath(p string) string {
	if !strings.Contains(p, "~") {
		return p
	}
	var rest []string
	for q := p; strings.Contains(q, "~"); {
		if long, ok := getLongPathName(q); ok {
			return filepath.Join(append([]string{long}, rest...)...)
		}
		parent := filepath.Dir(q)
		if parent == q {
			break
		}
		rest = append([]string{filepath.Base(q)}, rest...)
		q = parent
	}
	return p
}

// getLongPathName is GetLongPathName for an existing path; a drive path is passed with the \\?\ prefix, which lets it be
// longer than MAX_PATH.
func getLongPathName(p string) (string, bool) {
	const prefix = `\\?\`
	in := p
	if v := filepath.VolumeName(p); len(v) == 2 && v[1] == ':' {
		in = prefix + p
	}
	from, err := windows.UTF16PtrFromString(in)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, max(len(in)*2, windows.MAX_PATH))
	for {
		n, err := windows.GetLongPathName(from, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return "", false
		}
		if int(n) < len(buf) {
			long := windows.UTF16ToString(buf[:n])
			if in != p {
				long = strings.TrimPrefix(long, prefix)
			}
			return long, true
		}
		buf = make([]uint16, n) // too small: n is the size it needs
	}
}
