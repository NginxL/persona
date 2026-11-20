package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercise the RPC decoder, real process startup, state persistence and idle resume,
// rather than testing only a flag builder or fingerprint in isolation.
func TestAssistantSupervisionStartupResumeIsolation(t *testing.T) {
	f := cliTest(t)
	ctx, cwd := context.Background(), t.TempDir()
	policy := prompts.RenderSupervisionInstructions()
	sendMethod := "assistant.send"
	send := func(id, text, instructions string) string {
		t.Helper()
		raw, err := nodeCall(t, f.n, sendMethod, map[string]any{
			"session_id": id, "cwd": cwd, "launcher": "claude",
			"text": text, "supervision_instructions": instructions,
		})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			ID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("send result: %s (%v)", raw, err)
		}
		if sendMethod == "session.send" {
			result.ID = id
		}
		if result.ID == "" {
			t.Fatalf("send result has no session: %s", raw)
		}
		f.idle(t, result.ID)
		return result.ID
	}
	checkStartup := func(id, want string, resume bool) {
		t.Helper()
		args := f.file("cli-argv")
		if want != "" && !strings.Contains(args, "--append-system-prompt "+want) {
			t.Fatal("CLI did not receive exact policy bytes")
		}
		if want == "" && strings.Contains(args, "--append-system-prompt") {
			t.Fatal("ordinary CLI received supervisor policy")
		}
		if resume && !strings.Contains(args, "--resume "+id) {
			t.Fatal("role switch did not resume the CLI conversation")
		}
	}
	clearStartup := func() {
		t.Helper()
		_ = os.Remove(filepath.Join(f.dir, "cli-argv"))
	}
	id := send("", "First task facts", policy)
	first := f.proc(id)
	checkStartup(id, policy, false)
	if got, _ := f.n.state.get(id); got.SupervisionInstructions != policy {
		t.Fatal("state did not retain supervision policy for node-local resume")
	}
	send(id, "Changed task evidence, same supervisor", policy)
	if f.proc(id) != first {
		t.Fatal("new event data needlessly restarted the process")
	}
	// A role change must neither kill active work nor send new input on the old
	// system policy. An idle retry below applies it before delivery.
	first.mu.Lock()
	first.busy = true
	first.mu.Unlock()
	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{"session_id": id, "launcher": "claude",
		"text": "Do not deliver on the old role", "supervision_instructions": ""}); refusalCode(err) != "busy" {
		t.Fatalf("active role change was not held: %v", err)
	}
	if f.proc(id) != first || !first.alive() {
		t.Fatal("role change killed active work")
	}
	first.mu.Lock()
	first.busy = false
	first.mu.Unlock()
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.n.mu.Lock()
	f.n.paths[id] = path
	f.n.mu.Unlock()
	clearStartup()
	sendMethod = "session.send" // internal manager messages must refresh roles too
	send(id, "Role removed", "")
	sendMethod = "assistant.send"
	ordinary := f.proc(id)
	if ordinary == first {
		t.Fatal("removing the role without a model fingerprint kept privileged process policy")
	}
	checkStartup(id, "", true)
	clearStartup()
	send(id, "Role granted again", policy)
	if f.proc(id) == ordinary {
		t.Fatal("granting the role did not restart")
	}
	checkStartup(id, policy, true)
	// A separate ordinary conversation shares exactly the same cwd.
	clearStartup()
	side := send("", "Unrelated ordinary chat", "")
	if side == id || f.proc(side).cwd != cwd {
		t.Fatal("fixture did not create a separate same-directory conversation")
	}
	checkStartup(side, "", false)
	if f.proc(id).model.SupervisionInstructions != policy {
		t.Fatal("ordinary chat changed its neighbor's policy")
	}
	// A local resend with no manager metadata keeps the persisted role.
	if _, err := f.n.assistantSend(ctx, id, cwd, "", "Local resume", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if f.proc(id).model.SupervisionInstructions != policy {
		t.Fatal("node-local send lost the persisted supervisor role")
	}
}

