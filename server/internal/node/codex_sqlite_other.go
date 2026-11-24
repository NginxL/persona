//go:build !windows

package node

import "os/exec"

func defaultCodexSQLiteFallbackDirs() []string { return nil }

func hideCodexSQLiteWindow(*exec.Cmd) {}
