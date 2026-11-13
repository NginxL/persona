package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestOrdinaryClaudeSkillsCatalogAndSend(t *testing.T) {
	probedLaunchers(t, map[string]string{"claude": "claude"})
	withSubmitGrace(t, 50*time.Millisecond)
	n, o, _ := orcaSession(t)
	cwd, profile := t.TempDir(), t.TempDir()
	a, _ := n.agent(sidA)
	a.Cwd = cwd
	n.agents[sidA] = a
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, ConfigDir: profile})
	transcript := filepath.Join(profile, "projects", "-example", sidA+".jsonl")
	writeFile(t, transcript, `{"type":"user","message":{"content":"Example"}}`+"\n")
	n.paths[sidA] = transcript
	file := filepath.Join(profile, "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Inspect boundaries\n---\n!`never-run`\n")
	owner := "agents:helper"
	catalog, err := n.sessionSkillCatalog(context.Background(), owner, sidA)
	if err != nil || len(catalog.Skills) != 1 {
		t.Fatalf("ordinary catalog: %+v %v", catalog, err)
	}
	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">review</persona-context>` + "\nCheck this"
	n.agents[sidB] = host.Agent{Agent: "claude", Cwd: cwd, PaneID: a.PaneID}
	n.state.put(sidB, sessionState{Launcher: "claude", Cwd: cwd, ConfigDir: profile})
	for _, change := range []string{"manager", "machine", "session", "cwd", "profile", "launcher", "switch", "offline"} {
		otherOwner, otherID := owner, sidA
		machine := n.machineID
		switch change {
		case "manager":
			otherOwner = "agents:other"
		case "session":
			otherID = sidB
		case "machine":
			n.machineID = "other-computer"
		case "launcher":
			n.state.put(sidA, sessionState{Launcher: "unknown-launcher", Cwd: cwd, ConfigDir: profile})
		case "switch":
			n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, ConfigDir: profile, PendingSwitch: &proto.PendingSwitch{Launcher: "other"}})
		case "offline":
			delete(n.agents, sidA)
		case "cwd":
			changed := a
			changed.Cwd = t.TempDir()
			n.agents[sidA] = changed
		case "profile":
			otherProfile := t.TempDir()
			otherTranscript := filepath.Join(otherProfile, "projects", "-example", sidA+".jsonl")
			writeFile(t, otherTranscript, `{"type":"user","message":{"content":"Another profile"}}`+"\n")
			n.paths[sidA] = otherTranscript
			n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, ConfigDir: otherProfile})
		}
		other, err := n.sessionSkillCatalog(context.Background(), otherOwner, otherID)
		if err == nil {
			if _, err := assistantskills.Resolve(text, other.Skills); err == nil {
				t.Fatalf("selection crossed %s", change)
			}
		}
		n.agents[sidA] = a
		n.machineID = machine
		n.paths[sidA] = transcript
		n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, ConfigDir: profile})
	}
	o.SetScreen("term_1", "❯ \n")
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "skill_owner": owner, "text": text}); err != nil {
		t.Fatal(err)
	}
	writes := o.Writes()
	if len(writes) == 0 || !strings.Contains(writes[0].Text, "Instruction file:") || !strings.Contains(writes[0].Text, file) || !strings.Contains(writes[0].Text, catalog.Skills[0].ID) || strings.Contains(writes[0].Text, "never-run") {
		t.Fatalf("missing exact-file intent: %+v", writes)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	before := len(o.Writes())
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "skill_owner": owner, "text": text}); err == nil {
		t.Fatal("deleted skill was sent")
	}
	if len(o.Writes()) != before {
		t.Fatal("a refused skill typed into the terminal")
	}
}

func TestOrdinarySkillsNeverReachUnsupportedRuntime(t *testing.T) {
	n, _ := runtimeNode(t)
	runtime := dialRuntime(t, n, dshHello())
	_ = runtime.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-skills", "status": "idle"}}})
	waitFor(t, "runtime listed", func() bool { _, ok := listed(t, n)["session-skills"]; return ok })
	text := `<persona-context kind="skill" ref="old-selection">review</persona-context>` + "\nReview"
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "session-skills", "text": text, "skill_owner": "agents:helper"}); err == nil {
		t.Fatal("unsupported runtime accepted a selection")
	}
	if len(runtime.got("session.send")) != 0 {
		t.Fatal("a skill reference reached the unsupported runtime")
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "session-skills", "text": "Literal @review"}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.got("session.send")) != 1 {
		t.Fatal("plain chat stopped working")
	}
}

func TestOrdinarySkillsRefuseUnboundCodexFirstMessage(t *testing.T) {
	n, _ := testNode(t)
	id := codexWaitID()
	n.codexWaiting = map[string]*codexWait{"synthetic-pane": {id: id, pane: "synthetic-pane"}}
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": `<persona-context kind="skill" ref="old-selection">review</persona-context>`, "skill_owner": "agents:helper"})
	code, data := refusalOf(t, err)
	if code != "skill_unavailable" || data["delivered"] != "none" {
		t.Fatalf("unbound thread selection: %s %+v", code, data)
	}
}

func TestOrdinaryCodexSkillsUseOwningConnection(t *testing.T) {
	n := remoteTestNode(t)
	// The catalog reports resolved paths; on macOS t.TempDir() is under a symlink.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	file := filepath.Join(home, ".agents", "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\n---\nInstructions\n")
	f.mu.Lock()
	f.response = func(method string, params map[string]any) (any, bool) {
		if method != "skills/list" {
			return nil, false
		}
		return map[string]any{"data": []any{map[string]any{"cwd": home, "skills": []any{
			map[string]any{"name": "review", "path": file, "scope": "repo", "enabled": true},
		}, "errors": []any{}}}}, true
	}
	f.mu.Unlock()
	owner := "agents:helper"
	catalog, err := n.sessionSkillCatalog(context.Background(), owner, id)
	if err != nil || !catalog.Complete || len(catalog.Skills) != 1 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	if f.count("thread/resume") != 0 || f.count("turn/start") != 0 {
		t.Fatal("catalog took over the thread")
	}
	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">review</persona-context>` + "\nReview"
	for _, method := range []string{"turn/start", "turn/steer"} {
		if err := n.send(withSkillOwner(context.Background(), owner), id, text, nil, sendOpts{}); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		for _, m := range f.got {
			if m.Method != method {
				continue
			}
			var p struct{ Input []map[string]any }
			_ = json.Unmarshal(m.Params, &p)
			if len(p.Input) < 2 || p.Input[0]["type"] != "skill" || p.Input[0]["path"] != file || p.Input[0]["name"] != "review" {
				t.Errorf("%s lost typed selection: %s", method, m.Params)
			}
		}
		f.mu.Unlock()
		if f.count(method) != 1 {
			t.Fatalf("missing %s", method)
		}
	}
	before := f.count("turn/steer")
	if err := n.send(withSkillOwner(context.Background(), "agents:other"), id, text, nil, sendOpts{}); err == nil {
		t.Fatal("foreign manager ref sent")
	}
	c.close()
	_, _ = connectRemoteFixture(t, n, home, "replacement", remoteRaw)
	if err := n.send(withSkillOwner(context.Background(), owner), id, text, nil, sendOpts{}); err == nil {
		t.Fatal("old connection ref sent")
	}
	if f.count("turn/steer") != before {
		t.Fatal("invalid selection was delivered")
	}
	if err := n.send(context.Background(), id, "Literal @review", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
}
