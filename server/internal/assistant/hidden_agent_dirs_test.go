package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// memAgentDirs keeps hidden Agent folders like the devices do (internal/devices agentdirs.go): per machine, one edit at
// a time, the assistant told after each change.
type memAgentDirs struct {
	mu      sync.Mutex
	rules   map[string][]proto.HiddenAgentDir
	err     error
	changed func(machine string)
}

func (f *memAgentDirs) HiddenAgentDirs(_ context.Context, id string) ([]proto.HiddenAgentDir, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, "", f.err
	}
	return slices.Clone(f.rules[id]), "", nil
}

func (f *memAgentDirs) EditHiddenAgentDirs(_ context.Context, id string, edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)) ([]proto.HiddenAgentDir, error) {
	f.mu.Lock()
	next, err := edit(slices.Clone(f.rules[id]))
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	for i := range next { // as the devices store them: a rule without an action hides
		if next[i].Action == "" {
			next[i].Action = proto.AgentDirHide
		}
	}
	f.rules[id] = slices.Clone(next)
	f.mu.Unlock()
	if f.changed != nil {
		f.changed(id)
	}
	return next, nil
}

// set replaces machine's rules, as a PUT from the settings page does.
func (f *memAgentDirs) set(t *testing.T, machine string, rules ...proto.HiddenAgentDir) {
	t.Helper()
	if _, err := f.EditHiddenAgentDirs(context.Background(), machine, func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) { return rules, nil }); err != nil {
		t.Fatal(err)
	}
}

func useAgentDirs(s *Service) *memAgentDirs {
	f := &memAgentDirs{rules: map[string][]proto.HiddenAgentDir{}, changed: s.HiddenAgentDirsChanged}
	s.agentDirs = f
	return f
}

func hideExposureDirectory(s *Service, machine, pattern string) {
	f, ok := s.agentDirs.(*memAgentDirs)
	if !ok {
		f = useAgentDirs(s)
	}
	f.mu.Lock()
	f.rules[machine] = []proto.HiddenAgentDir{{Pattern: pattern, Enabled: true}}
	f.mu.Unlock()
}