func TestCodexSupervisionDeveloperInstructionsStartResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, supervisor := range []bool{false, true} {
			t.Run("resume="+strconv.FormatBool(resume)+"/supervisor="+strconv.FormatBool(supervisor), func(t *testing.T) {
				n := codexTestNode(t)
				cwd := t.TempDir()
				p, c, f := newFakeCodex(t, n, cwd)
				method := "thread/start"
				if resume {
					method = "thread/resume"
				}
				f.results[method] = map[string]any{"thread": map[string]any{"id": testThread}}
				policy := ""
				if supervisor {
					policy = prompts.RenderSupervisionInstructions()
				}
				p.model.SupervisionInstructions = policy
				if err := c.start(context.Background(), testThread, cwd, resume); err != nil {
					t.Fatal(err)
				}
				var params map[string]any
				if err := json.Unmarshal(f.sent(method)[0].Params, &params); err != nil {
					t.Fatal(err)
				}
				want := prompts.RenderCodexInstructions(codexMemoryPrompt(c.memDir), policy)
				if params["developerInstructions"] != want {
					t.Fatal("Codex thread received different instruction bytes")
				}
				if !supervisor && strings.Contains(want, "## Task supervision") {
					t.Fatal("ordinary Codex thread received supervisor policy")
				}
				if supervisor && (!strings.HasSuffix(want, policy) || strings.Count(want, policy) != 1) {
					t.Fatal("Codex policy was changed or duplicated")
				}
			})
		}
	}
}

func TestSupervisionWarmSpareIsolation(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			if agent == "codex" {
				fakeCodexHome(t, testThread)
			} else {
				fakeClaudeHome(t, "echo '{\"type\":\"system\",\"subtype\":\"init\"}'\nwhile read line; do :; done\n")
			}
			n := codexTestNode(t)
			withFakeHub(t, n)
			cwd, policy := t.TempDir(), prompts.RenderSupervisionInstructions()
			for _, instructions := range []string{policy, ""} {
				params := map[string]any{"launcher": agent, "cwd": cwd, "supervision_instructions": instructions}
				if _, err := nodeCall(t, n, "assistant.warm", params); err != nil {
					t.Fatal(err)
				}
				n.mu.Lock()
				spare := n.spare
				n.mu.Unlock()
				if spare == nil || spare.model.SupervisionInstructions != instructions {
					t.Fatal("warm RPC lost role policy")
				}
				t.Cleanup(spare.stop)
				if _, err := nodeCall(t, n, "assistant.warm", params); err != nil {
					t.Fatal(err)
				}
				n.mu.Lock()
				same := n.spare == spare
				n.mu.Unlock()
				if !same {
					t.Fatal("identical static policy restarted the warm process")
				}
				other := modelExecution{SupervisionInstructions: policy}
				if instructions != "" {
					other.SupervisionInstructions = ""
				}
				if n.adoptSpare(cwd, agent, other) != nil {
					t.Fatal("warm spare leaked between supervisor and ordinary roles")
				}
			}
		})
	}
}

func TestSupervisionModelLookupAndFingerprint(t *testing.T) {
	n := codexTestNode(t)
	f := withFakeHub(t, n)
	policy := prompts.RenderSupervisionInstructions()
	f.model = map[string]any{"launcher": "claude", "supervision_instructions": policy}
	_, ex := n.assistantModel(context.Background(), testThread)
	if ex.SupervisionInstructions != policy || !ex.SupervisionResolved {
		t.Fatal("node-local model lookup lost current authorized policy")
	}
	a := n.executionFingerprint("claude", ex)
	ex.SupervisionResolved = false
	if n.executionFingerprint("claude", ex) != a {
		t.Fatal("metadata presence changed execution despite identical policy bytes")
	}
	ex.SupervisionInstructions = ""
	if n.executionFingerprint("claude", ex) == a {
		t.Fatal("role removal was not part of the execution fingerprint")
	}
}

