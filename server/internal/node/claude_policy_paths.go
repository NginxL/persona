package node

import (
	"os"
	"path/filepath"
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
