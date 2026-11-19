package node

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameExcl renames from to to unless to exists (os.ErrExist then): the check and the rename are one call, so a
// file that appears meanwhile is never replaced.
func renameExcl(from, to string) error {
	err := renameNoReplace(from, to)
	if errors.Is(err, errNoReplaceRename) {
		return renameIfFree(from, to)
	}
	return err
}

// renameNoReplace is renameExcl without its fallback: errNoReplaceRename where the rename that refuses to replace is
// missing.
func renameNoReplace(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) { // a file system without it
		return errNoReplaceRename
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