func TestAssistantSupervisionStoppedCompactionRefreshesRole(t *testing.T) {
	f := cliTest(t)
	ctx, cwd := context.Background(), t.TempDir()
	ex := modelExecution{SupervisionInstructions: prompts.RenderSupervisionInstructions()}
	id, err := f.n.assistantSend(ctx, "", cwd, "claude", "Task facts", nil, "", "", ex)
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.n.mu.Lock()
	f.n.paths[id] = path
	f.n.mu.Unlock()
	f.n.stopAssistant(id)
	f.fh.mu.Lock()
	f.fh.model = map[string]any{"launcher": "claude", "supervision_instructions": ""}
	f.fh.mu.Unlock()
	if err := f.n.assistantCompact(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if f.proc(id).model.SupervisionInstructions != "" {
		t.Fatal("stopped compaction resumed with a revoked supervisor role")
	}
	if st, _ := f.n.state.get(id); st.SupervisionInstructions != "" {
		t.Fatal("compaction did not persist the refreshed role")
	}
}

// A conversation on a runtime has no separate policy channel. A person's message that arrives with a policy (the hub
// asks for one when it cannot tell that the conversation runs on a runtime) goes through without it; only the manager's
// own supervision message, which the policy is for, is refused rather than delivered unsupervised.
func TestSupervisionRuntimeDropsPolicyForOwnerMessagesButRefusesSupervision(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	dir := filepath.Join(n.home, ".persona", "assistant")
	policy := prompts.RenderSupervisionInstructions()
	supervision := map[string]any{"launcher": "dsh", "cwd": dir, "text": "Review task evidence", "supervision_instructions": policy,
		"message_source": map[string]any{"kind": "supervision"}}
	for _, method := range []string{"assistant.send", "assistant.warm"} {
		p := maps.Clone(supervision)
		if method == "assistant.warm" {
			delete(p, "message_source") // a warm is nobody's message
		}
		if _, err := nodeCall(t, n, method, p); method == "assistant.send" && refusalCode(err) != "assistant_supervision_runtime" {
			t.Fatalf("%s silently accepted a supervision message without its policy: %v", method, err)
		} else if method == "assistant.warm" && err != nil {
			t.Fatalf("warming a runtime with a policy: %v", err)
		}
	}
	if len(host.got("session.start")) != 0 || len(host.got("session.send")) != 0 {
		t.Fatal("unsupported runtime started a supervisor without system instructions")
	}
	// The owner's message in a conversation the hub took for a supervisor is delivered, without the policy.
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "session-a1", "cwd": dir, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	owner := map[string]any{"launcher": "dsh", "cwd": dir, "text": "Where are we?", "supervision_instructions": policy}
	if _, err := nodeCall(t, n, "assistant.send", owner); err != nil {
		t.Fatalf("the owner's message to a runtime conversation was refused: %v", err)
	}
	sends := host.got("session.send")
	if len(sends) != 1 || !strings.Contains(string(sends[0]), "Where are we?") || strings.Contains(string(sends[0]), "supervis") {
		t.Fatalf("the owner's message did not reach the runtime without the policy: %s", sends)
	}
	// A later message to the same conversation, resolved without a policy, is delivered as well.
	owner = map[string]any{"session_id": "session-a1", "launcher": "dsh", "cwd": dir, "text": "And now?", "resume_only": true, "supervision_instructions": policy}
	if _, err := nodeCall(t, n, "assistant.send", owner); err != nil {
		t.Fatalf("the owner's next message was refused: %v", err)
	}
	if got := len(host.got("session.send")); got != 2 {
		t.Fatalf("%d messages reached the runtime, want 2", got)
	}
	// And a supervision message to that conversation is still refused.
	supervision["session_id"] = "session-a1"
	if _, err := nodeCall(t, n, "assistant.send", supervision); refusalCode(err) != "assistant_supervision_runtime" {
		t.Fatalf("a supervision message to a runtime conversation = %v", err)
	}
	if got := len(host.got("session.send")); got != 2 {
		t.Fatalf("a refused supervision message reached the runtime: %d sends", got)
	}
	if row := listed(t, n)["session-a1"]; row.Capabilities != nil && row.Capabilities.SupervisionPolicy {
		t.Fatalf("non-capable runtime advertised supervision_policy capability: %+v", row.Capabilities)
	}
}

// A runtime that explicitly advertises caps.supervision_policy receives supervision_instructions as a dedicated
// protocol parameter on session.start and session.send (never concatenated into user text), preserves busy role-change
// isolation, clears the policy on explicit empty metadata, and fails closed when the runtime returns
// assistant_supervision_runtime.
func TestSupervisionCapableRuntimePolicyLifecycleAndFailClosed(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	agyHello := map[string]any{
		"runtime":  "agy",
		"name":     "Antigravity",
		"launcher": "agy",
		"caps": map[string]any{
			"start":              true,
			"send":               true,
			"interrupt":          true,
			"assistant":          true,
			"supervision_policy": true,
		},
	}
	host := dialRuntime(t, n, agyHello)
	dir := filepath.Join(n.home, ".persona", "assistant")
	policy := prompts.RenderSupervisionInstructions()
	const sid = "agy-11111111-2222-4333-8444-555555555555"

	var failSend error
	host.answer = func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "session.start":
			return map[string]any{"session": map[string]any{"session_id": sid, "cwd": dir, "status": "idle"}}, nil
		case "session.send":
			if failSend != nil {
				return nil, failSend
			}
			return map[string]any{"ok": true}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	// Start a new supervisor conversation with supervision message_source and policy.
	raw, err := nodeCall(t, n, "assistant.send", map[string]any{
		"launcher":                 "agy",
		"cwd":                      dir,
		"text":                     "Review task evidence",
		"supervision_instructions": policy,
		"message_source":           map[string]any{"kind": "supervision"},
	})
	if err != nil {
		t.Fatalf("capable runtime start/send failed: %v", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.SessionID != sid {
		t.Fatalf("start response = %s (%v)", raw, err)
	}
	if row := listed(t, n)[sid]; row.Capabilities == nil || !row.Capabilities.SupervisionPolicy {
		t.Fatalf("capable runtime row missing supervision_policy capability: %+v", row.Capabilities)
	}
	starts := host.got("session.start")
	if len(starts) != 1 {
		t.Fatalf("session.start count = %d, want 1", len(starts))
	}
	var startP struct {
		Assistant               bool   `json:"assistant"`
		SupervisionInstructions string `json:"supervision_instructions"`
	}
	if err := json.Unmarshal(starts[0], &startP); err != nil || !startP.Assistant || startP.SupervisionInstructions != policy {
		t.Fatalf("session.start did not carry exact supervision_instructions: %s", starts[0])
	}
	sends := host.got("session.send")
	if len(sends) != 1 {
		t.Fatalf("session.send count = %d, want 1", len(sends))
	}
	var sendP struct {
		Text                    string `json:"text"`
		Context                 string `json:"context"`
		SupervisionInstructions string `json:"supervision_instructions"`
	}
	if err := json.Unmarshal(sends[0], &sendP); err != nil {
		t.Fatal(err)
	}
	if sendP.SupervisionInstructions != policy || strings.Contains(sendP.Text, "## Task supervision") || strings.Contains(sendP.Context, "## Task supervision") {
		t.Fatalf("supervision policy was missing or leaked into user text/context: %+v", sendP)
	}
	if st, ok := n.state.get(sid); !ok || st.SupervisionInstructions != policy {
		t.Fatalf("persisted supervision policy = %q (ok=%v)", st.SupervisionInstructions, ok)
	}

	// While busy, a role change (revoking policy) is refused as busy without calling session.send or mutating state.
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": sid, "cwd": dir, "status": "working"},
	}})
	waitFor(t, "session working", func() bool { return listed(t, n)[sid].Status == "working" })
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id":               sid,
		"launcher":                 "agy",
		"cwd":                      dir,
		"text":                     "Role removed while working",
		"supervision_instructions": "",
	}); refusalCode(err) != "busy" {
		t.Fatalf("busy role change = %v, want busy", err)
	}
	if got := len(host.got("session.send")); got != 1 {
		t.Fatalf("busy role change reached runtime: %d sends", got)
	}
	if st, _ := n.state.get(sid); st.SupervisionInstructions != policy {
		t.Fatal("busy role change altered persisted supervision policy")
	}

	// Once idle, explicit empty supervision_instructions clears the role on the runtime and in state.
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": sid, "cwd": dir, "status": "idle"},
	}})
	waitFor(t, "session idle", func() bool { return listed(t, n)[sid].Status == "idle" })
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id":               sid,
		"launcher":                 "agy",
		"cwd":                      dir,
		"text":                     "Ordinary chat now",
		"supervision_instructions": "",
	}); err != nil {
		t.Fatalf("revoking policy while idle failed: %v", err)
	}
	sends = host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("session.send count = %d, want 2", len(sends))
	}
	var clearP map[string]any
	if err := json.Unmarshal(sends[1], &clearP); err != nil {
		t.Fatal(err)
	}
	if val, present := clearP["supervision_instructions"]; !present || val != "" {
		t.Fatalf("role revocation did not send explicit empty supervision_instructions: %v", clearP)
	}
	if st, _ := n.state.get(sid); st.SupervisionInstructions != "" {
		t.Fatalf("persisted supervision policy was not cleared: %q", st.SupervisionInstructions)
	}

	// That turn ends: the runtime reports the conversation idle. A send marks an Antigravity conversation working until
	// its runtime says otherwise (#11591), and a policy change while working is refused as busy (above).
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": sid, "cwd": dir, "status": "idle"},
	}})
	waitFor(t, "session idle after the ordinary turn", func() bool { return listed(t, n)[sid].Status == "idle" })

	// If the runtime fails closed applying policy, the node propagates assistant_supervision_runtime and keeps prior state.
	failSend = &rpc.Error{Code: "assistant_supervision_runtime", Message: "policy write failed"}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id":               sid,
		"launcher":                 "agy",
		"cwd":                      dir,
		"text":                     "Review again",
		"supervision_instructions": policy,
		"message_source":           map[string]any{"kind": "supervision"},
	}); refusalCode(err) != "assistant_supervision_runtime" {
		t.Fatalf("runtime fail-closed error = %v, want assistant_supervision_runtime", err)
	}
	if st, _ := n.state.get(sid); st.SupervisionInstructions != "" {
		t.Fatalf("failed policy application overwrote persisted state: %q", st.SupervisionInstructions)
	}
}

