package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// agent_switch (agent_switch.go, #5973): the assistant moves a running Claude Code agent to another launcher and goes
// on with it, now or not at all.

// switchDevs are the manager's computers, whose nodes announced launcher_switch_now unless old says otherwise.
type switchDevs struct {
	*fleetDevs
	old bool
}

func (d *switchDevs) NodeAnnounced(id, capability string) bool {
	return !d.old && (capability == nodeproto.LauncherSwitchNow || capability == nodeproto.LauncherSwitchIdle) && d.Has(id)
}

// switchRig is a manager with the computer "mac" whose agents s1… a test lists; the node's session.modes and
// session.send go to the fake hub (f.calls), answered by f.nodes.
func switchRig(t *testing.T) (*fleetTest, *switchDevs) {
	t.Helper()
	return switchRigOn(t, nil)
}

// switchRigOn is switchRig with st as its database (nil: none).
func switchRigOn(t *testing.T, st Store) (*fleetTest, *switchDevs) {
	t.Helper()
	f := newFakeHub()
	f.sessions["mac"] = nil
	d := &switchDevs{fleetDevs: newFleetDevs()}
	d.add("mac", "Mac")
	opts := Options{Hub: f, Logf: testLogf(t), Devices: d}
	if st != nil {
		opts.Store = func() Store { return st }
	}
	s := New(opts)
	return &fleetTest{t: t, s: s, d: d.fleetDevs, f: f}, d
}

// nodeCalls are the session.modes and session.send calls the node got, in order.
func nodeCalls(f *fakeHub) []DeviceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []DeviceCall
	for _, c := range f.calls {
		if c.Method == DeviceSessionModes || c.Method == DeviceSessionSend || c.Method == DeviceSessionSwitch {
			out = append(out, c)
		}
	}
	return out
}

func callParams(t *testing.T, c DeviceCall) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(c.Params, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func switchTool(ft *fleetTest, args map[string]any) (string, error) {
	raw, _ := json.Marshal(args)
	return ft.s.runTool(context.Background(), "server", "conv1", "agent_switch", raw)
}

// An agent its usage limit stopped is moved and goes on with what it was doing: the pick (from: the launcher it runs
// on), then the continuation with switch_now.
func TestAgentSwitchMovesAUsageLimitedAgent(t *testing.T) {
	ft, _ := switchRig(t)
	reset := time.Now().Add(2 * time.Hour)
	c := limitTurn("s1abcdef01", reset, false)
	c.MachineID, c.Cwd, c.Agent, c.Launcher = "mac", "/code/app", "claude", "sub"
	ft.sessions("mac", c)

	res, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1abcdef", "launcher": "work", "model": "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "Moved") || !strings.Contains(res, "from launcher sub to work") {
		t.Fatalf("result: %s", res)
	}
	calls := nodeCalls(ft.f)
	if len(calls) != 2 || calls[0].Method != DeviceSessionModes || calls[1].Method != DeviceSessionSend {
		t.Fatalf("calls: %+v", calls)
	}
	if p := callParams(t, calls[0]); p["session_id"] != "s1abcdef01" || p["launcher"] != "work" || p["from"] != "sub" || p["model"] != "opus" {
		t.Errorf("pick: %v", p)
	}
	p := callParams(t, calls[1])
	if p["switch_now"] != true || p["apply_switch"] != true || !strings.Contains(p["text"].(string), prompts.RenderAgentLimitContinue("")) {
		t.Errorf("send: %v", p)
	}
	requireAutoContinueSource(t, p)
}

// The node's refusal (a busy agent: switch_busy) comes back as it is, nothing sent; the pick is taken back in case the
// node never got the message.
func TestAgentSwitchRefusedByTheNode(t *testing.T) {
	ft, _ := switchRig(t)
	ft.sessions("mac", proto.Session{SessionID: "s1abcdef01", MachineID: "mac", Cwd: "/code/app", Agent: "claude", Launcher: "sub",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	ft.f.nodes = map[string]nodeAnswer{
		DeviceSessionSend: func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: proto.CodeSwitchBusy, Message: "the agent is working"}
		},
	}
	_, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1abcdef", "launcher": "work", "text": "finish the tests"})
	if err == nil || !strings.Contains(err.Error(), "the agent is working") || !strings.Contains(err.Error(), "Nothing was sent") {
		t.Fatalf("err: %v", err)
	}
	calls := nodeCalls(ft.f)
	if len(calls) != 3 || calls[2].Method != DeviceSessionModes {
		t.Fatalf("calls: %+v", calls)
	}
	if p := callParams(t, calls[2]); p["launcher"] != "sub" || p["from"] != "sub" {
		t.Errorf("take back: %v", p)
	}
}

