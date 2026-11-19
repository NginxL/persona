package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The profile the main chat's new conversations start in (#4633): only the assistant offers it, private is stored as
// none, a provider change keeps it, and the main chat's other uses inherit it.
func TestModelConfigDirIsOfferedAndPatchedOnlyForTheAssistant(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	r := modelRequest(s, "GET", "", "")
	var list struct {
		Purposes []ModelPurpose `json:"purposes"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Purposes {
		want := p.ID == "assistant"
		if got := slices.Equal(p.ConfigDirs, []string{"private", "legacy"}) && slices.Contains(p.Fields, "config_dir"); got != want {
			t.Fatalf("%s: config_dirs %v fields %v", p.ID, p.ConfigDirs, p.Fields)
		}
	}
	for _, c := range []struct {
		id, body string
		code     int
	}{
		{"agent.new", `{"revision":0,"config_dir":"legacy"}`, 400},
		{"kb", `{"revision":0,"config_dir":"legacy"}`, 400},
		{"assistant", `{"revision":0,"config_dir":"~/.claude"}`, 400},
		{"assistant", `{"revision":0,"config_dir":""}`, 400},
		{"assistant", `{"revision":0,"config_dir":"legacy"}`, 200},
		{"assistant", `{"revision":1,"provider":"launcher.claude"}`, 200}, // a provider change keeps the profile
	} {
		if r := modelRequest(s, "PATCH", c.id, c.body); r.Code != c.code {
			t.Fatalf("%s %s: %d %s", c.id, c.body, r.Code, r.Body.String())
		}
	}
	for _, id := range []string{"assistant", "schedule"} {
		if b, err := s.resolveModel(ctx, id); err != nil || b.ConfigDir != assistantConfigDirLegacy {
			t.Fatalf("%s: %+v %v", id, b, err)
		}
	}
	if b, _ := s.resolveModel(ctx, "agent.new"); b.ConfigDir != "" {
		t.Fatalf("Agents took the main chat's profile: %+v", b)
	}
	// A use of the main chat given a provider of its own (a scheduled task's) keeps the inherited profile.
	if b := mergeModel(ModelBinding{Provider: "launcher.claude", ConfigDir: assistantConfigDirLegacy}, ModelBinding{Provider: "launcher.gw"}); b.ConfigDir != assistantConfigDirLegacy {
		t.Fatalf("a provider change dropped the profile: %+v", b)
	}
	if r := modelRequest(s, "PATCH", "assistant", `{"revision":2,"config_dir":"private"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if rec, _ := s.modelRecord(ctx, "assistant"); rec.Binding.ConfigDir != "" {
		t.Fatalf("private is stored as none: %+v", rec.Binding)
	}
	if r := modelRequest(s, "PATCH", "assistant", `{"revision":3,"config_dir":"legacy"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := modelRequest(s, "PATCH", "assistant", `{"revision":4,"config_dir":null}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if b, _ := s.resolveModel(ctx, "assistant"); b.ConfigDir != "" {
		t.Fatalf("config_dir not cleared: %+v", b)
	}
}

// A message never carries the profile, and choosing one restarts no process: it is not in the execution fingerprint.
func TestModelConfigDirIsNotSentWithMessagesNorFingerprinted(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	apply := func() map[string]any {
		t.Helper()
		p := map[string]any{}
		if err := s.applyModel(ctx, "assistant", p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := apply()
	setModel(t, s, "assistant", ModelBinding{ConfigDir: assistantConfigDirLegacy})
	after := apply()
	if _, set := after["config_dir"]; set {
		t.Fatalf("a message carried the profile: %+v", after)
	}
	if after["model_fingerprint"] != before["model_fingerprint"] {
		t.Fatal("choosing a profile changed the fingerprint")
	}
	fp, err := s.modelFingerprint(ctx, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	setModel(t, s, "assistant", ModelBinding{})
	if again, _ := s.modelFingerprint(ctx, "assistant"); again != fp {
		t.Fatal("the binding fingerprint follows the profile")
	}
}

// A new conversation starts in the chosen profile unless it names its own, is a guest's or runs on another agent; a
// node too old for config_dir starts it as before.
func TestNewConversationStartsInTheMainChatsProfile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		p     StartParams
		older bool
		want  any // nil: not sent
	}{
		{"chosen", StartParams{Text: "hello", Untitled: true}, false, assistantConfigDirLegacy},
		{"own", StartParams{Text: "hello", Untitled: true, ConfigDir: "~/.config/claude-assistant"}, false, "~/.config/claude-assistant"},
		{"guest", StartParams{Text: "hello", Untitled: true, GuestTools: []string{}}, false, nil},
		{"codex", StartParams{Text: "hello", Untitled: true, Launcher: "codex"}, false, nil},
		{"older node", StartParams{Text: "hello", Untitled: true}, true, nil},
		// A purpose that inherits from Agents, not the main chat, starts there too: an extension's /agent (#10859).
		{"lark.agent", StartParams{Text: "hello", Untitled: true, Purpose: "lark.agent"}, false, assistantConfigDirLegacy},
		{"lark.agent guest", StartParams{Text: "hello", Untitled: true, Purpose: "lark.agent", GuestTools: []string{}}, false, nil},
		// The Inbox AI twin acts on what strangers sent: it stays in Persona's own profile, away from the user's setup.
		{"inbox.agent", StartParams{Text: "hello", Untitled: true, Purpose: purposeAgent, Origin: OriginInbox}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.settings = &Settings{MachineID: "server"}
			f.refuseConfigDir = tc.older
			s := newTestService(t, f)
			setModel(t, s, "assistant", ModelBinding{ConfigDir: assistantConfigDirLegacy})
			if _, err := s.Start(context.Background(), tc.p); err != nil {
				t.Fatal(err)
			}
			if len(f.sends) != 1 {
				t.Fatalf("%d starts", len(f.sends))
			}
			if got := f.sends[0]["config_dir"]; got != tc.want {
				t.Fatalf("config_dir = %v, want %v", got, tc.want)
			}
		})
	}
	// A launcher whose agent the hub cannot tell (the machine's list did not come) is asked for in the profile: the node
	// refuses one before starting anything for another agent than Claude Code, and the start goes again without it, as
	// on a node too old for one (#6884).
	t.Run("codex the hub cannot tell", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.settings = &Settings{MachineID: "server"}
		f.launcherList = nil
		var asked []any // config_dir of every assistant.send, refused or not (written under f.mu)
		f.onAssistantSend = func(_ *fakeHub, p map[string]any) { asked = append(asked, p["config_dir"]) }
		s := newTestService(t, f)
		setModel(t, s, "assistant", ModelBinding{ConfigDir: assistantConfigDirLegacy})
		if _, err := s.Start(context.Background(), StartParams{Text: "hello", Untitled: true, Launcher: "codex-work"}); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if want := []any{assistantConfigDirLegacy, nil}; !slices.Equal(asked, want) || len(f.sends) != 1 || f.sends[0]["launcher"] != "codex-work" {
			t.Fatalf("asked the node in %v, started as %v", asked, f.sends)
		}
	})
	t.Run("not chosen", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.settings = &Settings{MachineID: "server"}
		s := newTestService(t, f)
		if _, err := s.Start(context.Background(), StartParams{Text: "hello", Untitled: true}); err != nil {
			t.Fatal(err)
		}
		if got, set := f.sends[0]["config_dir"]; set {
			t.Fatalf("Persona's own profile is the node's default, nothing to send: %v", got)
		}
	})
}

// The spare process waits for the next new conversation, which adopts it only in the same profile
// (node/assistant_spare.go): it is warmed there, and without it on a node too old for config_dir.
func TestWarmSpareStartsInTheMainChatsProfile(t *testing.T) {
	for _, older := range []bool{false, true} {
		f := newFakeHub().withMachine()
		f.settings = &Settings{MachineID: "server"}
		f.refuseConfigDir = older
		s := newTestService(t, f)
		setModel(t, s, "assistant", ModelBinding{ConfigDir: assistantConfigDirLegacy})
		s.warm("")
		var warms []map[string]any
		f.mu.Lock()
		for _, c := range f.calls {
			if c.Method == DeviceAssistantWarm {
				var p map[string]any
				_ = json.Unmarshal(c.Params, &p)
				warms = append(warms, p)
			}
		}
		f.mu.Unlock()
		if len(warms) == 0 {
			t.Fatalf("older=%v: not warmed", older)
		}
		last := warms[len(warms)-1]
		if want := any(assistantConfigDirLegacy); older && last["config_dir"] != nil || !older && last["config_dir"] != want {
			t.Fatalf("older=%v: warmed %v", older, warms)
		}
	}
}

// The spare on a launcher whose agent the hub cannot tell is asked for in the main chat's profile: the node refuses one
// for another agent than Claude Code before starting anything, and the spare is warmed again without it (#6884).
func TestWarmSpareOnALauncherTheHubCannotTell(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Launcher: "codex-work"}
	f.launcherList = nil
	s := newTestService(t, f)
	setModel(t, s, "assistant", ModelBinding{ConfigDir: assistantConfigDirLegacy})
	s.warm("")
	var warms []any // config_dir of every assistant.warm, refused or not
	f.mu.Lock()
	for _, c := range f.calls {
		if c.Method == DeviceAssistantWarm {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			warms = append(warms, p["config_dir"])
		}
	}
	f.mu.Unlock()
	if want := []any{assistantConfigDirLegacy, nil}; !slices.Equal(warms, want) {
		t.Fatalf("warmed in %v, want %v", warms, want)
	}
}

