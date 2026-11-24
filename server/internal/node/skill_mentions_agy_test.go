package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

func agyAssistantHello() map[string]any {
	hello := assistantRuntimeHello()
	hello["runtime"], hello["name"], hello["agent"], hello["launcher"] = agyRuntimeID, "Antigravity", agyAgent, agyAgent
	return hello
}

func TestAgySkillCatalogDiscoveryPrecedenceAndDedup(t *testing.T) {
	n, _ := runtimeNode(t)
	_ = dialRuntime(t, n, agyAssistantHello())
	agyConfig := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", agyConfig)

	// Build a repository tree: <home>/outer/.agents/skills (outside repo) -> <home>/outer/repo (.git) -> <home>/outer/repo/sub
	outer := filepath.Join(n.home, "outer")
	repo := filepath.Join(outer, "repo")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Skill outside the .git repo root must NOT be discovered.
	writeFile(t, filepath.Join(outer, ".agents", "skills", "outside-repo", "SKILL.md"),
		"---\nname: outside-repo\ndescription: Must not leak past repo root\n---\nInstructions\n")
	// Workspace ancestor skill (repo root) + workspace skill (sub) overriding same-name skills.
	writeFile(t, filepath.Join(repo, ".agents", "skills", "review", "SKILL.md"),
		"---\nname: review\ndescription: Repo root review\n---\nInstructions\n")
	writeFile(t, filepath.Join(repo, ".agents", "skills", "repo-only", "SKILL.md"),
		"---\nname: repo-only\ndescription: Repo skill\n---\nInstructions\n")
	subReview := filepath.Join(sub, ".agents", "skills", "review", "SKILL.md")
	writeFile(t, subReview,
		"---\nname: review\ndescription: Subdir review wins\n---\nInstructions\n")
	// Non-user-invocable skill must be excluded.
	writeFile(t, filepath.Join(sub, ".agents", "skills", "internal", "SKILL.md"),
		"---\nname: internal\nuser-invocable: false\ndescription: Hidden\n---\nInstructions\n")

	// User-level skills (~/.agents/skills and ~/.gemini/config/skills).
	writeFile(t, filepath.Join(n.home, ".agents", "skills", "review", "SKILL.md"),
		"---\nname: review\ndescription: User review\n---\nInstructions\n")
	userTool := filepath.Join(n.home, ".agents", "skills", "user-tool", "SKILL.md")
	writeFile(t, userTool,
		"---\nname: user-tool\ndescription: From ~/.agents/skills\n---\nInstructions\n")
	writeFile(t, filepath.Join(n.home, ".gemini", "config", "skills", "user-tool", "SKILL.md"),
		"---\nname: user-tool\ndescription: Lower-priority gemini config duplicate\n---\nInstructions\n")
	geminiTool := filepath.Join(n.home, ".gemini", "config", "skills", "gemini-global", "SKILL.md")
	writeFile(t, geminiTool,
		"---\nname: gemini-global\ndescription: From ~/.gemini/config/skills\n---\nInstructions\n")

	// Builtin agy skills (<ANTIGRAVITY_CLI_CONFIG_DIR>/builtin/skills).
	writeFile(t, filepath.Join(agyConfig, "builtin", "skills", "user-tool", "SKILL.md"),
		"---\nname: user-tool\ndescription: Builtin shadowed by user\n---\nInstructions\n")
	builtinWorkflow := filepath.Join(agyConfig, "builtin", "skills", "run-workflow", "SKILL.md")
	writeFile(t, builtinWorkflow,
		"---\nname: run-workflow\ndescription: Builtin workflow\n---\nInstructions\n")

	catalog, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:    "helper-a",
		Machine:  n.machineID,
		Cwd:      sub,
		Launcher: agyAgent,
	})
	if err != nil {
		t.Fatalf("agy skillCatalog: %v", err)
	}
	if catalog.Version != 1 || catalog.Complete {
		t.Fatalf("bounded local agy catalog metadata: %+v", catalog)
	}

	byName := map[string]assistantskills.Entry{}
	for _, s := range catalog.Skills {
		if _, dup := byName[s.Name]; dup {
			t.Fatalf("duplicate skill name %q in catalog: %+v", s.Name, catalog.Skills)
		}
		byName[s.Name] = s
	}
	if _, leaked := byName["outside-repo"]; leaked {
		t.Fatalf("skill outside .git root leaked into catalog: %+v", byName["outside-repo"])
	}
	if _, hidden := byName["internal"]; hidden {
		t.Fatalf("non-invocable skill appeared in catalog: %+v", byName["internal"])
	}
	want := []struct {
		name, desc, source, path string
	}{
		{"review", "Subdir review wins", "project · .", resolved(t, subReview)},
		{"repo-only", "Repo skill", "project · ..", resolved(t, filepath.Join(repo, ".agents", "skills", "repo-only", "SKILL.md"))},
		{"user-tool", "From ~/.agents/skills", "user", resolved(t, userTool)},
		{"gemini-global", "From ~/.gemini/config/skills", "user", resolved(t, geminiTool)},
		{"run-workflow", "Builtin workflow", "builtin", resolved(t, builtinWorkflow)},
	}
	if len(catalog.Skills) != len(want) {
		t.Fatalf("got %d skills (%+v), want %d", len(catalog.Skills), catalog.Skills, len(want))
	}
	for _, w := range want {
		got, ok := byName[w.name]
		if !ok {
			t.Fatalf("missing skill %q from %+v", w.name, catalog.Skills)
		}
		if got.Description != w.desc || got.Source != w.source || got.Path != w.path || got.ID == "" {
			t.Fatalf("skill %q = %+v, want desc=%q source=%q path=%q", w.name, got, w.desc, w.source, w.path)
		}
	}

	// Non-git assistant directory under n.home stops walking before n.home so ~/.agents/skills keeps Source="user".
	asstDir := filepath.Join(n.home, ".persona", "assistant")
	asstSkill := filepath.Join(asstDir, ".agents", "skills", "persona-office", "SKILL.md")
	writeFile(t, asstSkill, "---\nname: persona-office\ndescription: Office\n---\nInstructions\n")
	asstCatalog, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:    "helper-a",
		Machine:  n.machineID,
		Cwd:      asstDir,
		Launcher: agyAgent,
	})
	if err != nil {
		t.Fatalf("assistant dir agy catalog: %v", err)
	}
	asstByName := map[string]assistantskills.Entry{}
	for _, s := range asstCatalog.Skills {
		asstByName[s.Name] = s
	}
	if got := asstByName["persona-office"]; got.Source != "project · ." || got.Path != resolved(t, asstSkill) {
		t.Fatalf("assistant dir skill = %+v", got)
	}
	if got := asstByName["user-tool"]; got.Source != "user" {
		t.Fatalf("home skill mislabeled as project ancestor: %+v", got)
	}

	// Unreadable root (file where skills directory is expected) fails closed instead of reporting empty.
	badCwd := t.TempDir()
	writeFile(t, filepath.Join(badCwd, ".agents", "skills"), "not a directory")
	if _, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:    "helper-a",
		Machine:  n.machineID,
		Cwd:      badCwd,
		Launcher: agyAgent,
	}); err == nil {
		t.Fatal("unreadable agy skill root succeeded")
	}
}

