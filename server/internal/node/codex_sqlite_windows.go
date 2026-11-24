package node

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows login tasks inherit PATH once. Read its current registered values only
// when that inherited PATH has no SQLite, without changing the node's environment.
// A portable installation may also keep sqlite3.exe beside personad.exe.
func defaultCodexSQLiteFallbackDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, loc := range []struct {
		root registry.Key
		key  string
	}{
		{registry.CURRENT_USER, `Environment`},
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	} {
		key, err := registry.OpenKey(loc.root, loc.key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		path, typ, err := key.GetStringValue("Path")
		key.Close()
		if err != nil {
			continue
		}
		if typ == registry.EXPAND_SZ {
			path, err = registry.ExpandString(path)
			if err != nil {
				continue
			}
		}
		dirs = append(dirs, filepath.SplitList(path)...)
	}
	if local := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(local) {
		dirs = append(dirs, filepath.Join(local, "Microsoft", "WinGet", "Links"))
	}
	return dirs
}

func hideCodexSQLiteWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
