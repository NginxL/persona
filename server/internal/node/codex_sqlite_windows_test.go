package node

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCodexSQLiteDoesNotOpenAConsoleWindow(t *testing.T) {
	cmd := exec.Command("sqlite3.exe")
	hideCodexSQLiteWindow(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("SQLite process attributes = %+v", cmd.SysProcAttr)
	}
}