func TestAgySkillCatalogScopeIsolationAndManagedLauncher(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, agyAssistantHello())
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", t.TempDir())
	n.setManaged([]proto.ManagedLauncher{{Name: "my-agy", Agent: agyAgent}})

	cwd := filepath.Join(n.home, ".persona", "assistant")
	file := filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Inspect boundaries\n---\nInstructions\n")

	req := skillCatalogRequest{Owner: "helper-a", Machine: n.machineID, Cwd: cwd, Launcher: agyAgent}
	catalog, err := n.skillCatalog(context.Background(), req)
	if err != nil || len(catalog.Skills) != 1 {
		t.Fatalf("agy catalog: %+v %v", catalog, err)
	}
	text := `<persona-context kind="skill" title="review" ref="` + catalog.Skills[0].ID + `">project</persona-context>` + "\nReview this"

	// Scope binding must reject crossing owner, machine, cwd, or launcher boundaries.
	for _, boundary := range []string{"owner", "machine", "cwd", "launcher"} {
		other := req
		switch boundary {
		case "owner":
			other.Owner = "helper-b"
		case "machine":
			other.Machine = "other-machine"
		case "cwd":
			other.Cwd = t.TempDir()
			writeFile(t, filepath.Join(other.Cwd, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\n---\nOther\n")
		case "launcher":
			other.Launcher = "my-agy"
		}
		changed, err := n.skillCatalog(context.Background(), other)
		if err != nil {
			t.Fatalf("%s catalog: %v", boundary, err)
		}
		if _, err := assistantskills.Resolve(text, changed.Skills); err == nil {
			t.Fatalf("agy selection crossed %s boundary", boundary)
		}
	}

	// Non-empty Claude profile config_dir is rejected on agy.
	withProfile := req
	withProfile.ConfigDir = "private"
	if _, err := n.skillCatalog(context.Background(), withProfile); err == nil {
		t.Fatal("agy accepted Claude-only config_dir")
	}

	// Existing agy assistant session: GET /assistant/skills omits launcher when SourceKind == runtime,
	// and must produce the same bound IDs as send-time resolution (even if session state was forgotten).
	const sid = "agy-11111111-2222-3333-4444-555555555555"
	n.state.put(sid, sessionState{Cwd: cwd, Launcher: agyAgent})
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: sid, Cwd: cwd, Status: proto.StatusIdle},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agy session listed", func() bool {
		_, _, ok := n.runtimes.rowOf(sid)
		return ok
	})

	fromSession, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:     req.Owner,
		Machine:   n.machineID,
		SessionID: sid,
		Cwd:       cwd,
	})
	if err != nil || len(fromSession.Skills) != 1 || fromSession.Skills[0].ID != catalog.Skills[0].ID {
		t.Fatalf("existing agy session catalog mismatch: %+v vs %+v (%v)", fromSession, catalog, err)
	}

	// Even after state is cleared, runtime ownership resolves to agy consistently.
	n.state.put(sid, sessionState{Cwd: cwd})
	afterReset, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:     req.Owner,
		Machine:   n.machineID,
		SessionID: sid,
		Cwd:       cwd,
	})
	if err != nil || len(afterReset.Skills) != 1 || afterReset.Skills[0].ID != catalog.Skills[0].ID {
		t.Fatalf("stateless agy session catalog mismatch: %+v (%v)", afterReset, err)
	}
}

