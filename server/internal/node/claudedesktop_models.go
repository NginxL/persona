package node

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Claude Desktop's model list (claude-desktop launcher and sessions of source claude_desktop):
//  1. When Claude / Claude-3p has an active gateway profile in configLibrary (_meta.json -> appliedId -> <appliedId>.json),
//     query <inferenceGatewayBaseUrl>/v1/models with that profile's credentials, filter by inferenceModels when non-empty,
//     apply labelOverride / display_name / supported_reasoning_levels, and mark the opus family default (or first entry)
//     as default — matching Claude Desktop's gateway profile behavior. Never expose or log inferenceGatewayApiKey.
//  2. If /v1/models is unreachable, fall back to static inferenceModels entries from the active profile.
//  3. When no configLibrary profile is active (first-party Claude Desktop login), ask the desktop CLI (or PATH claude)
//     via headless claudeModelList.

var desktopProfileIDRe = lazyre.New(`^[A-Za-z0-9-]+$`)

var desktopGatewayModelsTimeout = 5 * time.Second

type desktopConfigMeta struct {
	AppliedID string `json:"appliedId"`
}

type desktopConfiguredModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	LabelOverride string `json:"labelOverride"`
}

type desktopAppConfig struct {
	BaseURL string                   `json:"inferenceGatewayBaseUrl"`
	APIKey  string                   `json:"inferenceGatewayApiKey"`
	Models  []desktopConfiguredModel `json:"inferenceModels"`
}

func (c desktopAppConfig) findModel(id string) (desktopConfiguredModel, bool) {
	for _, m := range c.Models {
		if cmp.Or(m.ID, m.Name) == id {
			return m, true
		}
	}
	return desktopConfiguredModel{}, false
}

// activeDesktopAppConfig returns the active configLibrary gateway profile across Claude Desktop's app directories,
// preferring the most recently updated _meta.json when both Claude and Claude-3p exist.
func activeDesktopAppConfig(dirs []string) (cfg desktopAppConfig, fingerprint string, ok bool) {
	var newest time.Time
	for _, dir := range dirs {
		lib := filepath.Join(dir, "configLibrary")
		metaPath := filepath.Join(lib, "_meta.json")
		metaInfo, err := os.Stat(metaPath)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta desktopConfigMeta
		if json.Unmarshal(b, &meta) != nil || !desktopProfileIDRe.MatchString(meta.AppliedID) {
			continue
		}
		cfgPath := filepath.Join(lib, meta.AppliedID+".json")
		cfgInfo, err := os.Stat(cfgPath)
		if err != nil {
			continue
		}
		cb, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		var parsed desktopAppConfig
		if json.Unmarshal(cb, &parsed) != nil {
			continue
		}
		parsed.BaseURL = strings.TrimSpace(parsed.BaseURL)
		parsed.APIKey = strings.TrimSpace(parsed.APIKey)
		if parsed.BaseURL == "" || parsed.APIKey == "" {
			continue
		}
		modAt := metaInfo.ModTime()
		if cfgInfo.ModTime().After(modAt) {
			modAt = cfgInfo.ModTime()
		}
		if !ok || !modAt.Before(newest) {
			newest = modAt
			cfg = parsed
			fingerprint = fmt.Sprintf("%s:%s:%d:%d", dir, meta.AppliedID, metaInfo.ModTime().UnixNano(), cfgInfo.ModTime().UnixNano())
			ok = true
		}
	}
	return cfg, fingerprint, ok
}

func desktopClaudeExecutable(home string) (string, error) {
	if exe, err := desktopClaudeCLI(); err == nil {
		return exe, nil
	}
	for _, cli := range claudeDesktopCLIsIn(runtime.GOOS, claudeDesktopDirs(home)) {
		if _, _, err := desktopClaudeProgram(cli); err == nil {
			return cli, nil
		}
	}
	return "", fmt.Errorf("claude executable not found")
}

func (n *Node) claudeDesktopLauncherModels(ctx context.Context, refresh bool) (proto.LauncherModels, error) {
	cfg, fp, hasProfile := activeDesktopAppConfig(claudeDesktopDirs(n.home))
	key := n.home + "\x00" + claudeDesktopLauncher + "\x00" + fp
	launcherModelsCache.Lock()
	at, ok := launcherModelsCache.at[key]
	cached := launcherModelsCache.got[key]
	launcherModelsCache.Unlock()
	ask := func() (any, error) {
		res := proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{}}
		switch {
		case hasProfile:
			res.Models = n.fetchDesktopProfileModels(ctx, cfg)
		default:
			if models, err := n.listDesktopCLIModels(ctx); err == nil {
				res.Models = models
			}
		}
		launcherModelsCache.Lock()
		if len(launcherModelsCache.got) > 64 {
			launcherModelsCache.at, launcherModelsCache.got = map[string]time.Time{}, map[string]proto.LauncherModels{}
		}
		launcherModelsCache.at[key], launcherModelsCache.got[key] = time.Now(), res
		launcherModelsCache.Unlock()
		return res, nil
	}
	if ok && !refresh {
		switch age := time.Since(at); {
		case age < launcherModelsTTL:
			return cached, nil
		case age < launcherModelsStale:
			launcherModelsCache.flight.DoChan(key, ask)
			return cached, nil
		}
	}
	v, err, _ := launcherModelsCache.flight.Do(key, ask)
	if err != nil {
		return proto.LauncherModels{}, err
	}
	return v.(proto.LauncherModels), nil
}