// TestFakeSupervisionCodexServer is the subprocess used by the native startup test.
func TestFakeSupervisionCodexServer(t *testing.T) {
	record := os.Getenv("PERSONA_TEST_SUPERVISION_CODEX")
	if record == "" {
		return
	}
	log, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	thread := os.Getenv("PERSONA_FAKE_CODEX_THREAD")
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	write := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Println(string(raw))
	}
	for scan.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ThreadID string `json:"threadId"`
			} `json:"params"`
		}
		if json.Unmarshal(scan.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		_, _ = log.Write(append(append([]byte(nil), scan.Bytes()...), '\n'))
		var result any = map[string]any{}
		switch m.Method {
		case "thread/resume":
			thread = m.Params.ThreadID
			fallthrough
		case "thread/start":
			result = map[string]any{"thread": map[string]any{"id": thread}}
		case "thread/backgroundTerminals/list":
			result = map[string]any{"data": []any{}}
		case "turn/start":
			result = map[string]any{"turn": map[string]any{"id": "test-turn", "status": "inProgress"}}
		}
		write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		if m.Method == "turn/start" || m.Method == "turn/steer" {
			write(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
				"threadId": thread, "turn": map[string]any{"id": "test-turn", "status": "completed"}}})
		}
	}
	_ = log.Close()
	os.Exit(0)
}

