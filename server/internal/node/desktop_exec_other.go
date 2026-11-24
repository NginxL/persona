//go:build !windows

package node

import "os/exec"

var desktopClaudeCLI = claudeExecutable

func desktopClaudeProgram(path string) (string, []string, error) { return path, nil, nil }

func prepareDesktopChild(cmd *exec.Cmd) {}
