package node

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/prompts"
)

// sessionSkillCatalog derives the execution environment from the target, never from client paths or launchers.
// A catalog read does not join, resume or start a conversation.
func (n *Node) sessionSkillCatalog(ctx context.Context, owner, id string) (assistantskills.Catalog, error) {
	unavailable := func() (assistantskills.Catalog, error) {
		return assistantskills.Catalog{}, skillSelectionError(assistantskills.ErrReference)
	}
	if owner == "" || id == "" || n.isAssistantSession(id) || n.runtimeAssistant(id) || n.guestOf(id) != nil {
		return unavailable()
	}
	if c, t, err := n.codexOwner(id); err == nil {
		if !filepath.IsAbs(t.cwd) {
			return unavailable()
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var out codexSkillList
		if err := c.wire.call(ctx, "skills/list", map[string]any{"cwds": []string{t.cwd}, "forceReload": true}, &out); err != nil {
			return unavailable()
		}
		entries, complete, err := n.codexSkillEntries(t.cwd, out)
		if err != nil {
			return unavailable()
		}
		// The exact connection (and its actual CODEX_HOME) is the execution boundary. Never probe another launcher.
		scope, _ := json.Marshal([]string{owner, n.machineID, id, t.cwd, c.spec.Home, c.generation})
		return assistantskills.Catalog{Version: 1, Complete: complete, Skills: assistantskills.Bind(string(scope), entries)}, nil
	}
	a, live := n.agent(id)
	if !live || a.Agent != "claude" || !filepath.IsAbs(a.Cwd) || n.pendingSwitch(id) != nil {
		return unavailable()
	}
	h, _, err := n.paneHost(a.PaneID)
	if err != nil || h.Kind() != host.Herdr && h.Kind() != host.HerdrCore && h.Kind() != host.Orca {
		return unavailable() // an external runtime needs its own catalog and delivery contract
	}
	st, _ := n.state.get(id)
	launcher := st.Launcher
	if launcher == "" {
		launcher = n.inferLauncher(id)
	}
	if launcher == "" {
		return unavailable()
	}
	catalog, err := n.skillCatalog(ctx, skillCatalogRequest{Owner: owner, Machine: n.machineID, SessionID: id,
		Cwd: a.Cwd, Launcher: launcher, Scope: id})
	if err != nil {
		return unavailable()
	}
	return catalog, nil
}

func (n *Node) sessionSelectedSkills(ctx context.Context, id, text string) ([]assistantskills.Entry, error) {
	refs, err := assistantskills.References(text)
	if err != nil {
		return nil, skillSelectionError(err)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	owner, _ := ctx.Value(skillOwnerKey{}).(string)
	catalog, err := n.sessionSkillCatalog(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	selected, err := assistantskills.Resolve(text, catalog.Skills)
	if err != nil {
		return nil, skillSelectionError(err)
	}
	return selected, nil
}

func selectedSkillInstructions(entries []assistantskills.Entry) string {
	skills := make([]prompts.AssistantSelectedSkill, len(entries))
	for i, e := range entries {
		skills[i] = prompts.AssistantSelectedSkill{Name: e.Name, Path: e.Path, Ref: e.ID}
	}
	return prompts.RenderAssistantSelectedSkills(skills)
}
