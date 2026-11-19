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
			if want := []proto.HiddenAgentDir{{Pattern: "/work", Enabled: true}}; slices.Equal(rules, want) != allow {
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
	if got.IsError || !strings.Contains(got.Content[0].Text, "/work (on; matches 1 sessions)") {
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
	if rules, _, _ := f.HiddenAgentDirs(context.Background(), "mac"); !slices.Equal(rules, []proto.HiddenAgentDir{{Pattern: "/work", Enabled: false}}) {
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
	if want := []proto.HiddenAgentDir{{Pattern: "/other", Enabled: true}, {Pattern: "/work", Enabled: true}}; !slices.Equal(rules, want) {
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
	if got.IsError || !strings.Contains(got.Content[0].Text, "~/.bots/botfarm/* (on; matches 2 sessions)") ||
		!strings.Contains(got.Content[0].Text, "~/.bots/botfarm (on; matches 2 sessions)") {
		t.Fatalf("get: %+v", got)
	}
	// A rule written with the followed path hides the folder named through the link too.
	f.set(t, "mac", proto.HiddenAgentDir{Pattern: "/data/home/u/.bots", Enabled: true})
	if !ft.s.directoryHiddenSession("mac", "bot-link") || ft.s.directoryHiddenSession("mac", "mine") {
		t.Fatal("a rule on the followed path")
	}
}
