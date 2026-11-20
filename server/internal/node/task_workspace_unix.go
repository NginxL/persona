//go:build unix

package node

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const workspaceLockSurvivesCommand = true

func workspaceInheritLocks(cmd *exec.Cmd, locks []*os.File) {
	cmd.ExtraFiles = append(cmd.ExtraFiles, locks...)
}
func workspaceFileID(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("not a directory")
	}
	sys := st.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", sys.Dev, sys.Ino), nil
}
func workspaceSyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