// Refused before anything reaches the node: no text for an agent that did not stop on a usage limit, the launcher it
// runs on, a stopped agent, an assistant conversation, Codex, a node too old for switch_now.
func TestAgentSwitchRefusals(t *testing.T) {
	ft, d := switchRig(t)
	idle := proto.Session{SessionID: "s1abcdef01", MachineID: "mac", Cwd: "/code/app", Agent: "claude", Launcher: "sub",
		Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()}
	stopped := idle
	stopped.SessionID, stopped.Status = "s2abcdef01", proto.StatusOffline
	codex := idle
	codex.SessionID, codex.Agent = "s3abcdef01", "codex"
	asst := proto.Session{SessionID: "s4abcdef01", MachineID: "mac", Cwd: stateDirOn("/home/u", "", identity.AssistantDirName("")), Status: proto.StatusIdle}
	ft.sessions("mac", idle, stopped, codex, asst)
	for name, args := range map[string]map[string]any{
		"no text":          {"session_id": "s1abcdef", "launcher": "work"},
		"its own launcher": {"session_id": "s1abcdef", "launcher": "sub", "text": "go on"},
		"no launcher":      {"session_id": "s1abcdef", "text": "go on"},
		"stopped":          {"session_id": "s2abcdef", "launcher": "work", "text": "go on"},
		"codex":            {"session_id": "s3abcdef", "launcher": "work", "text": "go on"},
		"assistant":        {"session_id": "s4abcdef", "launcher": "work", "text": "go on"},
		"exit":             {"session_id": "s1abcdef", "launcher": "work", "text": "/exit"},
	} {
		args["machine"] = "mac"
		if res, err := switchTool(ft, args); err == nil {
			t.Errorf("%s: %s", name, res)
		}
	}
	d.old = true
	if _, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1abcdef", "launcher": "work", "text": "go on"}); err == nil ||
		!strings.Contains(err.Error(), "older") {
		t.Errorf("old node: %v", err)
	}
	if calls := nodeCalls(ft.f); len(calls) != 0 {
		t.Fatalf("reached the node: %+v", calls)
	}
}

// agents_list shows what agent_switch needs: the launcher an agent runs on, and that its usage limit stopped it.
func TestAgentsListShowsLauncherAndUsageLimit(t *testing.T) {
	ft, _ := switchRig(t)
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	c := limitTurn("s1abcdef01", reset, false)
	c.MachineID, c.Cwd, c.Agent, c.Launcher = "mac", "/code/app", "claude", "sub"
	ft.sessions("mac", c)
	list := ft.s.describeAgents(context.Background(), "all", "", "")
	if !strings.Contains(list, " · launcher=sub") || !strings.Contains(list, " · usage_limit resets_at="+reset.Format(time.RFC3339)) {
		t.Fatalf("agents_list:\n%s", list)
	}
}

// An agent its node lists off its launcher (off_launcher, #11792: a herdr restart resumed it with a bare
// `claude --resume <id>`) runs on none of it, whatever its record says: agents_list says so, and agent_switch to the
// launcher it was started on moves it back there instead of answering that it runs on it already. Its pick is not
// taken back when the node refuses: picking its own launcher is the switch back there.
func TestAgentSwitchBackOntoTheLauncherAnAgentRunsOff(t *testing.T) {
	ft, _ := switchRig(t)
	off := agentRow("s1abcdef01", "kimi", proto.StatusIdle)
	off.StartedByPersona, off.OffLauncher = true, true
	ft.sessions("mac", off)
	if list := ft.s.describeAgents(context.Background(), "all", "", ""); !strings.Contains(list, " · launcher=kimi (off_launcher:") {
		t.Fatalf("agents_list:\n%s", list)
	}
	res, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1abcdef", "launcher": "kimi", "text": "go on"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "Moved") {
		t.Fatalf("result: %s", res)
	}
	calls := nodeCalls(ft.f)
	if len(calls) != 2 || calls[0].Method != DeviceSessionModes || calls[1].Method != DeviceSessionSend {
		t.Fatalf("calls: %+v", calls)
	}
	if p := callParams(t, calls[0]); p["launcher"] != "kimi" || p["from"] != "kimi" {
		t.Errorf("pick: %v", p)
	}
	if p := callParams(t, calls[1]); p["message_source"] != nil {
		t.Errorf("custom switch text was mislabeled as automatic: %v", p["message_source"])
	}

	ft.f.mu.Lock()
	ft.f.calls = nil
	ft.f.mu.Unlock()
	ft.f.nodes = map[string]nodeAnswer{
		DeviceSessionSend: func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: proto.CodeSwitchBusy, Message: "the agent is working"}
		},
	}
	if _, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1abcdef", "launcher": "kimi", "text": "go on"}); err == nil {
		t.Fatal("the node's refusal did not come back")
	}
	if calls := nodeCalls(ft.f); len(calls) != 2 {
		t.Fatalf("taken back with a pick of its own launcher (a switch back for this one): %+v", calls)
	}

	// To another launcher, a pick whose message did not go is dropped as for any agent: off its launcher, its own
	// launcher picked while that switch waits cancels it (launcherModes).
	ft.f.mu.Lock()
	ft.f.calls = nil
	ft.f.mu.Unlock()
	if _, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1abcdef", "launcher": "work", "text": "go on"}); err == nil {
		t.Fatal("the node's refusal did not come back")
	}
	calls = nodeCalls(ft.f)
	if len(calls) != 3 || calls[2].Method != DeviceSessionModes {
		t.Fatalf("the switch to work was not dropped: %+v", calls)
	}
	if p := callParams(t, calls[2]); p["launcher"] != "kimi" || p["from"] != "kimi" {
		t.Errorf("drop: %v", p)
	}
}
