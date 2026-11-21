package node

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func nvmVersionsDirs(home string) []string {
	standard := filepath.Join(home, ".nvm", "versions", "node")
	if root := os.Getenv("NVM_DIR"); filepath.IsAbs(root) {
		if configured := filepath.Join(root, "versions", "node"); configured != standard {
			return []string{configured, standard}
		}
	}
	return []string{standard}
}

// launcherNodeDir finds the interpreter paired with an env-node launcher. npm links bin/<command> to
// lib/node_modules/<package>/… under the same installation prefix, including when another bin links to the command.
func launcherNodeDir(exe string) (dir string, needsNode bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	script, err := filepath.EvalSymlinks(exe)
	if err != nil || !envNodeScript(script) {
		return "", false
	}
	scriptDir := filepath.Dir(script)
	for dir := scriptDir; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		parent := filepath.Dir(dir)
		if filepath.Base(dir) == "node_modules" && filepath.Base(parent) == "lib" {
			bin := filepath.Join(filepath.Dir(parent), "bin")
			if hasLauncherNode(bin) {
				return bin, true
			}
		}
	}
	if filepath.Base(scriptDir) == "bin" && hasLauncherNode(scriptDir) {
		return scriptDir, true
	}
	if bin := filepath.Dir(exe); hasLauncherNode(bin) {
		return bin, true
	}
	return "", true
}

func hasLauncherNode(dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "node"))
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

func envNodeScript(path string) bool {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [64]byte
	n, _ := f.Read(head[:])
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	line, ok := strings.CutPrefix(line, "#!")
	return ok && slices.Equal(strings.Fields(line), []string{"/usr/bin/env", "node"})
}
