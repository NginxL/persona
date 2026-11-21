package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// installedExtension lays out what the node leaves for an extension package it installed under dataDir (plugins.go):
// its marked <id>/ (the node's own pluginHost.own), the version's digest record and manifest, and a hook program in
// the package. It returns the program's path.
func installedExtension(t *testing.T, dataDir, id, version, name string) string {
	t.Helper()
	ph := &pluginHost{dir: filepath.Join(dataDir, nodePluginsDir)}
	if err := ph.own(id); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ph.dir, id, version)
	manifest, _ := json.Marshal(map[string]any{"manifest_version": 1, "id": id, "name": name, "version": version})
	writeFiles(t, dir, map[string]string{
		".persona-digest":        strings.Repeat("ab", 32) + "\n",
		"persona-extension.json": string(manifest),
	})
	prog := filepath.Join(mkdir(t, filepath.Join(dir, "bin")), "approve-hook")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return prog
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	mkdir(t, dir)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// writePermissionHooks writes the user's settings.json with these synchronous PermissionRequest hooks (raw JSON
// objects, so a test can give args or async).
func writePermissionHooks(t *testing.T, configDir string, hooks ...string) {
	t.Helper()
	body := `{"hooks":{"PermissionRequest":[{"matcher":"*","hooks":[` + strings.Join(hooks, ",") + `]}]}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commandHook(command string) string {
	b, _ := json.Marshal(map[string]any{"type": "command", "command": command})
	return string(b)
}

func hookRoots(t *testing.T) (configDir, dataDir string) {
	t.Helper()
	root := t.TempDir()
	return mkdir(t, filepath.Join(root, "claude")), mkdir(t, filepath.Join(root, "persona"))
}

// An installed extension's own hook is the user's choice: ok, and the detail says which extension it is (#11973).
func TestGlobalPermissionHooksNamesInstalledExtension(t *testing.T) {
	configDir, dataDir := hookRoots(t)
	prog := installedExtension(t, dataDir, "approver", "1.4.0", "Example Approver")
	cmd := shellQuote(prog) + " permission-request"
	writePermissionHooks(t, configDir, commandHook(cmd))

	c := checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
	if c.Status != DoctorOK || c.Hint != "" {
		t.Fatalf("an installed extension's hook: %s %q (hint %q)", c.Status, c.Detail, c.Hint)
	}
	if want := "1 个同步的 PermissionRequest 钩子，都来自已安装的扩展（Example Approver）"; c.Detail != want {
		t.Errorf("detail %q, want %q", c.Detail, want)
	}
	if len(c.Items) != 1 || c.Items[0] != shorten(cmd, 80) {
		t.Errorf("items %q", c.Items)
	}
	en := checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Resolve("en"))
	if !strings.Contains(en.Detail, "all from installed extensions (Example Approver)") {
		t.Errorf("en detail %q", en.Detail)
	}
	// Without the persona dir (nothing to check the install record against) it is a hook like any other.
	if c := checkGlobalPermissionHooksIn(configDir, "", i18n.Default); c.Status != DoctorWarn {
		t.Errorf("no persona dir: %s %q", c.Status, c.Detail)
	}
}

// The ways a command line names the program all lead to the same package; two extensions are both named, once.
func TestGlobalPermissionHooksExtensionSpellings(t *testing.T) {
	configDir, dataDir := hookRoots(t)
	home := filepath.Dir(dataDir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	prog := installedExtension(t, dataDir, "approver", "1.4.0", "Example Approver")
	other := installedExtension(t, dataDir, "second", "0.2.0", "")
	tilde := filepath.ToSlash(filepath.Join("persona", nodePluginsDir, "approver", "1.4.0", "bin", "approve-hook"))
	writePermissionHooks(t, configDir,
		commandHook(`"`+prog+`" --gate`),
		commandHook("APPROVER_MODE=strict "+shellQuote(prog)),
		commandHook("~/"+tilde),
		commandHook(`"$HOME/`+tilde+`" check`),
		commandHook("${HOME}/"+tilde),
		`{"type":"command","command":`+string(mustJSON(other))+`,"args":["--gate"]}`,
		`{"command":`+string(mustJSON(other))+`,"args":[]}`,
	)
	c := checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
	if c.Status != DoctorOK {
		t.Fatalf("%s %q", c.Status, c.Detail)
	}
	if want := "7 个同步的 PermissionRequest 钩子，都来自已安装的扩展（Example Approver、second）"; c.Detail != want {
		t.Errorf("detail %q, want %q", c.Detail, want)
	}
}

// Another tool's hook still warns, alone or next to an extension's; the extension's is still named.
func TestGlobalPermissionHooksForeignStillWarns(t *testing.T) {
	configDir, dataDir := hookRoots(t)
	prog := installedExtension(t, dataDir, "approver", "1.4.0", "Example Approver")

	writePermissionHooks(t, configDir, commandHook("/usr/local/bin/other-gate check"))
	c := checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
	if c.Status != DoctorWarn || c.Hint == "" || c.Detail != "1 个同步的 PermissionRequest 钩子（/usr/local/bin/other-gate check）" {
		t.Errorf("a foreign hook: %s %q (hint %q)", c.Status, c.Detail, c.Hint)
	}

	writePermissionHooks(t, configDir, commandHook(prog), commandHook("other-gate check"),
		`{"type":"command","command":"async-logger","async":true}`)
	c = checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
	if want := "1 个同步的 PermissionRequest 钩子（other-gate check），另有 1 个来自已安装的扩展（Example Approver）"; c.Status != DoctorWarn || c.Detail != want {
		t.Errorf("foreign next to an extension's: %s %q, want warn %q", c.Status, c.Detail, want)
	}
	if len(c.Items) != 2 {
		t.Errorf("items %q: both synchronous hooks, not the async one", c.Items)
	}
}

// Under node-plugins/ is not enough: without the node's install record, or outside the package, it is not an
// extension's hook.
func TestGlobalPermissionHooksNeedTheInstallRecord(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dataDir, prog string) string // returns the hook's program
	}{
		{"directory without the node's mark", func(t *testing.T, dataDir, prog string) string {
			rm(t, filepath.Join(dataDir, nodePluginsDir, "approver", pluginOwnMark))
			return prog
		}},
		{"version without the digest record", func(t *testing.T, dataDir, prog string) string {
			rm(t, filepath.Join(dataDir, nodePluginsDir, "approver", "1.4.0", ".persona-digest"))
			return prog
		}},
		{"digest record that is no digest", func(t *testing.T, dataDir, prog string) string {
			writeFiles(t, filepath.Join(dataDir, nodePluginsDir, "approver", "1.4.0"), map[string]string{".persona-digest": "hand-made\n"})
			return prog
		}},
		{"package without a manifest", func(t *testing.T, dataDir, prog string) string {
			rm(t, filepath.Join(dataDir, nodePluginsDir, "approver", "1.4.0", "persona-extension.json"))
			return prog
		}},
		{"program missing", func(t *testing.T, dataDir, prog string) string {
			rm(t, prog)
			return prog
		}},
		{"not a version directory", func(t *testing.T, dataDir, prog string) string {
			p := filepath.Join(mkdir(t, filepath.Join(dataDir, nodePluginsDir, "approver", "data", "bin")), "approve-hook")
			version := filepath.Join(dataDir, nodePluginsDir, "approver", "1.4.0")
			writeFiles(t, filepath.Dir(filepath.Dir(p)), map[string]string{
				".persona-digest":        string(mustRead(t, filepath.Join(version, ".persona-digest"))),
				"persona-extension.json": string(mustRead(t, filepath.Join(version, "persona-extension.json"))),
			})
			if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"a hub name, even with the files", func(t *testing.T, dataDir, prog string) string {
			from := filepath.Join(dataDir, nodePluginsDir, "approver")
			to := filepath.Join(dataDir, nodePluginsDir, "installed")
			if err := os.Rename(from, to); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(to, "1.4.0", "bin", "approve-hook")
		}},
		{"link out of the package", func(t *testing.T, dataDir, prog string) string {
			outside := filepath.Join(filepath.Dir(dataDir), "elsewhere-hook")
			if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			rm(t, prog)
			if err := os.Symlink(outside, prog); err != nil {
				if runtime.GOOS == "windows" {
					t.Skip("no symlinks here:", err)
				}
				t.Fatal(err)
			}
			return prog
		}},
		{"relative path", func(t *testing.T, dataDir, prog string) string {
			return filepath.Join(nodePluginsDir, "approver", "1.4.0", "bin", "approve-hook")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir, dataDir := hookRoots(t)
			prog := installedExtension(t, dataDir, "approver", "1.4.0", "Example Approver")
			writePermissionHooks(t, configDir, commandHook(shellQuote(tc.setup(t, dataDir, prog))))
			c := checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
			if c.Status != DoctorWarn || strings.Contains(c.Detail, "扩展") {
				t.Errorf("%s %q, want a warning about a foreign hook", c.Status, c.Detail)
			}
		})
	}
}

// The doctor itself looks in its own persona dir for the install record.
func TestDoctorGlobalPermissionHooksUsesTheNodeDir(t *testing.T) {
	e := newDoctorEnv(t)
	prog := installedExtension(t, e.opts.Dir, "approver", "1.4.0", "Example Approver")
	writePermissionHooks(t, e.opts.ClaudeConfigDir, commandHook(prog))
	c := wantStatus(t, RunDoctor(context.Background(), e.opts), "global-permission-hooks", DoctorOK, "")
	if !strings.Contains(c.Detail, "Example Approver") {
		t.Errorf("detail %q", c.Detail)
	}
}

func rm(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
