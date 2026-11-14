package proto

import (
	"path"
	"strings"
)

// A node reports paths in its own system's syntax, whatever system reads them: the hub and its managers may run on
// Linux or macOS while a Windows computer reports C:\Users\me\code. The path package knows only /, and path/filepath
// only the system it runs on, so both misread a Windows node's directories there (#7106). These read a path the way
// its node wrote it: one under a drive (C:\…, C:/…) or a share (\\server\share\…) is a Windows path, anything else a
// Unix one.

// windowsRoot splits a Windows path into its root and the rest below it. The root is a drive's, C:\ (with an
// upper-case letter), or a share's, \\server\share, which needs both its server and its share. ok is false for
// anything else, an incomplete share (\\, \\server) included.
func windowsRoot(p string) (root, rest string, ok bool) {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		if c := p[0] | 0x20; 'a' <= c && c <= 'z' {
			return strings.ToUpper(p[:1]) + `:\`, p[3:], true
		}
		return "", "", false
	}
	tail, ok := strings.CutPrefix(p, `\\`)
	if !ok {
		return "", "", false
	}
	parts := strings.FieldsFunc(tail, func(r rune) bool { return r == '\\' || r == '/' })
	if len(parts) < 2 || strings.ContainsAny(tail[:1], `\/`) {
		return "", "", false
	}
	return `\\` + parts[0] + `\` + parts[1], strings.Join(parts[2:], `\`), true
}

// WindowsPath: p is an absolute Windows path, under a drive or a complete share.
func WindowsPath(p string) bool {
	_, _, ok := windowsRoot(p)
	return ok
}

// NodeAbs: p is an absolute directory on the node that reported it; path.IsAbs is false for a Windows one.
func NodeAbs(p string) bool {
	return path.IsAbs(p) || WindowsPath(p)
}

// NodeClean is path.Clean in the node's own syntax. A Windows path gets backslashes throughout, an upper-case drive
// letter and no trailing separator (a drive's root keeps its own, C:\), and .. stops at the drive or the share, so one
// directory reads as one string however it was written (C:/code/app/, c:\code\app). Any other path is path.Clean's.
func NodeClean(p string) string {
	root, rest, ok := windowsRoot(p)
	if !ok {
		return path.Clean(p)
	}
	rest = strings.Trim(path.Clean("/"+strings.ReplaceAll(rest, `\`, "/")), "/")
	if rest == "" {
		return root
	}
	return strings.TrimSuffix(root, `\`) + `\` + strings.ReplaceAll(rest, "/", `\`)
}

// NodeJoin is path.Join in the node's own syntax: name in the node's directory dir.
func NodeJoin(dir, name string) string {
	if !WindowsPath(dir) {
		return path.Join(dir, name)
	}
	return NodeClean(dir + `\` + name)
}

// NodeKey is what two of a node's paths compare by: NodeClean's form, upper-cased for a Windows path, whose file
// system ignores case (as NTFS compares names), so C:\Code\App and c:\code\app\ are one directory. Send the node
// NodeClean's form, never the key.
func NodeKey(p string) string {
	clean := NodeClean(p)
	if WindowsPath(clean) {
		return strings.ToUpper(clean)
	}
	return clean
}
