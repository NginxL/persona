//go:build !windows

package node

import (
	"os"
	"runtime"
)

func bundledCodexAppCLI() string {
	if runtime.GOOS == "darwin" {
		path := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex"
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}