// After /clear the next main conversation starts on the carried pick, in the main chat's profile (startMain, startOn).
// The spare is warmed for that start as it runs: the same launcher, fingerprint and profile, or the start never adopts
// it (node/assistant_spare.go). A carried Codex pick has no profile to warm in; a node too old for config_dir gets
// both without it.
func TestWarmSpareForACarriedPickStartsInTheMainChatsProfile(t *testing.T) {
	for _, tc := range []struct {
		pick  string
		older bool
		want  any // nil: not sent
	}{
		{`{"launcher":"claude","model":"opus"}`, false, assistantConfigDirLegacy},
		{`{"launcher":"claude","model":"opus"}`, true, nil},
		{`{"launcher":"cx","model":"gpt-5"}`, false, nil},
	} {
		ctx := context.Background()
		s, f := pickFixture(t)
		f.refuseConfigDir = tc.older
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw", ConfigDir: assistantConfigDirLegacy})
		if r := putPick(t, s, "main", tc.pick); r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		if _, err := s.clearMain(ctx, ""); err != nil {
			t.Fatal(err)
		}
		s.warm("")
		var warm map[string]any
		f.mu.Lock()
		for _, c := range f.calls {
			if c.Method == DeviceAssistantWarm {
				warm = nil
				_ = json.Unmarshal(c.Params, &warm)
			}
		}
		f.mu.Unlock()
		if warm == nil {
			t.Fatalf("%s older=%v: not warmed", tc.pick, tc.older)
		}
		if code, sess := startConv(t, s, `{"prompt":"hi","main":true}`); code != 200 {
			t.Fatalf("%s older=%v: start %d %+v", tc.pick, tc.older, code, sess)
		}
		start := f.lastSend(t)
		if start["config_dir"] != tc.want {
			t.Fatalf("%s older=%v: started in %v, want %v", tc.pick, tc.older, start["config_dir"], tc.want)
		}
		for _, k := range []string{"launcher", "model_fingerprint", "config_dir"} {
			if warm[k] != start[k] {
				t.Fatalf("%s older=%v: %s warmed %v, started %v", tc.pick, tc.older, k, warm[k], start[k])
			}
		}
	}
}