// The rules are per computer, cover every runtime, use the blocked folders' pattern syntax on the working folder and
// what is under it, and a rule switched off hides nothing.
func TestHiddenDirectoryRules(t *testing.T) {
	ft := newFleetTest(t)
	if ft.s.hiddenByDirectory("mac", proto.Session{Cwd: "/work/a"}) {
		t.Fatal("no rules source hid a session")
	}
	f := useAgentDirs(ft.s)
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/work/*", Enabled: true}, proto.HiddenAgentDir{Pattern: "~/.automation", Enabled: true},
		proto.HiddenAgentDir{Pattern: "*botfarm*", Enabled: true})
	for _, agent := range []string{"claude", "codex", "pi"} {
		c := proto.Session{SessionID: "ambient", Cwd: "/work/a/src", Agent: agent, StartedByPersona: true}
		if !ft.s.hiddenByDirectory("mac", c) || ft.s.hiddenByDirectory("other", c) {
			t.Fatalf("runtime/machine policy: %s", agent)
		}
	}
	for cwd, want := range map[string]bool{
		"/home/u/.automation/job/1":            true, // ~ is that computer's home
		"/home/u/.automation-copy":             false,
		"/home/u/.bots/botfarm/workspaces/x/y": true, // a folder name glob anywhere in the path
		"/work":                                false,
		"/code/app":                            false,
		"":                                     false,
	} {
		if got := ft.s.hiddenByDirectory("mac", proto.Session{SessionID: "s", Cwd: cwd}); got != want {
			t.Errorf("%q: %v", cwd, got)
		}
	}
	if ft.s.hiddenByDirectory("mac", proto.Session{Cwd: "/work/a", Assistant: true}) {
		t.Fatal("an assistant conversation was hidden")
	}
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/work/*", Enabled: false})
	if ft.s.hiddenByDirectory("mac", proto.Session{Cwd: "/work/a"}) {
		t.Fatal("a rule switched off still hides")
	}
	// A failed read (the devices retry it after hiddenAgentDirsRetry) hides nothing: the rules are off by default, so
	// a database hiccup must not take every session of the computer out of the lists.
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/work/*", Enabled: true})
	f.mu.Lock()
	f.err = errors.New("database away")
	f.mu.Unlock()
	for _, cwd := range []string{"/code/app", "/work/a"} {
		if ft.s.hiddenByDirectory("mac", proto.Session{SessionID: "s", Cwd: cwd}) {
			t.Fatalf("a failed rules read hid %s", cwd)
		}
	}
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	if !ft.s.hiddenByDirectory("mac", proto.Session{SessionID: "s", Cwd: "/work/a"}) || ft.s.hiddenByDirectory("mac", proto.Session{SessionID: "s", Cwd: "/code/app"}) {
		t.Fatal("rules not applied again after the read recovered")
	}
}

func settingsTool(s *Service, args string) proto.ToolResult {
	return s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "c1", Arguments: json.RawMessage(args)})
}

// Changes made through persona_settings wait for the user's own approval on the phone, like agents.blocked_claude_dirs.
func TestHiddenDirectorySettingConfirmation(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "declined", true: "approved"}[allow], func(t *testing.T) {
			ft := newFleetTest(t)
			f := useAgentDirs(ft.s)
			go func() {
				q, ok := awaitCard(ft.s)
				if !ok {
					t.Error("missing phone confirmation")
					return
				}
				if !strings.Contains(q.Summary, "/work") || !strings.Contains(q.Summary, "Mac") {
					t.Errorf("card does not say what changes where: %q", q.Summary)
				}
				raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
				if _, err := ft.s.Action(context.Background(), Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
					t.Error(err)
				}
			}()
			result := settingsTool(ft.s, `{"action":"add","key":"agents.hidden_dirs","value":{"machine":"mac","pattern":"/work/"}}`)
			if result.IsError {
				t.Fatalf("tool: %+v", result)
			}
			rules, _, _ := f.HiddenAgentDirs(context.Background(), "mac")
			if want := []proto.HiddenAgentDir{{Pattern: "/work", Enabled: true, Action: proto.AgentDirBlock}}; slices.Equal(rules, want) != allow {
				t.Fatalf("approval=%v rules=%+v", allow, rules)
			}
		})
	}
}

// get lists every computer's rules; add switches an existing rule; remove takes "<machine id>:<pattern>" like the
// blocked folders' setting; nothing to change asks nothing.
func TestHiddenDirectorySettingActions(t *testing.T) {
	ft := newFleetTest(t)
	f := useAgentDirs(ft.s)
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/work", Enabled: true})
	ft.d.setSessions("mac", []proto.Session{{SessionID: "a", Cwd: "/work/a"}, {SessionID: "b", Cwd: "/code"}})
	got := settingsTool(ft.s, `{"action":"get","key":"agents.hidden_dirs"}`)
	if got.IsError || !strings.Contains(got.Content[0].Text, "/work (hide, on; matches 1 sessions)") {
		t.Fatalf("get: %+v", got)
	}
	if got := settingsTool(ft.s, `{"action":"add","key":"agents.hidden_dirs","value":{"machine":"mac","pattern":"/work"}}`); got.IsError || !strings.Contains(got.Content[0].Text, "already") {
		t.Fatalf("no-op add: %+v", got)
	}
	approve := func() {
		go func() {
			q, ok := awaitCard(ft.s)
			if !ok {
				t.Error("missing phone confirmation")
				return
			}
			yes := true
			raw, _ := json.Marshal(proto.Resolution{Allow: &yes})
			_, _ = ft.s.Action(context.Background(), Action{Op: "answer", InteractionID: q.ID, Answer: raw})
		}()
	}
	approve()
	if got := settingsTool(ft.s, `{"action":"add","key":"agents.hidden_dirs","value":{"machine":"mac","pattern":"/work","enabled":false}}`); got.IsError {
		t.Fatalf("switch off: %+v", got)
	}
	if rules, _, _ := f.HiddenAgentDirs(context.Background(), "mac"); !slices.Equal(rules, []proto.HiddenAgentDir{{Pattern: "/work", Enabled: false, Action: proto.AgentDirHide}}) {
		t.Fatalf("switched off: %+v", rules)
	}
	approve()
	if got := settingsTool(ft.s, `{"action":"remove","key":"agents.hidden_dirs","id":"mac:/work"}`); got.IsError {
		t.Fatalf("remove: %+v", got)
	}
	if rules, _, _ := f.HiddenAgentDirs(context.Background(), "mac"); len(rules) != 0 {
		t.Fatalf("removed: %+v", rules)
	}
	for _, bad := range []string{
		`{"action":"remove","key":"agents.hidden_dirs","id":"mac:/work"}`,
		`{"action":"add","key":"agents.hidden_dirs","value":{"machine":"nope","pattern":"/work"}}`,
		`{"action":"add","key":"agents.hidden_dirs","value":{"machine":"mac","pattern":"relative"}}`,
		`{"action":"add","key":"agents.hidden_dirs","value":{"machine":"mac","pattern":"*"}}`,
		`{"action":"add","key":"agents.hidden_dirs","value":{"machine":"mac","pattern":"/**"}}`,
		`{"action":"set","key":"agents.hidden_dirs","value":{}}`,
	} {
		if got := settingsTool(ft.s, bad); !got.IsError {
			t.Errorf("%s: %+v", bad, got)
		}
	}
}

// An approval applies its own change to the rules as they are then: a rule added while the card waited stays.
func TestHiddenDirectoryApprovalDoesNotOverwriteNewerRules(t *testing.T) {
	ft := newFleetTest(t)
	f := useAgentDirs(ft.s)
	change, err := ft.s.changeHiddenAgentDirs(context.Background(), "add", json.RawMessage(`{"machine":"mac","pattern":"/work"}`))
	if err != nil {
		t.Fatal(err)
	}
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/other", Enabled: true})
	if _, err = change.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	rules, _, _ := f.HiddenAgentDirs(context.Background(), "mac")
	if want := []proto.HiddenAgentDir{{Pattern: "/other", Enabled: true, Action: proto.AgentDirHide}, {Pattern: "/work", Enabled: true, Action: proto.AgentDirBlock}}; !slices.Equal(rules, want) {
		t.Fatalf("got %+v", rules)
	}
}

// A home reached through a link (/home/u -> /data/home/u): Codex records the working folder with the link
// followed, and a rule written with ~ still hides it — in the rules, agents_list and the count get shows (#8578).
func TestHiddenDirectoryHomeThroughLink(t *testing.T) {
	ft := newFleetTest(t)
	ft.f.mu.Lock()
	ft.f.sessions["mac"] = nil // the fake hub reaches mac's node, so agents_list lists it
	ft.f.mu.Unlock()
	ft.d.mu.Lock()
	ft.d.machines["mac"].homeReal = "/data/home/u"
	ft.d.mu.Unlock()
	f := useAgentDirs(ft.s)
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "~/.bots/botfarm/*", Enabled: true}, proto.HiddenAgentDir{Pattern: "~/.bots/botfarm", Enabled: true})
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "bot-real", Agent: "codex", Cwd: "/data/home/u/.bots/botfarm/workspaces/w1", Title: "Farm resolved", Status: proto.StatusWorking, UpdatedAt: time.Now()},
		{SessionID: "bot-link", Agent: "claude", Cwd: "/home/u/.bots/botfarm/workspaces/w2", Title: "Farm linked", Status: proto.StatusWorking, UpdatedAt: time.Now()},
		{SessionID: "mine", Agent: "codex", Cwd: "/data/home/u/code/app", Title: "Visible work", Status: proto.StatusWorking, UpdatedAt: time.Now()},
	})
	for sid, want := range map[string]bool{"bot-real": true, "bot-link": true, "mine": false} {
		if got := ft.s.directoryHiddenSession("mac", sid); got != want {
			t.Errorf("%s hidden=%v", sid, got)
		}
	}
	if got := ft.s.describeAgents(context.Background(), "all", "", ""); strings.Contains(got, "Farm") || !strings.Contains(got, "Visible work") {
		t.Fatalf("agents_list: %s", got)
	}
	got := settingsTool(ft.s, `{"action":"get","key":"agents.hidden_dirs"}`)
	if got.IsError || !strings.Contains(got.Content[0].Text, "~/.bots/botfarm/* (hide, on; matches 2 sessions)") ||
		!strings.Contains(got.Content[0].Text, "~/.bots/botfarm (hide, on; matches 2 sessions)") {
		t.Fatalf("get: %+v", got)
	}
	// A rule written with the followed path hides the folder named through the link too.
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/data/home/u/.bots", Enabled: true})
	if !ft.s.directoryHiddenSession("mac", "bot-link") || ft.s.directoryHiddenSession("mac", "mine") {
		t.Fatal("a rule on the followed path")
	}
}

// A rule's action through persona_settings (#10053): a new rule blocks unless hide is asked for, an existing rule keeps
// its action unless one is given, switching it says so on the card, and an unknown action is refused.
func TestAgentDirSettingAction(t *testing.T) {
	ft := newFleetTest(t)
	f := useAgentDirs(ft.s)
	ctx := context.Background()
	apply := func(value string) (string, []proto.HiddenAgentDir) {
		t.Helper()
		change, err := ft.s.changeHiddenAgentDirs(ctx, "add", json.RawMessage(value))
		if err != nil {
			t.Fatal(err)
		}
		if change.apply == nil {
			rules, _, _ := f.HiddenAgentDirs(ctx, "mac")
			return change.summary, rules
		}
		if _, err := change.apply(ctx); err != nil {
			t.Fatal(err)
		}
		rules, _, _ := f.HiddenAgentDirs(ctx, "mac")
		return change.cardText(i18n.En), rules
	}
	card, rules := apply(`{"machine":"mac","pattern":"~/.bots/runs"}`)
	if !slices.Equal(rules, []proto.HiddenAgentDir{{Pattern: "~/.bots/runs", Enabled: true, Action: proto.AgentDirBlock}}) || !strings.HasPrefix(card, "Block") {
		t.Fatalf("a new rule: %q %+v", card, rules)
	}
	if card, _ = apply(`{"machine":"mac","pattern":"~/.bots/runs","enabled":false}`); !strings.Contains(card, "Switch off") {
		t.Fatalf("switch off keeps the action: %q", card)
	}
	card, rules = apply(`{"machine":"mac","pattern":"~/.bots/runs","action":"hide"}`)
	if !slices.Equal(rules, []proto.HiddenAgentDir{{Pattern: "~/.bots/runs", Enabled: true, Action: proto.AgentDirHide}}) || !strings.Contains(card, "to Hide") {
		t.Fatalf("to hide: %q %+v", card, rules)
	}
	if card, _ = apply(`{"machine":"mac","pattern":"~/.bots/runs"}`); !strings.Contains(card, "already") || !strings.Contains(card, "hide, on") {
		t.Fatalf("no action given keeps hide: %q", card)
	}
	if card, rules = apply(`{"machine":"mac","pattern":"/scratch","action":"hide"}`); !strings.HasPrefix(card, "Hide") || rules[1].Action != proto.AgentDirHide {
		t.Fatalf("a new hide rule: %q %+v", card, rules)
	}
	if _, err := ft.s.changeHiddenAgentDirs(ctx, "add", json.RawMessage(`{"machine":"mac","pattern":"/x","action":"delete"}`)); err == nil {
		t.Fatal("an unknown action")
	}
}

// A node that does not block yet still lists a blocked folder's sessions: the manager leaves them out of every view
// it has, as it does a hidden folder's, so nothing leaks while that node updates (#10053).
func TestBlockedDirectoryOlderNodeFallsBackToHide(t *testing.T) {
	ft := newFleetTest(t)
	ft.f.mu.Lock()
	ft.f.sessions["mac"] = nil
	ft.f.mu.Unlock()
	f := useAgentDirs(ft.s)
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "~/automation", Enabled: true, Action: proto.AgentDirBlock})
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "bot-needs", Agent: "codex", Cwd: "/home/u/automation/a", Title: "Blocked question", Status: proto.StatusBlocked, UpdatedAt: time.Now()},
		{SessionID: "bot-done", Agent: "claude", Cwd: "/home/u/automation/b", Title: "Blocked completion", Status: proto.StatusDone, LastText: "Complete", UpdatedAt: time.Now()},
		{SessionID: "mine", Agent: "codex", Cwd: "/code/app", Title: "Visible work", Status: proto.StatusWorking, UpdatedAt: time.Now()},
	})
	for _, state := range []string{"all", "active", "needs_you", "unread", "done"} {
		if got := ft.s.describeAgents(context.Background(), state, "", ""); strings.Contains(got, "Blocked") {
			t.Fatalf("agents_list state=%s: %s", state, got)
		}
	}
	fleet := ft.s.fleetSnapshot()
	if fleet.NeedsYou != 0 || fleet.Unread != 0 || fleet.Working != 1 {
		t.Fatalf("counts: %+v", fleet)
	}
	agents, ixs, told := ft.s.fleetInputs()
	if got := linkAttention(i18n.Default, agents, ixs, told); got.NeedsYou != 0 {
		t.Fatalf("needs-you: %+v", got)
	}
	if !ft.s.muted("mac", "bot-done") {
		t.Fatal("a blocked folder's completion can notify")
	}
}
