//go:build !windows

package node

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func externalAdapterPlatformSupported() bool { return true }

// The final registry file must not be a symlink. Ancestors (including macOS's
// /var and /tmp aliases), fixed programs and fixed script paths may be links,
// but every link and every directory traversed must still be locally trusted.
func externalAdapterTrustedPath(name string, executable, finalLink bool) (string, error) {
	fail := func() (string, error) { return "", externalAdapterFailure("adapter_config_conflict") }
	if !filepath.IsAbs(name) || name != filepath.Clean(name) || len(name) > 4096 || strings.ContainsRune(name, '\x00') {
		return fail()
	}
	parts, current, links := strings.Split(strings.TrimPrefix(name, "/"), "/"), "/", 0
	root, err := os.Lstat(current)
	if err != nil || !externalAdapterTrustedInfo(root, true, false) {
		return fail()
	}
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			return fail()
		}
		last := len(parts) == 0
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 || (last && !finalLink) || !externalAdapterTrustedOwner(info) {
				return fail()
			}
			target, err := os.Readlink(next)
			if err != nil {
				return fail()
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(current, target)
			}
			target = filepath.Clean(target)
			parts = append(strings.Split(strings.TrimPrefix(target, "/"), "/"), parts...)
			current = "/"
			continue
		}
		if !externalAdapterTrustedInfo(info, !last, executable && last) {
			return fail()
		}
		current = next
	}
	return current, nil
}

func externalAdapterTrustedOwner(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == uint32(os.Geteuid()) || st.Uid == 0)
}

func externalAdapterTrustedInfo(info os.FileInfo, directory, executable bool) bool {
	if !externalAdapterTrustedOwner(info) || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return false
	}
	if info.Mode().Perm()&0o022 != 0 {
		st := info.Sys().(*syscall.Stat_t)
		// The root-owned sticky temporary directory cannot replace another user's
		// file. Ordinary group- or other-writable directories have no such protection.
		if !directory || st.Uid != 0 || info.Mode()&os.ModeSticky == 0 {
			return false
		}
	}
	return !executable || info.Mode().Perm()&0o111 != 0
}

func externalAdapterOpenConfig(name string) (*os.File, string, error) {
	canonical, err := externalAdapterTrustedPath(name, false, false)
	if err != nil {
		return nil, "", err
	}
	fd, err := unix.Open(canonical, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", externalAdapterFailure("adapter_config_conflict")
	}
	f := os.NewFile(uintptr(fd), canonical)
	info, err := f.Stat()
	pathInfo, pathErr := os.Lstat(canonical)
	if err != nil || pathErr != nil || !externalAdapterTrustedInfo(info, false, false) || !os.SameFile(info, pathInfo) {
		f.Close()
		return nil, "", externalAdapterFailure("adapter_config_conflict")
	}
	return f, canonical, nil
}