// Switching to a model profile and back keeps the main chat's profile choice, as it keeps its runtime, including one
// made between the switch and the rollback; a model profile cannot set one.
func TestModelProfileSwitchKeepsTheMainChatsConfigDir(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example", Model: "m1"}})
	setSettings(t, s, func(p *Settings) { p.Launcher = "gw" })
	original := ModelBinding{Provider: "launcher.gw", Model: "test-chat", ConfigDir: assistantConfigDirLegacy}
	setModel(t, s, "assistant", original)
	storeProfiles(t, st, ModelProfile{ID: "economy", Name: "Economy", Purposes: map[string]ModelBinding{"assistant": {Model: "test-selected"}}})
	by := ProfileActor{Kind: "device", Name: "Test device"}
	if _, err := s.applyModelProfile(ctx, "economy", by); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.modelRecord(ctx, "assistant"); r.Binding != (ModelBinding{Model: "test-selected", ConfigDir: assistantConfigDirLegacy}) {
		t.Fatalf("switch lost the profile choice: %+v", r.Binding)
	}
	setModel(t, s, "assistant", ModelBinding{Model: "test-selected"}) // back to Persona's own profile since the switch
	if _, err := s.rollbackModelProfile(ctx, by); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.modelRecord(ctx, "assistant"); r.Binding != (ModelBinding{Provider: "launcher.gw", Model: "test-chat"}) {
		t.Fatalf("rollback did not keep the choice made since the switch: %+v", r.Binding)
	}
	p := ModelProfile{ID: "own", Name: "Own", Purposes: map[string]ModelBinding{"assistant": {ConfigDir: assistantConfigDirLegacy}}}
	if err := checkModelProfile(&p, nil); err == nil {
		t.Fatal("a model profile set the main chat's profile")
	}
}

