package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// claudeDesktopEnvironment is a saved local environment from Claude's native
// catalog. Machine environments are scoped to an account, not an organization;
// OrgID remains empty for this source.
type claudeDesktopEnvironment struct {
	ID, Name, AccountID, OrgID string
	Cwd                        string
	AdditionalFolders          []string
}

const maxClaudeDesktopEnvironmentBytes = 4 << 20

// readClaudeDesktopEnvironments reads Claude's machine catalog mirror only. It
// does not read renderer storage or the app config, which contains credentials.
// A missing file returns nil; a valid empty catalog returns a non-nil slice.
// The mirror is suitable for discovery, but is not a safe write interface: the
// running renderer owns the catalog and can replace the complete mirror.
func readClaudeDesktopEnvironments(dataDir string) ([]claudeDesktopEnvironment, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("Claude desktop data directory must be absolute")
	}
	f, err := os.Open(filepath.Join(dataDir, "ccd-session-presets.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Claude desktop environments: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxClaudeDesktopEnvironmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Claude desktop environments: %w", err)
	}
	if len(b) > maxClaudeDesktopEnvironmentBytes {
		return nil, errors.New("Claude desktop environment catalog exceeds 4 MiB")
	}
	return parseClaudeDesktopEnvironments(b)
}

func parseClaudeDesktopEnvironments(b []byte) ([]claudeDesktopEnvironment, error) {
	// Electron's mirror uses {byAccount:{accountUuid:{presets:[...]}}}; the
	// renderer's versioned {state:...} localStorage envelope is a different source.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse Claude desktop environments: %w", err)
	}
	if raw != nil && len(raw) == 0 {
		return make([]claudeDesktopEnvironment, 0), nil // newly initialized Electron store
	}
	var byAccount map[string]json.RawMessage
	if json.Unmarshal(raw["byAccount"], &byAccount) != nil || byAccount == nil {
		return nil, errors.New("Claude desktop environment catalog has no byAccount map")
	}
	accounts := make([]string, 0, len(byAccount))
	for account := range byAccount {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	out := make([]claudeDesktopEnvironment, 0)
	for _, account := range accounts {
		if strings.TrimSpace(account) == "" {
			continue
		}
		var catalog struct {
			Presets []json.RawMessage `json:"presets"`
		}
		if json.Unmarshal(byAccount[account], &catalog) != nil || catalog.Presets == nil {
			continue
		}
		seen := make(map[string]bool)
		for _, row := range catalog.Presets {
			var preset struct {
				ID         string   `json:"id"`
				Name       string   `json:"name"`
				CreatedAt  *float64 `json:"createdAt"`
				LastUsedAt *float64 `json:"lastUsedAt"`
				Target     struct {
					Kind              string   `json:"kind"`
					Cwd               string   `json:"cwd"`
					WSLDistro         string   `json:"wslDistro"`
					AdditionalFolders []string `json:"additionalFolders"`
				} `json:"target"`
			}
			if json.Unmarshal(row, &preset) != nil || strings.TrimSpace(preset.ID) == "" || len(preset.ID) > 128 ||
				preset.CreatedAt == nil || preset.LastUsedAt == nil || preset.Target.Kind != "local" ||
				preset.Target.WSLDistro != "" || !filepath.IsAbs(preset.Target.Cwd) {
				continue
			}
			name := strings.TrimSpace(preset.Name)
			if name == "" || seen[preset.ID] {
				continue
			}
			seen[preset.ID] = true
			env := claudeDesktopEnvironment{ID: preset.ID, Name: name, AccountID: account, Cwd: filepath.Clean(preset.Target.Cwd)}
			folders := map[string]bool{claudeDesktopEnvironmentPathKey(env.Cwd): true}
			for _, folder := range preset.Target.AdditionalFolders {
				if !filepath.IsAbs(folder) {
					continue
				}
				folder = filepath.Clean(folder)
				key := claudeDesktopEnvironmentPathKey(folder)
				if !folders[key] {
					folders[key] = true
					env.AdditionalFolders = append(env.AdditionalFolders, folder)
				}
			}
			out = append(out, env)
		}
	}
	return out, nil
}

func claudeDesktopEnvironmentPathKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