func TestAgySkillCatalogToRuntimeDeliveryAndFailure(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, agyAssistantHello())
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", t.TempDir())

	cwd := filepath.Join(n.home, ".persona", "assistant")
	const sid = "agy-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	host.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": proto.Session{SessionID: sid, Cwd: cwd, Status: proto.StatusIdle}}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	file := filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Review\nallowed-tools: Bash(*)\n---\n!`must-not-execute`\n")

	// 1. Discover catalog via skills.list RPC (catalog mode) as /assistant/skills does.
	raw, err := nodeCall(t, n, "skills.list", map[string]any{
		"catalog":     true,
		"skill_owner": "helper-a",
		"machine_id":  n.machineID,
		"cwd":         cwd,
		"launcher":    agyAgent,
	})
	if err != nil {
		t.Fatalf("skills.list catalog: %v", err)
	}
	var catalog assistantskills.Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil || catalog.Version != 1 || len(catalog.Skills) != 1 {
		t.Fatalf("decoded catalog: %s %v", raw, err)
	}

	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">review</persona-context>` + "\nCheck this"

	// 2. First message (new agy conversation via assistant.send) binds and delivers skill instructions in context.
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"launcher":    agyAgent,
		"cwd":         cwd,
		"skill_owner": "helper-a",
		"text":        text,
	}); err != nil {
		t.Fatalf("assistant.send with agy skill: %v", err)
	}
	sends := host.got("session.send")
	if len(sends) != 1 {
		t.Fatalf("expected 1 runtime send, got %d", len(sends))
	}
	var first struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
		Context   string `json:"context"`
	}
	if err := json.Unmarshal(sends[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.SessionID != sid || first.Text != text {
		t.Fatalf("verbatim text or session mismatch: %+v", first)
	}
	if !strings.Contains(first.Context, `<persona-context kind="skill-instructions">`) ||
		!strings.Contains(first.Context, resolved(t, file)) ||
		!strings.Contains(first.Context, catalog.Skills[0].ID) ||
		strings.Contains(first.Context, "must-not-execute") ||
		strings.Contains(first.Context, "allowed-tools: Bash") {
		t.Fatalf("unexpected skill delivery context: %s", first.Context)
	}

	// 3. Follow-up on existing agy session via session.send (omitting launcher & cwd, public=true so snapshot is skipped).
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id":  sid,
		"skill_owner": "helper-a",
		"public":      true,
		"text":        text,
	}); err != nil {
		t.Fatalf("follow-up session.send with agy skill: %v", err)
	}
	sends = host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("expected 2 runtime sends, got %d", len(sends))
	}
	var second struct {
		Text    string `json:"text"`
		Context string `json:"context"`
	}
	_ = json.Unmarshal(sends[1], &second)
	if second.Text != text || !strings.Contains(second.Context, resolved(t, file)) || !strings.Contains(second.Context, catalog.Skills[0].ID) {
		t.Fatalf("public follow-up lost skill instructions: %+v", second)
	}

	// 4. Wrong skill_owner fails closed without delivering to agy.
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id":  sid,
		"skill_owner": "helper-b",
		"text":        text,
	}); rpcCode(err) != "skill_unavailable" {
		t.Fatalf("wrong owner should fail closed: %v", err)
	}
	if len(host.got("session.send")) != 2 {
		t.Fatal("wrong-owner skill reference reached agy runtime")
	}

	// 5. Deleted skill fails closed without delivering to agy.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id":  sid,
		"skill_owner": "helper-a",
		"text":        text,
	}); rpcCode(err) != "skill_unavailable" {
		t.Fatalf("deleted skill should fail closed: %v", err)
	}
	if len(host.got("session.send")) != 2 {
		t.Fatal("deleted skill reference reached agy runtime")
	}

	// 6. Ordinary chat without explicit skill references still works without skill_owner or catalog.
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id": sid,
		"text":       "Literal @review me@example.com",
	}); err != nil {
		t.Fatalf("ordinary agy chat failed: %v", err)
	}
	if len(host.got("session.send")) != 3 {
		t.Fatal("ordinary agy chat was not delivered")
	}
}