func TestAssistantSupervisionNativeCodexStartupResume(t *testing.T) {
	fakeCodexHome(t, testThread)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("PERSONA_TEST_SUPERVISION_CODEX", record)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nexec " + shellQuote(exe) + " -test.run='^TestFakeSupervisionCodexServer$'\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "codex"), []byte(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	n := codexTestNode(t)
	n.dir = t.TempDir()
	withFakeHub(t, n)
	cwd, policy := t.TempDir(), prompts.RenderSupervisionInstructions()
	proc := func(id string) *assistantProc {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.procs[id]
	}
	send := func(id, instructions string) string {
		t.Helper()
		raw, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": id, "launcher": "codex",
			"cwd": cwd, "text": "Changed synthetic task facts", "supervision_instructions": instructions})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			ID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.ID == "" {
			t.Fatalf("native Codex send: %s (%v)", raw, err)
		}
		cliWait(t, "native Codex turn completion", func() bool {
			p := proc(result.ID)
			if p == nil {
				return false
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			return !p.busy
		})
		return result.ID
	}
	id := send("", policy)
	t.Cleanup(func() { n.stopAssistant(id) })
	first := proc(id)
	send(id, policy)
	if proc(id) != first {
		t.Fatal("new task facts restarted the native Codex process")
	}
	otherID := "11111111-2222-4333-8444-555555555555"
	t.Setenv("PERSONA_FAKE_CODEX_THREAD", otherID)
	side := send("", "")
	t.Cleanup(func() { n.stopAssistant(side) })
	if side != otherID || proc(side).cwd != cwd || proc(side).model.SupervisionInstructions != "" {
		t.Fatal("ordinary native Codex chat inherited its neighbor's role")
	}
	path := filepath.Join(t.TempDir(), "rollout-2026-11-15T00-00-00-"+id+".jsonl")
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd}})
	if err := os.WriteFile(path, append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.paths[id] = path
	n.mu.Unlock()
	send(id, "")
	demoted := proc(id)
	if demoted == first || demoted.model.SupervisionInstructions != "" {
		t.Fatal("native Codex did not resume without the revoked policy")
	}
	send(id, policy)
	if proc(id) == demoted || proc(id).model.SupervisionInstructions != policy {
		t.Fatal("native Codex did not resume with its granted policy")
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var starts, resumes, policies int
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m struct {
			Method string `json:"method"`
			Params struct {
				DeveloperInstructions string `json:"developerInstructions"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &m) != nil || m.Method != "thread/start" && m.Method != "thread/resume" {
			continue
		}
		if m.Method == "thread/start" {
			starts++
		} else {
			resumes++
		}
		if strings.Contains(m.Params.DeveloperInstructions, "## Task supervision") {
			policies++
			if !strings.HasSuffix(m.Params.DeveloperInstructions, policy) || strings.Count(m.Params.DeveloperInstructions, policy) != 1 {
				t.Fatal("native Codex startup changed or duplicated the static policy bytes")
			}
		}
	}
	if starts != 2 || resumes != 2 || policies != 2 {
		t.Fatalf("native startup receipts: starts=%d resumes=%d policy-bearing=%d; want 2 each", starts, resumes, policies)
	}
	t.Logf("native startup receipts: starts=%d resumes=%d exact-policy-bearing=%d", starts, resumes, policies)
}
