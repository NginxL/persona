package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantskills"
)

type skillCatalogRequest struct {
	Owner     string `json:"skill_owner"`
	Machine   string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Launcher  string `json:"launcher"`
	Runtime   string `json:"runtime"`
	ConfigDir string `json:"config_dir"`
	Scope     string `json:"-"` // additional node-derived ordinary session identity
}

type skillOwnerKey struct{}

func withSkillOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, skillOwnerKey{}, owner)
}

// skillCatalog uses the same launcher/profile selection as assistantSend. The manager supplies cwd,
// not a request's arbitrary filesystem path. External runtimes need their own inventory contract.
func (n *Node) skillCatalog(ctx context.Context, in skillCatalogRequest) (assistantskills.Catalog, error) {
	out := assistantskills.Catalog{Version: 1, Skills: []assistantskills.Entry{}}
	if in.Owner == "" || in.Cwd == "" || !filepath.IsAbs(in.Cwd) {
		return out, errors.New("skill catalog requires an assistant execution environment")
	}
	if n.runtimeAssistant(in.SessionID) || n.assistantRuntime(in.Launcher) != nil || n.guestOf(in.SessionID) != nil {
		return out, errors.New("skill selection is unavailable for this runtime")
	}
	if in.Launcher == "" {
		in.Launcher = "claude"
	}
	agent, err := n.assistantAgent(ctx, in.Launcher)
	if err != nil {
		return out, err
	}
	var entries []assistantskills.Entry
	switch agent {
	case "codex":
		entries, out.Complete, err = n.codexSkillCatalog(ctx, in)
	case "claude":
		in.ConfigDir, err = n.assistantProfileSelection(ctx, in.SessionID, in.Launcher, in.Runtime, in.ConfigDir, in.SessionID != "")
		if err != nil {
			return out, err
		}
		profile := n.assistantConfigDir(in.Launcher, in.ConfigDir)
		if profile == "" {
			profile = filepath.Join(n.home, ".claude")
		}
		roots := []assistantskills.Root{{Path: filepath.Join(profile, "skills"), Source: "user"}}
		for dir, depth := filepath.Clean(in.Cwd), 0; depth < 64; depth++ {
			rel, _ := filepath.Rel(in.Cwd, dir)
			roots = append(roots, assistantskills.Root{Path: filepath.Join(dir, ".claude", "skills"), Source: "project · " + rel})
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		// Bounded local roots, not a claim to enumerate bundled, remote or plugin-only commands.
		entries, err = assistantskills.LocalCatalog(roots)
		if err == nil {
			var own map[string]any
			if settings := n.launcherOwnSettings(ctx, in.Launcher); settings != "" {
				own, err = readLauncherSettings(settings, in.Cwd, n.home)
				if err != nil {
					return out, errors.New("cannot establish the launcher's skill settings")
				}
			}
			entries, err = visibleClaudeSkills(entries, profile, in.Cwd, own)
		}
	default:
		err = errors.New("skill selection is unavailable for this runtime")
	}
	if err != nil {
		return out, err
	}
	identity := []string{in.Owner, in.Machine, in.Cwd, in.Launcher, in.Runtime, in.ConfigDir,
		n.executionFingerprint(in.Launcher, modelExecution{Runtime: in.Runtime})}
	if in.Scope != "" {
		identity = append(identity, in.Scope)
	}
	scope, _ := json.Marshal(identity)
	out.Skills = assistantskills.Bind(string(scope), entries)
	return out, nil
}

func visibleClaudeSkills(entries []assistantskills.Entry, profile, cwd string, launcherSettings ...map[string]any) ([]assistantskills.Entry, error) {
	// A bounded local adapter cannot establish the effective managed policy. Check presence only:
	// never execute a policy helper or read a server-policy cache that may contain credentials.
	policies := append([]string{filepath.Join(profile, "remote-settings.json"),
		"/etc/claude-code/managed-settings.json", "/etc/claude-code/managed-settings.d"}, claudeManagedSettings...)
	policies = append(policies, claudeManagedSettingsDirs...)
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		policies = append(policies, filepath.Join(programFiles, "ClaudeCode", "managed-settings.json"),
			filepath.Join(programFiles, "ClaudeCode", "managed-settings.d"))
	}
	for _, file := range policies {
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("skill catalog is unavailable while a managed policy may apply")
		}
	}
	layers := []map[string]any{}
	for _, file := range []string{filepath.Join(profile, "settings.json"), filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json")} {
		settings, err := readSettingsFile(file)
		if err != nil {
			return nil, err
		}
		layers = append(layers, settings)
	}
	overrides := map[string]string{}
	for _, settings := range append(layers, launcherSettings...) {
		strict, _ := settings["strictPluginOnlyCustomization"].([]any)
		enabled, _ := settings["strictPluginOnlyCustomization"].(bool)
		if enabled || len(strict) > 0 {
			// Do not bypass policies whose effective roots this bounded adapter cannot establish.
			return nil, errors.New("skill catalog is unavailable under this customization policy")
		}
		if permissions, _ := settings["permissions"].(map[string]any); permissions != nil {
			for _, mode := range []string{"deny", "ask"} {
				rules, _ := permissions[mode].([]any)
				for _, value := range rules {
					rule, _ := value.(string)
					if rule == "Skill" || strings.HasPrefix(rule, "Skill(") {
						// Exact-file instructions must not work around a native Skill gate. Refuse
						// the bounded adapter rather than guess its rule/approval semantics.
						return nil, errors.New("skill catalog is unavailable under this skill permission policy")
					}
				}
			}
		}
		if values, _ := settings["skillOverrides"].(map[string]any); values != nil {
			for key, value := range values {
				overrides[key], _ = value.(string)
			}
		}
	}
	out := entries[:0]
	for _, e := range entries {
		if overrides[e.Name] != "off" {
			out = append(out, e)
		}
	}
	return out, nil
}

func (n *Node) selectedSkills(ctx context.Context, id, cwd, launcher, text string, execution modelExecution) ([]assistantskills.Entry, error) {
	refs, err := assistantskills.References(text)
	if err != nil {
		return nil, skillSelectionError(err)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	owner, _ := ctx.Value(skillOwnerKey{}).(string)
	n.mu.Lock()
	machine := n.machineID
	n.mu.Unlock()
	catalog, err := n.skillCatalog(ctx, skillCatalogRequest{Owner: owner, Machine: machine, SessionID: id, Cwd: cwd,
		Launcher: launcher, Runtime: execution.Runtime, ConfigDir: execution.ConfigDir})
	if err != nil {
		return nil, skillSelectionError(err)
	}
	selected, err := assistantskills.Resolve(text, catalog.Skills)
	if err != nil {
		return nil, skillSelectionError(err)
	}
	return selected, nil
}

func skillSelectionError(err error) error {
	return refusal("skill_unavailable", err.Error(), nil)
}
