package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Meeting minutes (meeting.minutes) and the In-meeting conversation (meeting.room), each set apart from its category
// like Lark /agent (#7980, docs/assistant/model-config.md "Meeting minutes and the in-meeting conversation (#7980)").
// Names, addresses and models are placeholders.

// apiProvider is a keyed direct API connection speaking api.
func apiProvider(t *testing.T, s *Service, name, api string) storedLauncher {
	t.Helper()
	key, err := s.sealKey("private-test-key")
	if err != nil {
		t.Fatal(err)
	}
	return storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: name, Kind: "api", API: api,
		BaseURL: "https://" + name + ".example.invalid", Model: name + "-model"}, KeySealed: key}
}

// errorOf is the error a refused request answered.
func errorOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return out.Error
}

// The catalog lists the room and then the minutes right after the background supervisor and before an extension's
// purpose, each the row its own GET answers: its kind and launch, its category as parent, its group, its own fields,
// description and record, Follows <category> while it is empty. The categories name them among their uses;
// meeting.answer stays a read-only use of Information processing.
func TestMeetingPurposesInTheCatalog(t *testing.T) {
	s, f, _ := modelService(t)
	f.mu.Lock()
	f.modelExts = []assistantwire.ModelExtension{{ID: "curator", Name: "Curator"}}
	f.mu.Unlock()
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "main-model", Effort: "high", Runtime: proto.AssistantRuntimeAgentSDK})
	l := s.requestCtxLang(context.Background())
	r := modelRequest(s, "GET", "", "")
	var catalog struct {
		Purposes []ModelPurpose `json:"purposes"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &catalog) != nil {
		t.Fatalf("catalog: %d %s", r.Code, r.Body.String())
	}
	var ids []string
	rows := map[string]ModelPurpose{}
	for _, p := range catalog.Purposes {
		ids = append(ids, p.ID)
		rows[p.ID] = p
	}
	i := slices.Index(ids, purposeSupervise)
	if i < 0 || i+4 > len(ids) || !slices.Equal(ids[i:i+4], []string{purposeSupervise, purposeMeetingRoom, purposeMeetingMinutes, "extension.curator"}) {
		t.Fatalf("catalog order: %v", ids)
	}
	for _, c := range []struct {
		id, kind, launch, parent, group, machine string
		fields                                   []string
	}{
		{purposeMeetingRoom, "launcher", "headless", "assistant", "group_assistant", "server", []string{"provider", "model", "effort"}},
		{purposeMeetingMinutes, "api", "", "kb", "group_processing", "server", purposeOf(t, s, "kb").Fields},
	} {
		p, own := rows[c.id], purposeOf(t, s, c.id)
		if !reflect.DeepEqual(p, own) {
			t.Fatalf("%s listed %+v, by its id %+v", c.id, p, own)
		}
		parent := purposeOf(t, s, c.parent)
		follows := parent.Effective
		follows.Runtime = "" // the room never takes the main chat's runtime
		if p.Kind != c.kind || p.Launch != c.launch || p.Parent != c.parent || p.CategoryID != c.parent || p.Group != mc(l, c.group) ||
			p.MachineID != c.machine || !slices.Equal(p.Fields, c.fields) || len(p.Uses) != 0 || len(p.Runtimes) != 0 {
			t.Fatalf("%s: %+v", c.id, p)
		}
		if p.Name != purposeName(l, c.id) || p.Description != purposeDescription(l, c.id) || p.Description == "" ||
			p.Revision != 0 || p.Override != (ModelBinding{}) || p.InheritedFrom != purposeName(l, c.parent) || p.Effective != follows {
			t.Fatalf("%s while it follows %s (%+v): %+v", c.id, c.parent, parent.Effective, p)
		}
	}
	if minutes, kb := rows[purposeMeetingMinutes], rows["kb"]; !slices.Equal(minutes.ProviderIDs, kb.ProviderIDs) || minutes.Status != kb.Status {
		t.Fatalf("the minutes offer %v (%s), Information processing %v (%s)", minutes.ProviderIDs, minutes.Status, kb.ProviderIDs, kb.Status)
	}
	usesOf := func(id string) []string {
		var names []string
		for _, u := range rows[id].Uses {
			names = append(names, u.Name)
		}
		return names
	}
	if got := usesOf("kb"); !slices.Contains(got, mc(l, "use_meeting_minutes")) || !slices.Contains(got, mc(l, "use_meeting_answer")) {
		t.Fatalf("Information processing's uses: %v", got)
	}
	if got := usesOf("assistant"); !slices.Contains(got, mc(l, "use_meeting_room")) {
		t.Fatalf("Main chat's uses: %v", got)
	}
	if answer := purposeOf(t, s, purposeMeetingAnswer); answer.CategoryID != "kb" || len(answer.Fields) != 0 || answer.InheritedFrom != purposeName(l, "kb") {
		t.Fatalf("meeting.answer is no longer a read-only use of Information processing: %+v", answer)
	}
}

// Each has its own revision from 0: a save, a sparse update, a stale revision refused by both verbs, explicit nulls
// back to following its category (and its later changes). Its record is model_config.<id>; neither category moves.
func TestMeetingPurposesEditAndReset(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "room-cc", Model: "room-default"}}, apiProvider(t, s, "direct", "messages"))
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "main-model", Effort: "high"})
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.direct", Model: "kb-model"})
	l := s.requestCtxLang(ctx)
	categories := func() string {
		raw, _ := json.Marshal([]modelRecord{modelRecordOf(t, s, "assistant"), modelRecordOf(t, s, "kb")})
		return string(raw)
	}
	before := categories()
	for _, c := range []struct{ id, parent, provider, model string }{
		{purposeMeetingRoom, "assistant", "launcher.room-cc", "room-model"},
		{purposeMeetingMinutes, "kb", "launcher.direct", "minutes-model"},
	} {
		follows := purposeOf(t, s, c.parent).Effective
		p := purposeOf(t, s, c.id)
		if p.Revision != 0 || p.Override != (ModelBinding{}) || p.Effective != follows || p.InheritedFrom != purposeName(l, c.parent) {
			t.Fatalf("%s unset: %+v; want %+v", c.id, p, follows)
		}
		own := ModelBinding{Provider: c.provider, Model: c.model, Effort: "low"}
		if w := modelRequest(s, http.MethodPut, c.id, fmt.Sprintf(`{"revision":0,"provider":%q,"model":%q,"effort":"low"}`, c.provider, c.model)); w.Code != http.StatusOK {
			t.Fatalf("%s save: %d %s", c.id, w.Code, w.Body.String())
		}
		if p = purposeOf(t, s, c.id); p.Revision != 1 || p.Override != own || p.Effective != own || p.InheritedFrom != "" {
			t.Fatalf("%s saved: %+v", c.id, p)
		}
		if w := modelRequest(s, http.MethodPatch, c.id, `{"revision":1,"effort":"medium"}`); w.Code != http.StatusOK {
			t.Fatalf("%s sparse update: %d %s", c.id, w.Code, w.Body.String())
		}
		own.Effort = "medium"
		if p = purposeOf(t, s, c.id); p.Revision != 2 || p.Override != own || p.Effective != own {
			t.Fatalf("%s after a sparse update: %+v", c.id, p)
		}
		for _, method := range []string{http.MethodPut, http.MethodPatch} {
			if w := modelRequest(s, method, c.id, `{"revision":0,"model":"stale"}`); w.Code != http.StatusConflict {
				t.Fatalf("%s %s stale: %d %s", c.id, method, w.Code, w.Body.String())
			}
		}
		var stored modelRecord
		if ok, err := st.Setting(ctx, modelKey(c.id), &stored); !ok || err != nil || stored.Revision != 2 || stored.Binding != own {
			t.Fatalf("%s's record model_config.%s: %+v %v %v", c.id, c.id, stored, ok, err)
		}
		if got := categories(); got != before {
			t.Fatalf("saving %s moved a category: %s; was %s", c.id, got, before)
		}
		if w := modelRequest(s, http.MethodPut, c.id, `{"revision":2,"provider":null,"model":null,"effort":null}`); w.Code != http.StatusOK {
			t.Fatalf("%s reset: %d %s", c.id, w.Code, w.Body.String())
		}
		if p = purposeOf(t, s, c.id); p.Revision != 3 || p.Override != (ModelBinding{}) || p.Effective != follows || p.InheritedFrom != purposeName(l, c.parent) {
			t.Fatalf("%s reset: %+v; want %+v", c.id, p, follows)
		}
	}
	// Following again includes the category's later changes.
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.direct", Model: "kb-changed"})
	if b, err := s.resolveModel(ctx, purposeMeetingMinutes); err != nil || b.Model != "kb-changed" {
		t.Fatalf("the minutes after Information processing changed: %+v %v", b, err)
	}
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "main-changed"})
	if b, err := s.resolveModel(ctx, purposeMeetingRoom); err != nil || b.Model != "main-changed" {
		t.Fatalf("the room after Main chat changed: %+v %v", b, err)
	}
}

// Model profiles switch the five categories only, and category-default assignment the two it may set: neither writes
// the meeting purposes' records, and one set apart keeps its own binding.
func TestMeetingPurposesOutsideProfilesAndAssignment(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "room-cc"}}, apiProvider(t, s, "direct", "messages"),
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "fresh"}})
	room := ModelBinding{Provider: "launcher.room-cc", Model: "room-model", Effort: "low"}
	minutes := ModelBinding{Provider: "launcher.direct", Model: "minutes-model"}
	setModel(t, s, purposeMeetingRoom, room)
	setModel(t, s, purposeMeetingMinutes, minutes)
	records := func() string {
		raw, _ := json.Marshal([]modelRecord{modelRecordOf(t, s, purposeMeetingRoom), modelRecordOf(t, s, purposeMeetingMinutes)})
		return string(raw)
	}
	before := records()
	storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{
		"assistant": {Provider: "launcher.claude", Model: "profile-main"}, "kb": {Provider: "launcher.direct", Model: "profile-kb"},
	}})
	if _, err := s.applyModelProfile(ctx, "selected", ProfileActor{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.resolveModel(ctx, "kb"); b.Model != "profile-kb" {
		t.Fatalf("the profile was not applied: %+v", b)
	}
	assigned, _ := s.assignIfDefault(ctx, "launcher.fresh", assignablePurposes)
	if got := records(); got != before || slices.Contains(assigned, purposeMeetingRoom) || slices.Contains(assigned, purposeMeetingMinutes) {
		t.Fatalf("a profile or an assignment wrote them: %s (assigned %v); was %s", got, assigned, before)
	}
	if b, _ := s.resolveModel(ctx, purposeMeetingRoom); b != room {
		t.Fatalf("the room: %+v", b)
	}
	if b, _ := s.resolveModel(ctx, purposeMeetingMinutes); b != minutes {
		t.Fatalf("the minutes: %+v", b)
	}
}

// The room runs on Claude Code's CLI only: it offers and accepts a Claude Code launcher on the assistant's computer and
// a Messages API connection, refuses Codex (a ~/.zshrc alias or a managed one) and a Responses connection with the
// localized reason before anything is stored, and refuses a runtime. Its effective binding carries no runtime even
// while the main chat runs on the Agent SDK; following a main chat on Codex, its status says rooms can't start, until a
// Claude Code launcher is picked here while the main chat stays on Codex.
func TestMeetingRoomRunsOnlyOnClaudeCode(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "cc-wrapper", Source: "zshrc"}, {Name: "cx", Agent: "codex", Source: "zshrc"}}}
	f.mu.Unlock()
	putLaunchers(t, s, apiProvider(t, s, "msgs", "messages"), apiProvider(t, s, "resp", "responses"),
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex-gw", Agent: "codex", BaseURL: "https://gw.example.invalid/v1", Model: "gpt"}})
	l := s.requestCtxLang(ctx)
	claudeOnly := i18n.T(l, "assistant-models.err_meeting_room_claude_only")

	p := purposeOf(t, s, purposeMeetingRoom)
	for _, id := range []string{"launcher.claude", "launcher.cc-wrapper", "launcher.msgs"} {
		if !slices.Contains(p.ProviderIDs, id) {
			t.Fatalf("%s not offered: %v", id, p.ProviderIDs)
		}
	}
	for _, id := range []string{"launcher.cx", "launcher.codex-gw", "launcher.resp"} {
		if slices.Contains(p.ProviderIDs, id) {
			t.Fatalf("%s offered: %v", id, p.ProviderIDs)
		}
	}
	if main := purposeOf(t, s, "assistant"); !slices.Contains(main.ProviderIDs, "launcher.cx") {
		t.Fatalf("the main chat does not offer the Codex alias, so this proves nothing: %v", main.ProviderIDs)
	}
	for _, provider := range []string{"launcher.cx", "launcher.codex-gw", "launcher.resp"} {
		for _, method := range []string{http.MethodPut, http.MethodPatch} {
			w := modelRequest(s, method, purposeMeetingRoom, fmt.Sprintf(`{"revision":0,"provider":%q}`, provider))
			if w.Code != http.StatusBadRequest || errorOf(t, w.Body.Bytes()) != claudeOnly {
				t.Fatalf("%s %s: %d %s; want 400 %q", method, provider, w.Code, w.Body.String(), claudeOnly)
			}
		}
	}
	w := modelRequest(s, http.MethodPatch, purposeMeetingRoom, `{"revision":0,"runtime":"agentsdk"}`)
	if w.Code != http.StatusBadRequest || errorOf(t, w.Body.Bytes()) != i18n.T(l, "assistant-models.err_runtime_assistant_only") {
		t.Fatalf("runtime: %d %s", w.Code, w.Body.String())
	}
	if r := modelRecordOf(t, s, purposeMeetingRoom); r.Revision != 0 || r.Binding != (ModelBinding{}) {
		t.Fatalf("a refused save was stored: %+v", r)
	}
	if w := modelRequest(s, http.MethodPut, purposeMeetingRoom, `{"revision":0,"provider":"launcher.cc-wrapper","model":"room-model"}`); w.Code != http.StatusOK {
		t.Fatalf("a Claude Code launcher: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPut, purposeMeetingRoom, `{"revision":1,"provider":"launcher.msgs"}`); w.Code != http.StatusOK {
		t.Fatalf("a Messages connection: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPut, purposeMeetingRoom, `{"revision":2,"provider":null}`); w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}

	// A main chat on the Agent SDK: the room follows its launcher, on the CLI.
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "main-model", Runtime: proto.AssistantRuntimeAgentSDK})
	main := purposeOf(t, s, "assistant")
	p = purposeOf(t, s, purposeMeetingRoom)
	if main.Effective.Runtime != proto.AssistantRuntimeAgentSDK || p.Effective.Runtime != "" || p.Effective.Provider != main.Effective.Provider ||
		p.Effective.Model != "main-model" || slices.Contains(p.Fields, "runtime") || len(p.Runtimes) != 0 {
		t.Fatalf("the room %+v, the main chat %+v", p, main)
	}
	if b, err := s.resolveModel(ctx, purposeMeetingRoom); err != nil || b.Runtime != "" {
		t.Fatalf("resolved with a runtime: %+v %v", b, err)
	}

	// A main chat on Codex: the room says why it can't start, and a model saved alone on it is refused.
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.cx"})
	notClaude := mc(l, "status_meeting_room_not_claude", i18n.Args{"launcher": "cx"})
	if p = purposeOf(t, s, purposeMeetingRoom); p.Effective.Provider != "launcher.cx" || p.Status != notClaude {
		t.Fatalf("following Codex: %+v; want status %q", p, notClaude)
	}
	if w := modelRequest(s, http.MethodPatch, purposeMeetingRoom, `{"revision":3,"model":"room-model"}`); w.Code != http.StatusBadRequest || errorOf(t, w.Body.Bytes()) != claudeOnly {
		t.Fatalf("a model on Codex: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, purposeMeetingRoom, `{"revision":3,"provider":"launcher.claude"}`); w.Code != http.StatusOK {
		t.Fatalf("Claude Code while the main chat stays on Codex: %d %s", w.Code, w.Body.String())
	}
	if p = purposeOf(t, s, purposeMeetingRoom); p.Effective.Provider != "launcher.claude" || p.Status == notClaude {
		t.Fatalf("set apart: %+v", p)
	}
	if b, _ := s.resolveModel(ctx, "assistant"); b.Provider != "launcher.cx" {
		t.Fatalf("the main chat moved: %+v", b)
	}
}

// A room starts on the In-meeting conversation's launcher, model and effort, and on the main chat's while it follows
// it; the main chat itself does not move.
func TestMeetingRoomStartsOnItsModel(t *testing.T) {
	ctx := context.Background()
	mainChat := ModelBinding{Provider: "launcher.claude", Model: "main-model", Effort: "high"}
	for _, c := range []struct {
		name string
		room *ModelBinding
		want ModelBinding
	}{
		{"follows the main chat", nil, mainChat},
		{"set apart", &ModelBinding{Provider: "launcher.room-cc", Model: "room-model", Effort: "low"}, ModelBinding{Provider: "launcher.room-cc", Model: "room-model", Effort: "low"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRoomFixture(t)
			putLaunchers(t, r.s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "room-cc"}})
			if err := r.s.saveModelRecord(ctx, "assistant", 0, mainChat); err != nil {
				t.Fatal(err)
			}
			if c.room != nil {
				if err := r.s.saveModelRecord(ctx, purposeMeetingRoom, 0, *c.room); err != nil {
					t.Fatal(err)
				}
			}
			r.roomUp(meetingRec, "participant")
			sends := r.sends()
			if len(sends) != 1 {
				t.Fatalf("sends %v", sends)
			}
			start := sends[0]
			if start["launcher"] != c.want.Provider[len("launcher."):] || start["model"] != c.want.Model || start["effort"] != c.want.Effort ||
				start["runtime"] != nil || start["launch"] != "headless" || start["model_fingerprint"] == "" {
				t.Fatalf("room start %v; want %+v", start, c.want)
			}
			main := map[string]any{}
			if err := r.s.applyModel(ctx, "assistant", main); err != nil || main["model"] != "main-model" || main["effort"] != "high" {
				t.Fatalf("the main chat: %v %v", main, err)
			}
		})
	}
}

// Deleting a provider either one uses is refused, naming it: Meeting minutes' own provider, Information processing's
// explicit one while the minutes follow it, the room's own. One Information processing only picks by itself, with
// another it would pick without it, is no use of the minutes either.
func TestMeetingPurposesProtectTheirProviders(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, apiProvider(t, s, "api1", "messages"), apiProvider(t, s, "api2", "messages"),
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "room-only"}})
	l := s.requestCtxLang(ctx)
	minutes, room, kb := purposeName(l, purposeMeetingMinutes), purposeName(l, purposeMeetingRoom), purposeName(l, "kb")
	uses := func(name string) []string {
		t.Helper()
		got, err := s.providerModelUses(ctx, "launcher."+name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	refused := func(name string, names ...string) {
		t.Helper()
		w := request(s, http.MethodDelete, "/api/v1/launchers/"+name, "")
		want := i18n.T(l, "assistant-models_launchers.err_in_use", i18n.Args{"uses": names[0]})
		if len(names) > 1 {
			want = i18n.T(l, "assistant-models_launchers.err_in_use", i18n.Args{"uses": names[0] + i18n.T(l, "assistant-models_launchers.list_sep") + names[1]})
		}
		if w.Code != http.StatusConflict || errorOf(t, w.Body.Bytes()) != want {
			t.Fatalf("deleting %s: %d %s; want 409 %q", name, w.Code, w.Body.String(), want)
		}
	}

	if b, _ := s.resolveModel(ctx, purposeMeetingMinutes); b.Provider != "launcher.api1" {
		t.Fatalf("the minutes do not follow Information processing's automatic pick: %+v", b)
	}
	if got := uses("api1"); len(got) != 0 {
		t.Fatalf("an automatic pick, api2 left: %v", got)
	}
	if w := modelRequest(s, http.MethodPatch, purposeMeetingMinutes, `{"revision":0,"provider":"launcher.api2"}`); w.Code != http.StatusOK {
		t.Fatalf("the minutes' own provider: %d %s", w.Code, w.Body.String())
	}
	if got := uses("api2"); !slices.Equal(got, []string{minutes}) {
		t.Fatalf("uses of the minutes' own provider: %v", got)
	}
	refused("api2", minutes)
	if w := modelRequest(s, http.MethodPatch, purposeMeetingMinutes, `{"revision":1,"provider":null}`); w.Code != http.StatusOK {
		t.Fatalf("following again: %d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(s, http.MethodPatch, "kb", `{"revision":0,"provider":"launcher.api1"}`); w.Code != http.StatusOK {
		t.Fatalf("Information processing's provider: %d %s", w.Code, w.Body.String())
	}
	if got := uses("api1"); !slices.Equal(got, []string{kb, minutes}) {
		t.Fatalf("uses of Information processing's explicit provider: %v; want %q and %q", got, kb, minutes)
	}
	refused("api1", kb, minutes)

	if got := uses("room-only"); len(got) != 0 {
		t.Fatalf("before the room is given it: %v", got)
	}
	if w := modelRequest(s, http.MethodPatch, purposeMeetingRoom, `{"revision":0,"provider":"launcher.room-only"}`); w.Code != http.StatusOK {
		t.Fatalf("the room's own provider: %d %s", w.Code, w.Body.String())
	}
	if got := uses("room-only"); !slices.Equal(got, []string{room}) {
		t.Fatalf("uses of the room's own provider: %v", got)
	}
	refused("room-only", room)
	if w := modelRequest(s, http.MethodPatch, purposeMeetingRoom, `{"revision":1,"provider":null}`); w.Code != http.StatusOK {
		t.Fatalf("following again: %d %s", w.Code, w.Body.String())
	}
	for _, name := range []string{"room-only", "api2"} {
		if w := request(s, http.MethodDelete, "/api/v1/launchers/"+name, ""); w.Code != http.StatusOK {
			t.Fatalf("deleting %s once nothing uses it: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// What a launcher of a computer's own runs is read on the assistant's computer, where rooms start: Model settings' list
// keeps the first computer's entry for a name several have, and hub.json's agent_type there overrides what the node
// says. A relay that is Codex on the laptop but Claude Code on the assistant's computer is offered and taken; one that is
// Claude Code on the laptop but Codex there is not; hub.json's word decides both ways.
func TestMeetingRoomReadsTheAssistantsComputer(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.sessions["laptop"] = nil
	f.devices = []Device{{ID: "laptop", Name: "laptop", Online: true}, {ID: "server", Name: "server", Online: true}}
	f.zshrc = map[string][]proto.Launcher{
		"laptop": {{Name: "relay", Agent: "codex", Source: "zshrc"}, {Name: "other", Agent: "claude", Source: "zshrc"}},
		"server": {{Name: "relay", Source: "zshrc"}, {Name: "other", Agent: "codex", Source: "zshrc"},
			{Name: "fixed", Agent: "codex", Source: "zshrc"}, {Name: "turned", Source: "zshrc"}},
	}
	f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
		"server": {{Launcher: proto.Launcher{Name: "fixed"}, AgentType: "claude"}, {Launcher: proto.Launcher{Name: "turned"}, AgentType: "codex"}},
	}
	f.mu.Unlock()
	listed := map[string]ModelProvider{}
	for _, v := range s.modelProviders(ctx) {
		listed[v.ID] = v
	}
	if relay, other := listed["launcher.relay"], listed["launcher.other"]; relay.Agent != "codex" || other.Agent != "claude" ||
		!slices.Contains(relay.Machines, "server") || !slices.Contains(other.Machines, "server") {
		t.Fatalf("the list does not keep the laptop's entries, so this proves nothing: %+v %+v", relay, other)
	}
	claudeOnly := i18n.T(s.requestCtxLang(ctx), "assistant-models.err_meeting_room_claude_only")
	p := purposeOf(t, s, purposeMeetingRoom)
	for id, offered := range map[string]bool{"launcher.relay": true, "launcher.fixed": true, "launcher.other": false, "launcher.turned": false} {
		if slices.Contains(p.ProviderIDs, id) != offered {
			t.Fatalf("%s offered %v: %v", id, !offered, p.ProviderIDs)
		}
	}
	for _, provider := range []string{"launcher.other", "launcher.turned"} {
		w := modelRequest(s, http.MethodPut, purposeMeetingRoom, fmt.Sprintf(`{"revision":0,"provider":%q}`, provider))
		if w.Code != http.StatusBadRequest || errorOf(t, w.Body.Bytes()) != claudeOnly {
			t.Fatalf("%s: %d %s; want 400 %q", provider, w.Code, w.Body.String(), claudeOnly)
		}
	}
	for i, provider := range []string{"launcher.relay", "launcher.fixed"} {
		if w := modelRequest(s, http.MethodPut, purposeMeetingRoom, fmt.Sprintf(`{"revision":%d,"provider":%q}`, i, provider)); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", provider, w.Code, w.Body.String())
		}
		if p = purposeOf(t, s, purposeMeetingRoom); p.Effective.Provider != provider || p.Status == mc(s.requestCtxLang(ctx), "status_meeting_room_not_claude", i18n.Args{"launcher": provider[len("launcher."):]}) {
			t.Fatalf("%s saved: %+v", provider, p)
		}
	}
}

// A hub.json wrapper of another agent is refused even while the assistant's computer can't answer and the provider list
// does not name it: hub.json says what it runs. One it says runs Claude Code is still taken, as before.
func TestMeetingRoomRefusesAConfiguredWrapperOfAnotherAgentOffline(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.down = map[string]bool{"server": true} // offline, and its node's list does not come (no launcherList)
	f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
		"server": {{Launcher: proto.Launcher{Name: "other-wrapper"}, AgentType: "codex"}, {Launcher: proto.Launcher{Name: "cc-wrapper"}, AgentType: "claude"}},
	}
	f.mu.Unlock()
	if slices.ContainsFunc(s.modelProviders(ctx), func(v ModelProvider) bool { return v.ID == "launcher.other-wrapper" }) {
		t.Fatal("the provider list names the wrapper, so this proves nothing")
	}
	claudeOnly := i18n.T(s.requestCtxLang(ctx), "assistant-models.err_meeting_room_claude_only")
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		w := modelRequest(s, method, purposeMeetingRoom, `{"revision":0,"provider":"launcher.other-wrapper"}`)
		if w.Code != http.StatusBadRequest || errorOf(t, w.Body.Bytes()) != claudeOnly {
			t.Fatalf("%s a Codex wrapper offline: %d %s; want 400 %q", method, w.Code, w.Body.String(), claudeOnly)
		}
	}
	if r := modelRecordOf(t, s, purposeMeetingRoom); r.Revision != 0 || r.Binding != (ModelBinding{}) {
		t.Fatalf("a refused save was stored: %+v", r)
	}
	if w := modelRequest(s, http.MethodPut, purposeMeetingRoom, `{"revision":0,"provider":"launcher.cc-wrapper"}`); w.Code != http.StatusOK {
		t.Fatalf("a Claude Code wrapper offline: %d %s", w.Code, w.Body.String())
	}
}

// Following a main chat on a runtime that runs it (DSH declaring caps.assistant), which the provider list leaves out, the
// room's status names that launcher and says rooms need Claude Code, in the request's language.
func TestMeetingRoomExplainsAMainChatOnARuntime(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "dsh-rt", Source: "dsh", Agent: "dsh", Assistant: true}, {Name: "cc", Source: "zshrc"}}}
	f.mu.Unlock()
	if names := pickerNames(t, s, "assistant"); !slices.Contains(names, "dsh-rt") {
		t.Fatalf("the main chat can't run on the runtime, so this proves nothing: %v", names)
	}
	if slices.ContainsFunc(s.modelProviders(ctx), func(v ModelProvider) bool { return v.ID == "launcher.dsh-rt" }) {
		t.Fatal("the provider list names the runtime's launcher, so this proves nothing")
	}
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.dsh-rt"})
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		res, err := serveHTTP(ctx, s, assistantwire.HTTPRequest{Method: http.MethodGet, Path: "/api/v1/model-config/purposes/" + purposeMeetingRoom,
			Header: map[string][]string{"Accept-Language": {string(l)}}}, s.lang)
		if err != nil || res.Status != http.StatusOK {
			t.Fatalf("%s: %v %d %s", l, err, res.Status, res.Body)
		}
		var out struct {
			Purpose ModelPurpose `json:"purpose"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatal(err)
		}
		want := mc(l, "status_meeting_room_not_claude", i18n.Args{"launcher": "dsh-rt"})
		if p := out.Purpose; p.Effective.Provider != "launcher.dsh-rt" || p.Status != want || !strings.Contains(want, "dsh-rt") {
			t.Fatalf("%s following the runtime: %+v; want status %q", l, p, want)
		}
	}
}
