package node

import (
	"context"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDesktopChildKeepsGroupCancellation(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "desktop-helper.exe")
	killGroupOnCancel(cmd)
	attr, delay := cmd.SysProcAttr, cmd.WaitDelay
	prepareDesktopChild(cmd)
	if cmd.SysProcAttr != attr || !cmd.SysProcAttr.HideWindow || cmd.Cancel == nil || cmd.WaitDelay != delay {
		t.Fatal("preparing the hidden desktop child replaced its process-group cancellation setup")
	}
	want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if cmd.SysProcAttr.CreationFlags&want != want {
		t.Fatalf("desktop child flags = %#x, need process group and hidden console", cmd.SysProcAttr.CreationFlags)
	}
}