func (n *Node) fetchDesktopProfileModels(ctx context.Context, cfg desktopAppConfig) []proto.AgentModel {
	if models := n.queryDesktopGatewayModels(ctx, cfg); len(models) > 0 {
		return models
	}
	return staticDesktopProfileModels(cfg)
}

func (n *Node) queryDesktopGatewayModels(ctx context.Context, cfg desktopAppConfig) []proto.AgentModel {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" || cfg.APIKey == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), desktopGatewayModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "claude-desktop")
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	resp, err := gatewayClient(env).Do(req)
	if err != nil {
		n.logf("launcher %s: desktop gateway model list: %v", claudeDesktopLauncher, redactSecrets(err.Error()))
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		n.logf("launcher %s: desktop gateway model list answered %d", claudeDesktopLauncher, resp.StatusCode)
		return nil
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"display_name"`
			Description   string `json:"description"`
			FamilyDefault bool   `json:"is_family_default"`
			FamilyTier    string `json:"anthropic_family_tier"`
			Reasoning     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
		return nil
	}
	var out []proto.AgentModel
	defaultIdx := -1
	for _, m := range body.Data {
		if m.ID == "" {
			continue
		}
		entry, configured := cfg.findModel(m.ID)
		if len(cfg.Models) > 0 && !configured {
			continue
		}
		var efforts []string
		for _, r := range m.Reasoning {
			if slices.Contains(proto.EffortLevels, r.Effort) && !slices.Contains(efforts, r.Effort) {
				efforts = append(efforts, r.Effort)
			}
		}
		defEffort := ""
		if len(efforts) > 0 {
			defEffort = efforts[0]
			if slices.Contains(efforts, "high") {
				defEffort = "high"
			}
		}
		label := cmp.Or(strings.TrimSpace(entry.LabelOverride), strings.TrimSpace(m.DisplayName), m.ID)
		if defaultIdx < 0 && m.FamilyDefault && m.FamilyTier == "opus" {
			defaultIdx = len(out)
		}
		out = append(out, proto.AgentModel{
			ID:            m.ID,
			Name:          label,
			Description:   strings.TrimSpace(m.Description),
			Efforts:       efforts,
			DefaultEffort: defEffort,
		})
	}
	if len(out) == 0 {
		return nil
	}
	if defaultIdx < 0 {
		defaultIdx = 0
	}
	out[defaultIdx].Default = true
	return out
}

func staticDesktopProfileModels(cfg desktopAppConfig) []proto.AgentModel {
	out := make([]proto.AgentModel, 0, len(cfg.Models))
	seen := map[string]bool{}
	for _, m := range cfg.Models {
		id := strings.TrimSpace(cmp.Or(m.ID, m.Name))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		label := cmp.Or(strings.TrimSpace(m.LabelOverride), strings.TrimSpace(m.Name), id)
		out = append(out, proto.AgentModel{
			ID:            id,
			Name:          label,
			Efforts:       slices.Clone(proto.EffortLevels),
			DefaultEffort: "high",
		})
	}
	if len(out) > 0 {
		out[0].Default = true
	}
	return out
}

func (n *Node) listDesktopCLIModels(ctx context.Context) ([]proto.AgentModel, error) {
	exe, err := desktopClaudeExecutable(n.home)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "persona-desktop-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ours := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(ours, []byte(`{"disableAllHooks":true}`), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launcherModelsTimeout)
	defer cancel()
	program, prefix, err := desktopClaudeProgram(exe)
	if err != nil {
		return nil, err
	}
	argv := append(prefix, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--settings", ours)
	cmd := exec.CommandContext(ctx, program, argv...)
	cmd.Dir = dir
	cmd.Env = desktopSeedEnv(os.Environ())
	killGroupOnCancel(cmd)
	prepareDesktopChild(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = cmd.Cancel()
		_ = cmd.Wait()
	}()
	type result struct {
		models []proto.AgentModel
		err    error
	}
	done := make(chan result, 1)
	go func() {
		models, err := claudeModelList(stdin, stdout)
		done <- result{models, err}
	}()
	select {
	case r := <-done:
		return r.models, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
