package node

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Desktop seeds run with structured arguments. A Windows npm shim is never passed to cmd.exe.
var desktopClaudeCLI = func() (string, error) {
	path, err := exec.LookPath("claude")
	if err == nil {
		if _, _, err := desktopClaudeProgram(path); err == nil {
			return path, nil
		}
	}
	for _, path := range []string{
		filepath.Join(userHome(), ".local", "bin", "claude.exe"),
		filepath.Join(userHome(), ".claude", "local", "claude.exe"),
	} {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			return path, nil
		}
	}
	path, err = claudeExecutable()
	if err != nil {
		return "", err
	}
	if _, _, err := desktopClaudeProgram(path); err != nil {
		return "", err
	}
	return path, nil
}

func desktopClaudeProgram(path string) (string, []string, error) {
	if strings.EqualFold(filepath.Ext(path), ".exe") {
		return path, nil, nil
	}
	if strings.EqualFold(filepath.Base(path), "claude.cmd") {
		entry := filepath.Join(filepath.Dir(path), "node_modules", "@anthropic-ai", "claude-code", "cli.js")
		if st, err := os.Stat(entry); err == nil && st.Mode().IsRegular() {
			node := filepath.Join(filepath.Dir(path), "node.exe")
			if st, err := os.Stat(node); err != nil || !st.Mode().IsRegular() {
				node, err = exec.LookPath("node.exe")
				if err != nil {
					return "", nil, err
				}
			}
			return node, []string{entry}, nil
		}
	}
	return "", nil, fmt.Errorf("%s cannot be started directly; use Claude Code's native claude.exe or its npm installation", path)
}

func prepareDesktopChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
}