func TestAgySkillCatalogRejectsUnsupportedRuntimesAndGuests(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", t.TempDir())

	cwd := filepath.Join(n.home, ".persona", "assistant")
	writeFile(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\n---\nInstructions\n")

	// Connect an assistant-capable DSH runtime and a non-assistant runtime.
	dsh := dialRuntime(t, n, assistantRuntimeHello())
	dshID := startedAssistant(t, n, dsh)
	_ = dialRuntime(t, n, map[string]any{
		"runtime": "plain-rt", "launcher": "plain-rt",
		"caps": map[string]any{"start": true, "send": true},
	})
	agy := dialRuntime(t, n, agyAssistantHello())
	// Non-assistant ordinary agy session.
	const ordinaryAgy = "agy-ordinary-session"
	if err := agy.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: ordinaryAgy, Cwd: t.TempDir(), Status: proto.StatusIdle},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ordinary agy session listed", func() bool {
		_, _, ok := n.runtimes.rowOf(ordinaryAgy)
		return ok
	})

	// Guest session recorded in state.
	const guestID = "550e8400-e29b-41d4-a716-446655440000"
	n.state.put(guestID, sessionState{Cwd: cwd, Launcher: "claude", Guest: []string{"ask_user"}})

	// Existing Claude assistant session.
	const claudeID = "550e8400-e29b-41d4-a716-446655440001"
	n.state.put(claudeID, sessionState{Cwd: cwd, Launcher: "claude"})

	cases := []struct {
		name string
		req  skillCatalogRequest
	}{
		{"dsh new session", skillCatalogRequest{Owner: "helper", Machine: n.machineID, Cwd: cwd, Launcher: "dsh"}},
		{"dsh existing session", skillCatalogRequest{Owner: "helper", Machine: n.machineID, SessionID: dshID, Cwd: cwd}},
		{"dsh existing session spoofing agy launcher", skillCatalogRequest{Owner: "helper", Machine: n.machineID, SessionID: dshID, Cwd: cwd, Launcher: agyAgent}},
		{"non-assistant runtime launcher", skillCatalogRequest{Owner: "helper", Machine: n.machineID, Cwd: cwd, Launcher: "plain-rt"}},
		{"ordinary non-assistant agy session", skillCatalogRequest{Owner: "helper", Machine: n.machineID, SessionID: ordinaryAgy, Cwd: cwd, Launcher: agyAgent}},
		{"existing claude session with agy launcher", skillCatalogRequest{Owner: "helper", Machine: n.machineID, SessionID: claudeID, Cwd: cwd, Launcher: agyAgent}},
		{"guest session", skillCatalogRequest{Owner: "helper", Machine: n.machineID, SessionID: guestID, Cwd: cwd, Launcher: agyAgent}},
	}
	for _, tc := range cases {
		if _, err := n.skillCatalog(context.Background(), tc.req); err == nil {
			t.Errorf("%s: expected error, got catalog", tc.name)
		}
	}

	// Unsupported runtime must reject explicit skill references on send without delivering anything.
	beforeDSH := len(dsh.got("session.send"))
	refText := `<persona-context kind="skill" ref="deadbeef">review</persona-context>` + "\nReview"
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id":  dshID,
		"skill_owner": "helper",
		"text":        refText,
	}); rpcCode(err) != "skill_unavailable" {
		t.Fatalf("dsh send with skill ref: got %v, want skill_unavailable", err)
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"launcher":    "dsh",
		"cwd":         cwd,
		"skill_owner": "helper",
		"text":        refText,
	}); rpcCode(err) != "skill_unavailable" {
		t.Fatalf("dsh new assistant.send with skill ref: got %v, want skill_unavailable", err)
	}
	if len(dsh.got("session.send")) != beforeDSH {
		t.Fatal("skill reference reached unsupported dsh runtime")
	}

	// Disconnecting agy must cause both plain and managed agy launchers to fail closed (never fall back to Claude).
	n.setManaged([]proto.ManagedLauncher{{Name: "managed-agy", Agent: agyAgent}})
	_ = agy.conn.Close()
	waitFor(t, "agy runtime disconnected", func() bool { return n.assistantRuntime(agyAgent) == nil })
	for _, launcher := range []string{"managed-agy"} {
		if _, err := n.skillCatalog(context.Background(), skillCatalogRequest{
			Owner:    "helper",
			Machine:  n.machineID,
			Cwd:      cwd,
			Launcher: launcher,
		}); err == nil {
			t.Fatalf("offline %s fell back instead of failing closed", launcher)
		}
	}
}

