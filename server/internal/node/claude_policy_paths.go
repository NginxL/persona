package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// claudePolicyPaths are native policy layers whose presence changes what a bounded
// local integration can establish. Keep the paths shared by skill and model adapters.
func claudePolicyPaths(profile string) []string {
	policies := append([]string{filepath.Join(profile, "remote-settings.json"),
		"/etc/claude-code/managed-settings.json", "/etc/claude-code/managed-settings.d"}, claudeManagedSettings...)
	policies = append(policies, claudeManagedSettingsDirs...)
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		policies = append(policies, filepath.Join(programFiles, "ClaudeCode", "managed-settings.json"),
			filepath.Join(programFiles, "ClaudeCode", "managed-settings.d"))
	}
	return policies
}

// claudeManagedSettingsFiles are the managed-settings.json files an administrator's Claude Code policy may be in
// (Linux and WSL, macOS, Windows: claudePolicyPaths' file-based ones), each with its managed-settings.d beside it.
// A variable for the tests.
var claudeManagedSettingsFiles = func() []string {
	files := append([]string{"/etc/claude-code/managed-settings.json"}, claudeManagedSettings...)
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		files = append(files, filepath.Join(programFiles, "ClaudeCode", "managed-settings.json"))
	}
	return files
}

// claudeManagedEnvLayers are the managed settings documents whose env Claude Code applies over every other settings
// layer, --settings included, highest first: of each managed-settings.json, the *.json drop-ins of its
// managed-settings.d (merged after it in alphabetical order, so the last wins a key; hidden files skipped), then the
// file itself. MDM profiles and remote-settings.json (a server-policy cache that may hold credentials, read only for
// a session on Anthropic's own API) are not read.
func claudeManagedEnvLayers() []string {
	var out []string
	for _, file := range claudeManagedSettingsFiles() {
		var dropIns []string
		if ents, err := os.ReadDir(filepath.Join(filepath.Dir(file), "managed-settings.d")); err == nil {
			for _, e := range ents {
				if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
					dropIns = append(dropIns, filepath.Join(filepath.Dir(file), "managed-settings.d", name))
				}
			}
		}
		slices.Sort(dropIns)
		slices.Reverse(dropIns)
		out = append(append(out, dropIns...), file)
	}
	return out
}
