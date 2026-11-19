package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// assistantBypassHookTags are the stable ownership markers written by
// agent-bypass-allow (and its former public ID). Do not import a permission
// hook merely because it happens to be in the user's default directory.
var assistantBypassHookTags = []string{
	"--persona-extension=agent-bypass-allow",
	"--persona-extension=claude-bypass-allow",
}

// assistantProfileWrite is the seed's atomic write, replaced by the retry test.
var assistantProfileWrite = writeFileAtomic

// prepareAssistantProfile creates the selected profile and serializes the
// first-start seed for all conversations using the same launcher. A nonempty
// existing profile is left alone. An empty one is retried: it has no local
// rules or other user state, and may be left by an earlier failed start.
func (n *Node) prepareAssistantProfile(configPath string) error {
	n.profileSeedMu.Lock()
	defer n.profileSeedMu.Unlock()
	_, statErr := os.Stat(configPath)
	created := errors.Is(statErr, os.ErrNotExist)
	if err := os.MkdirAll(configPath, 0o700); err != nil {
		return err
	}
	if !created {
		entries, err := os.ReadDir(configPath)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return nil
		}
	}
	if err := n.seedAssistantBypassHook(configPath); err != nil {
		if created {
			_ = os.Remove(configPath) // empty after a failed atomic write: the next start can claim it again
		}
		return err // abort this start; a later one retries instead of running unprotected and making the profile nonempty
	}
	return nil
}

// seedAssistantBypassHook gives a newly created assistant profile the
// extension's existing opt-in entry before Claude reads hooks at first startup.
// The extension owns later reconciliation and rewrites the entry with the new
// profile's --config-dir. Until then the source directory is the right policy
// context: this directory was just created and has no local ask/deny rules.
//
// Nonempty existing profiles never come here (prepareAssistantProfile). No
// readable source entry means no file. The default directory and ~/.claude are
// authoritative: an unreadable one aborts the start when no later source supplies
// the tagged opt-in. An unrelated profile or session directory is logged and
// skipped instead, so its unrelated typo cannot block every new profile.
// Only the tagged entries are copied: other hooks, credentials, modes and
// settings stay out of the private profile, and the source file is not changed.
func (n *Node) seedAssistantBypassHook(configPath string) error {
	var sourceReadErr error
	defaultDir := n.defaultClaudeDir()
	legacyDir := filepath.Join(n.home, ".claude")
	for _, dir := range n.claudeConfigDirs() {
		settings, err := readSettingsFile(settingsFile(dir))
		if err != nil {
			if dir != defaultDir && dir != legacyDir {
				n.logger.Printf("assistant profile seed: ignoring unrelated source %s: %v", settingsFile(dir), err)
				continue
			}
			if sourceReadErr == nil {
				sourceReadErr = err
			}
			continue
		}
		groups := taggedAssistantBypassGroups(settings)
		if len(groups) == 0 {
			continue
		}
		body, err := json.MarshalIndent(map[string]any{
			"hooks": map[string]any{"PermissionRequest": groups},
		}, "", "  ")
		if err != nil {
			return err
		}
		return assistantProfileWrite(filepath.Join(configPath, "settings.json"), append(body, '\n'))
	}
	return sourceReadErr
}

// taggedAssistantBypassGroups clones only the groups and entries belonging to
// the extension. A user's unrelated hook beside one in the same matcher group
// is neither copied nor removed from the source.
func taggedAssistantBypassGroups(settings map[string]any) []any {
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["PermissionRequest"].([]any)
	var out []any
	for _, raw := range groups {
		group, _ := raw.(map[string]any)
		entries, _ := group["hooks"].([]any)
		var owned []any
		for _, rawEntry := range entries {
			entry, _ := rawEntry.(map[string]any)
			command, _ := entry["command"].(string)
			if isAssistantBypassHookCommand(command) {
				owned = append(owned, rawEntry)
			}
		}
		if len(owned) == 0 {
			continue
		}
		clone := make(map[string]any, len(group))
		for key, value := range group {
			clone[key] = value
		}
		clone["hooks"] = owned
		out = append(out, clone)
	}
	return out
}

func isAssistantBypassHookCommand(command string) bool {
	command = strings.TrimSpace(command)
	for _, tag := range assistantBypassHookTags {
		if strings.HasSuffix(command, tag) {
			return true
		}
	}
	return false
}