func TestAgyConnectedPreservesBuiltinUUIDSessionWithOmittedLauncher(t *testing.T) {
	fakeClaudeManagedSettings(t)
	n, _ := runtimeNode(t)
	_ = dialRuntime(t, n, agyAssistantHello())
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", t.TempDir())

	cwd, profile := t.TempDir(), t.TempDir()
	claudeSkill := filepath.Join(profile, "skills", "claude-review", "SKILL.md")
	writeFile(t, claudeSkill, "---\nname: claude-review\ndescription: Built-in Claude skill\n---\nInstructions\n")
	// Put a distinct agy skill in cwd/.agents/skills to prove the built-in Claude path is used.
	writeFile(t, filepath.Join(cwd, ".agents", "skills", "agy-only", "SKILL.md"),
		"---\nname: agy-only\ndescription: Agy skill\n---\nInstructions\n")

	const claudeID = "550e8400-e29b-41d4-a716-446655440010"
	n.state.put(claudeID, sessionState{Cwd: cwd, Launcher: "claude", ConfigDir: profile})

	// Omitting launcher on an existing UUID Claude session while an agy runtime is connected
	// must stay on the built-in Claude catalog path and resolve consistently on send.
	catalog, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:     "helper-a",
		Machine:   n.machineID,
		SessionID: claudeID,
		Cwd:       cwd,
		ConfigDir: profile,
	})
	if err != nil {
		t.Fatalf("UUID Claude session with omitted launcher failed while agy connected: %v", err)
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Name != "claude-review" || catalog.Skills[0].Path != resolved(t, claudeSkill) {
		t.Fatalf("expected built-in Claude catalog, got %+v", catalog.Skills)
	}
	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">claude-review</persona-context>` + "\nCheck this"
	ctx := withSkillOwner(context.Background(), "helper-a")
	for _, sendLauncher := range []string{"", "claude"} {
		selected, err := n.selectedSkills(ctx, claudeID, cwd, sendLauncher, text, modelExecution{ConfigDir: profile})
		if err != nil || len(selected) != 1 || selected[0].Path != resolved(t, claudeSkill) {
			t.Fatalf("selectedSkills(launcher=%q) failed: %+v %v", sendLauncher, selected, err)
		}
	}

	// A UUID Claude session whose saved state uses a managed Claude launcher must also keep the built-in path.
	n.setManaged([]proto.ManagedLauncher{{Name: "gw-claude", Agent: "claude"}})
	const managedClaudeID = "550e8400-e29b-41d4-a716-446655440011"
	n.state.put(managedClaudeID, sessionState{Cwd: cwd, Launcher: "gw-claude", ConfigDir: profile})
	managedCatalog, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:     "helper-a",
		Machine:   n.machineID,
		SessionID: managedClaudeID,
		Cwd:       cwd,
		ConfigDir: profile,
	})
	if err != nil || len(managedCatalog.Skills) != 1 || managedCatalog.Skills[0].Name != "claude-review" {
		t.Fatalf("managed Claude UUID session with omitted launcher failed: %+v %v", managedCatalog, err)
	}
	managedText := `<persona-context kind="skill" ref="` + managedCatalog.Skills[0].ID + `">claude-review</persona-context>` + "\nCheck this"
	for _, sendLauncher := range []string{"", "gw-claude"} {
		if selected, err := n.selectedSkills(ctx, managedClaudeID, cwd, sendLauncher, managedText, modelExecution{ConfigDir: profile}); err != nil || len(selected) != 1 {
			t.Fatalf("managed Claude selectedSkills(launcher=%q) mismatch: %+v %v", sendLauncher, selected, err)
		}
	}

	// Explicitly requesting launcher="agy" on that UUID Claude session remains rejected.
	if _, err := n.skillCatalog(context.Background(), skillCatalogRequest{
		Owner:     "helper-a",
		Machine:   n.machineID,
		SessionID: claudeID,
		Cwd:       cwd,
		Launcher:  agyAgent,
	}); err == nil {
		t.Fatal("explicit agy launcher on UUID Claude session must be rejected")
	}
}
