package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

type skillCatalogRequest struct {
	Owner     string `json:"skill_owner"`
	Machine   string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Launcher  string `json:"launcher"`
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
	if n.guestOf(in.SessionID) != nil {
		return out, errors.New("skill selection is unavailable for this runtime")
	}
	var (
		entries []assistantskills.Entry
		err     error
	)
	if agyLauncher, isRuntime, rerr := n.agySkillRuntime(in); isRuntime {
		if rerr != nil {
			return out, rerr
		}
		if in.ConfigDir != "" {
			return out, errors.New(assistantwire.ConfigDirClaudeOnly)
		}
		in.Launcher = agyLauncher
		entries, err = n.agySkillCatalog(in)
	} else {
		if in.Launcher == "" && in.SessionID != "" && n.state != nil {
			if st, ok := n.state.get(in.SessionID); ok && st.Launcher != "" &&
				n.assistantRuntime(st.Launcher) == nil && n.runtimeOf(st.Launcher) == nil {
				if m, ok := n.managedLauncher(st.Launcher); !ok || m.AgentType() != agyAgent {
					in.Launcher = st.Launcher
				}
			}
		}
		if in.Launcher == "" {
			in.Launcher = "claude"
		}
		agent, agentErr := n.assistantAgent(ctx, in.Launcher)
		if agentErr != nil {
			return out, agentErr
		}
		switch agent {
		case "codex":
			entries, out.Complete, err = n.codexSkillCatalog(ctx, in)
		case "claude":
			in.ConfigDir, err = n.assistantProfileSelection(ctx, in.SessionID, in.Launcher, in.ConfigDir, in.SessionID != "")
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
				// The user's skill switches a private profile carries (assistant_user_setup.go) come before the launcher's.
				entries, err = visibleClaudeSkills(entries, profile, in.Cwd, map[string]any{"skillOverrides": n.userSkillsOff(profile, in.Cwd)}, own)
			}
		default:
			err = errors.New("skill selection is unavailable for this runtime")
		}
	}
	if err != nil {
		return out, err
	}
	// The empty fifth word keeps a CLI conversation's scope, and so each skill's id, what an older node gave: a skill
	// picked from its catalog still resolves here.
	identity := []string{in.Owner, in.Machine, in.Cwd, in.Launcher, "", in.ConfigDir,
		n.executionFingerprint(in.Launcher, modelExecution{})}
	if in.Scope != "" {
		identity = append(identity, in.Scope)
	}
	scope, _ := json.Marshal(identity)
	out.Skills = assistantskills.Bind(string(scope), entries)
	return out, nil
}

// claudeLinuxManagedSettings and claudeLinuxManagedSettingsDirs are claudeManagedSettings and claudeManagedSettingsDirs
// on Linux and WSL.
var (
	claudeLinuxManagedSettings     = []string{"/etc/claude-code/managed-settings.json"}
	claudeLinuxManagedSettingsDirs = []string{"/etc/claude-code/managed-settings.d"}
)

// readClaudeManagedSettings reads the administrator's settings files for Claude Code on every system, each system's in
// the order Claude Code merges them: managed-settings.json, then the *.json files in managed-settings.d alphabetically,
// hidden ones left out. A missing file or directory holds no policy; one that cannot be read, or holds no JSON object, is
// an error.
func readClaudeManagedSettings() ([]map[string]any, error) {
	systems := [][2][]string{{claudeManagedSettings, claudeManagedSettingsDirs}, {claudeLinuxManagedSettings, claudeLinuxManagedSettingsDirs}}
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		root := filepath.Join(programFiles, "ClaudeCode")
		systems = append(systems, [2][]string{{filepath.Join(root, "managed-settings.json")}, {filepath.Join(root, "managed-settings.d")}})
	}
	var out []map[string]any
	for _, system := range systems {
		files := append([]string{}, system[0]...)
		for _, dir := range system[1] {
			entries, err := os.ReadDir(dir) // sorted by name
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if name := e.Name(); strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
					files = append(files, filepath.Join(dir, name))
				}
			}
		}
		for _, file := range files {
			// readSettingsFile gives a missing file as an empty map (no policy) and nil only for a file that holds null.
			settings, err := readSettingsFile(file)
			if err == nil && settings == nil {
				err = errors.New(file + " is not a JSON object")
			}
			if err != nil {
				return nil, err
			}
			out = append(out, settings)
		}
	}
	return out, nil
}

func visibleClaudeSkills(entries []assistantskills.Entry, profile, cwd string, launcherSettings ...map[string]any) ([]assistantskills.Entry, error) {
	// Never read the server-policy cache, which may contain credentials: its presence alone refuses.
	if _, err := os.Stat(filepath.Join(profile, "remote-settings.json")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("skill catalog is unavailable while a managed policy may apply")
	}
	// The administrator's files go through the same checks as every other layer below (#9862), so one that only sets
	// hooks restricts nothing. Failing to read one refuses, as does a policy helper: its output would replace them, and
	// this adapter never runs it.
	managed, err := readClaudeManagedSettings()
	if err != nil {
		return nil, errors.New("skill catalog is unavailable while a managed policy cannot be read")
	}
	for _, settings := range managed {
		if settings["policyHelper"] != nil {
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
	// Lowest first, so a later layer's skillOverrides entry replaces an earlier one's and the managed ones, last, win.
	for _, settings := range append(append(layers, launcherSettings...), managed...) {
		// true or a non-empty list; a value Claude Code cannot read may lock too.
		strict := settings["strictPluginOnlyCustomization"]
		if list, ok := strict.([]any); strict != nil && strict != false && (!ok || len(list) > 0) {
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
				if state, ok := value.(string); ok { // Claude Code drops an entry it cannot read
					overrides[key] = state
				}
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
		Launcher: launcher, ConfigDir: execution.ConfigDir})
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
