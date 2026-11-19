//go:build !windows

package node

import "errors"

// toRecycleBin and recycledAt are Windows' (trash_windows.go); toTrash and restoreOne ask for them only there.

func toRecycleBin(real, shown string) (string, error) { return "", errors.ErrUnsupported }

func recycledAt(real, name string) (item, info string, err error) {
	return "", "", errors.ErrUnsupported
}