// A model profile stored holding a config_dir (written past checkModelProfile, which refuses one) is read without it:
// it shows no change it cannot make, switching to it and rolling it back keep the main chat's choice, current and
// made since the switch, and an edit saves it clean.
func TestStoredModelProfileConfigDirIsNotTheProfiles(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example", Model: "m1"}})
	setSettings(t, s, func(p *Settings) { p.Launcher = "gw" })
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw", Model: "test-chat"}) // Persona's own profile
	storeProfiles(t, st,
		ModelProfile{ID: "economy", Name: "Economy", Purposes: map[string]ModelBinding{"assistant": {Model: "test-selected", ConfigDir: assistantConfigDirLegacy}}},
		ModelProfile{ID: "same", Name: "Same", Purposes: map[string]ModelBinding{"assistant": {Provider: "launcher.gw", Model: "test-chat", ConfigDir: assistantConfigDirLegacy}}})
	for _, p := range profilesOf(t, s).Profiles {
		if p.Purposes["assistant"].ConfigDir != "" {
			t.Fatalf("%s is shown with the main chat's profile: %+v", p.ID, p.Purposes)
		}
		if p.ID == "same" && len(p.Changes) > 0 {
			t.Fatalf("a profile differing in config_dir alone shows changes: %v", p.Changes)
		}
	}
	by := ProfileActor{Kind: "device", Name: "Test device"}
	if _, err := s.applyModelProfile(ctx, "economy", by); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.modelRecord(ctx, "assistant"); r.Binding != (ModelBinding{Model: "test-selected"}) {
		t.Fatalf("switch took the stored profile's config_dir: %+v", r.Binding)
	}
	setModel(t, s, "assistant", ModelBinding{Model: "test-selected", ConfigDir: assistantConfigDirLegacy}) // chosen since the switch
	if _, err := s.rollbackModelProfile(ctx, by); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.modelRecord(ctx, "assistant"); r.Binding != (ModelBinding{Provider: "launcher.gw", Model: "test-chat", ConfigDir: assistantConfigDirLegacy}) {
		t.Fatalf("rollback did not keep the choice made since the switch: %+v", r.Binding)
	}
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw", Model: "test-chat"}) // and back to Persona's own
	if sw, err := s.applyModelProfile(ctx, "same", by); err != nil {
		t.Fatal(err)
	} else if slices.Contains(sw.Changed, "assistant") {
		t.Fatalf("switching to a profile differing in config_dir alone changed the main chat: %v", sw.Changed)
	}
	if r, _ := s.modelRecord(ctx, "assistant"); r.Binding != (ModelBinding{Provider: "launcher.gw", Model: "test-chat"}) {
		t.Fatalf("switch took the stored profile's config_dir: %+v", r.Binding)
	}
	if _, err := s.saveModelProfile(ctx, nil, "economy", by, func(p *ModelProfile, _ bool) error { p.Name = "Cheap"; return nil }); err != nil {
		t.Fatalf("a stored profile holding a config_dir cannot be edited: %v", err)
	}
	var stored modelProfiles
	if _, err := st.Setting(ctx, modelProfilesKey, &stored); err != nil {
		t.Fatal(err)
	}
	for _, p := range stored.Profiles {
		if p.ID == "economy" && p.Purposes["assistant"].ConfigDir != "" {
			t.Fatalf("an edit saved the stored config_dir again: %+v", p.Purposes)
		}
	}
}
