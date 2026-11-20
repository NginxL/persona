// A disposable process boundary for the real codex-start helper. It never runs
// Codex, opens a terminal or contacts a node/hub. Both roles use only the test's
// directory and synthetic input; the receiver sees the OS-decoded native argv.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nwverse/persona/server/internal/node"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "receive" {
		_, err := os.Stat(filepath.Join(os.Args[2], "start.json"))
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"args": os.Args[3:], "source_removed": os.IsNotExist(err),
		})
		return
	}
	var input struct {
		Dir, Prompt          string
		NpmShim, UnsafeBatch bool
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		panic(err)
	}
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	file := filepath.Join(input.Dir, "start.json")
	program := self
	if input.NpmShim {
		program = filepath.Join(input.Dir, "npm", "codex.cmd")
		platform, triple := "codex-win32-x64", "x86_64-pc-windows-msvc"
		if runtime.GOARCH == "arm64" {
			platform, triple = "codex-win32-arm64", "aarch64-pc-windows-msvc"
		}
		pkg := filepath.Join(filepath.Dir(program), "node_modules", "@openai", "codex")
		for name, content := range map[string][]byte{
			program:                               []byte("@exit /b 99\r\n"),
			filepath.Join(pkg, "bin", "codex.js"): nil,
		} {
			if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
				panic(err)
			}
			if err := os.WriteFile(name, content, 0o600); err != nil {
				panic(err)
			}
		}
		binary, err := os.ReadFile(self)
		if err != nil {
			panic(err)
		}
		native := filepath.Join(pkg, "node_modules", "@openai", platform, "vendor", triple, "bin", "codex.exe")
		if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
			panic(err)
		}
		if err := os.WriteFile(native, binary, 0o700); err != nil {
			panic(err)
		}
	}
	if input.UnsafeBatch {
		program = filepath.Join(input.Dir, "unknown.cmd")
		if err := os.WriteFile(program, []byte("@exit /b 99\r\n"), 0o600); err != nil {
			panic(err)
		}
	}
	data, _ := json.Marshal(map[string]any{
		"argv": []string{program, "receive", input.Dir}, "prompt": input.Prompt, "dir": input.Dir,
	})
	if err := os.WriteFile(file, data, 0o600); err != nil {
		panic(err)
	}
	err = node.CodexStart([]string{file})
	_, statErr := os.Stat(file)
	// A successful exec never returns. Limit failures must happen before the
	// receiver starts, with no truncation or terminal-input fallback.
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"limit_refused":  err != nil && strings.Contains(err.Error(), "32767"),
		"batch_refused":  err != nil && strings.Contains(err.Error(), "batch file"),
		"source_removed": os.IsNotExist(statErr),
	})
}
