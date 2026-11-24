package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// qaKindConfig is the kind of acceptance item 23 (#13472): a space whose information carries emotion, with a daily
// rhythm, pushes capped at P1, reviewed by its own routine and listed under a group of its own.
func qaKindConfig() assistantwire.TrackKindConfig {
	c := validKind()
	c.DefaultCategories = []string{categoryEmotion}
	c.TagFocus = []string{categoryRelationships}
	c.Rhythms = []assistantwire.TrackRhythm{{Title: "Daily check-in", Cron: "0 21 * * *", Prompt: "Ask how the day went", TopicPolicy: "new", Enabled: true}}
	c.Attention.PushCap = assistantwire.LevelP1
	c.Supervision = "routine"
	c.Sidebar = assistantwire.TrackSidebar{Group: "Testing"}
	return c
}

// trackSettingsService is the health hub's assistant on a registry it can write, with qa-kind and a track of it
// (qa-k) made through the API, and a conversation of that track (qa-k-chat).
func trackSettingsService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := healthHub()
	f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "qa-k-chat", Cwd: "/home/me/.persona/tracks/qa-k",
		Assistant: true, Space: "qa-k", Title: "QA chat"})
	st := &trackKindMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}}}
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	body, _ := json.Marshal(assistantwire.TrackKindCreate{Slug: "qa-kind", Title: "QA kind", Config: qaKindConfig()})
	if w := serve(s, http.MethodPost, "/api/v1/assistant/track-kinds", string(body)); w.Code != http.StatusCreated {
		t.Fatalf("create qa-kind: %d %s", w.Code, w.Body)
	}
	if w := serve(s, http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-k","kind":"qa-kind","title":"QA track"}`); w.Code != http.StatusCreated {
		t.Fatalf("create qa-k: %d %s", w.Code, w.Body)
	}
	return s, f
}

// scheduledTasks are the assistant's scheduled tasks as it keeps them.
func scheduledTasks(t *testing.T, s *Service) []Task {
	t.Helper()
	sc, err := s.lockedSchedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.mu.Unlock()
	return slices.Clone(sc.tasks)
}

// A kind made of config alone takes effect (#13472, acceptance item 23): a new track of it gets its rhythm as a
// scheduled task in the track; its conversations carry its default categories; its tagging looks for its focus and
// categories; a push of its conversations that is no card is held to its push cap; its processes are told its kind.
// The health space keeps all it had.
func TestTrackKindSettingsTakeEffect(t *testing.T) {
	ctx := context.Background()
	s, f := trackSettingsService(t)

	// The rhythm: one cron task in the track, a new conversation each run, on.
	var made []Task
	for _, task := range scheduledTasks(t, s) {
		if task.Space == "qa-k" {
			made = append(made, task)
		}
	}
	if len(made) != 1 || made[0].Repeat != repeatCron || made[0].Cron != "0 21 * * *" || !made[0].Enabled || !made[0].FreshEachRun ||
		made[0].Title != "Daily check-in" || made[0].Prompt != "Ask how the day went" || made[0].CreatedBy != trackRhythmCreatedBy {
		t.Fatalf("the rhythm's tasks: %+v", made)
	}
	// A track of a kind without rhythms gets none.
	before := len(scheduledTasks(t, s))
	if w := serve(s, http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-plain","kind":"space","title":"QA plain"}`); w.Code != http.StatusCreated {
		t.Fatalf("create qa-plain: %d %s", w.Code, w.Body)
	}
	if got := len(scheduledTasks(t, s)); got != before {
		t.Errorf("a track without rhythms: %d tasks, want %d", got, before)
	}

	// Its conversations carry emotion; its tagging looks for relationships and emotion.
	if cats, _, _ := s.categoryStateIn(ctx, "", "qa-k", true); !slices.Equal(cats, []string{categoryEmotion}) {
		t.Errorf("a new conversation's categories = %v, want [emotion]", cats)
	}
	if got := s.infoTagFocus(ctx, "qa-k"); !slices.Equal(got, []string{categoryEmotion, categoryRelationships}) {
		t.Errorf("tag focus = %v", got)
	}
	if got := s.trackKindOf(ctx, "qa-k"); got != "qa-kind" {
		t.Errorf("kind = %q", got)
	}

	// notify_user from the track: at most P1, whatever it asks for.
	for _, level := range []string{"", assistantwire.LevelP0, assistantwire.LevelP1} {
		args, _ := json.Marshal(map[string]string{"title": "qa note", "body": "qa body", "push_level": level})
		if out, isErr := spaceTool(t, s, "qa-k-chat", "notify_user", string(args)); isErr {
			t.Fatalf("notify_user %q: %s", level, out)
		}
	}
	p := f.pushed()
	if len(p) != 3 {
		t.Fatalf("pushes: %+v", p)
	}
	// A push sent without a level keeps "" (the hub derives P1 for it); P0 and P1 both reach the hub as P1.
	for i, want := range []string{"", assistantwire.LevelP1, assistantwire.LevelP1} {
		if p[i].Level != want {
			t.Errorf("push %d: level %q, want %q", i, p[i].Level, want)
		}
	}
	// The main conversation's notify_user keeps its level.
	if _, isErr := spaceTool(t, s, "work-0000-0000", "notify_user", `{"title":"qa build","body":"all green","push_level":"P0"}`); isErr {
		t.Fatal("main notify_user")
	}
	if p := f.pushed(); len(p) != 4 || p[3].Level != assistantwire.LevelP0 {
		t.Errorf("the main conversation's push: %+v", p)
	}
	// A turn's finished push stays at the P2 the hub derives; a card keeps its level.
	done := Notification{Kind: assistantwire.NotifyDone, SessionID: "qa-k-chat"}
	s.capTrackPush(ctx, &done)
	card := Notification{Kind: assistantwire.NotifyInput, SessionID: "qa-k-chat", Level: assistantwire.LevelP0, Interaction: &proto.Interaction{ID: "ix-1"}}
	s.capTrackPush(ctx, &card)
	if done.Level != "" || card.Level != assistantwire.LevelP0 {
		t.Errorf("done %q, card %q", done.Level, card.Level)
	}
}

// Prepare names each directory's track kind for its processes (#13472, PERSONA_TRACK_KIND): orchestration for the
// assistant's own, space for the health space, a track's own kind for its directory.
func TestPrepareNamesTrackKind(t *testing.T) {
	ctx := context.Background()
	s, f := spaceTrackService(t)
	s.devs = spaceDevs{fleetDevs: newFleetDevs()}
	for _, c := range []struct{ space, kind string }{{"", trackKindOrchestration}, {SpaceHealth, trackKindSpace}, {"qa-plain", trackKindSpace}} {
		f.prepared = nil
		if _, err := s.prepareSpace(ctx, "server", c.space); err != nil {
			t.Fatalf("%q: %v", c.space, err)
		}
		if len(f.prepared) != 1 || f.prepared[0]["track_kind"] != c.kind {
			t.Errorf("%q: track_kind %v, want %q", c.space, f.prepared, c.kind)
		}
	}
}

// Push caps (#13472): a push that is no card is held to its track's cap; "" stays for the hub to derive when what it
// implies is already quiet enough; a cap of P0 changes nothing.
func TestCappedLevel(t *testing.T) {
	for _, c := range []struct{ kind, level, limit, want string }{
		{NotifyPush, "", assistantwire.LevelP0, ""},
		{NotifyPush, assistantwire.LevelP0, "", assistantwire.LevelP0},
		{NotifyPush, assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP1},
		{NotifyPush, "", assistantwire.LevelP1, ""},
		{NotifyPush, "", assistantwire.LevelP2, assistantwire.LevelP2},
		{NotifyPush, assistantwire.LevelP2, assistantwire.LevelP1, assistantwire.LevelP2},
		{assistantwire.NotifyDone, "", assistantwire.LevelP2, ""},
		{assistantwire.NotifyDone, assistantwire.LevelP0, assistantwire.LevelP2, assistantwire.LevelP2},
	} {
		if got := cappedLevel(c.kind, c.level, c.limit); got != c.want {
			t.Errorf("%s %q capped at %q = %q, want %q", c.kind, c.level, c.limit, got, c.want)
		}
	}
}

// A track's usage rules only tighten (#13472): a rule for "*" covers all of the track's information, one for a
// category the information carrying it or not known; the policy's switches stay on for anything stricter; with no
// rules (the health space's, the main space's) nothing changes.
func TestTrackUsageRulesTighten(t *testing.T) {
	general := infoUse{Known: true, SourceTrack: "qa-k"}
	if got := usage(general, sceneLockScreen, extensionGrant{}); got != usageAllow {
		t.Fatalf("general on the lock screen = %v", got)
	}
	all := general
	all.Rules = []assistantwire.TrackUsageRule{{Scene: "lock_screen", Category: "*", Rule: "redact"}, {Scene: "elsewhere", Category: "*", Rule: "deny"}}
	if got := usage(all, sceneLockScreen, extensionGrant{}); got != usageRedact {
		t.Errorf("a * rule on general information = %v, want redact", got)
	}
	if p := policyOf(all); !p.Redact || !p.DenyMainRead || p.ConfirmOutbound || p.NoKBWrite {
		t.Errorf("policy with * rules = %+v", p)
	}
	emotionRule := []assistantwire.TrackUsageRule{{Scene: "others", Category: categoryEmotion, Rule: "deny"}}
	for _, c := range []struct {
		info infoUse
		want usageRule
	}{
		{infoUse{Known: true, SourceTrack: "qa-k", Rules: emotionRule}, usageAllow},
		{infoUse{Categories: []string{categoryFinance}, Known: true, SourceTrack: "qa-k", Rules: emotionRule}, usageConfirm},
		{infoUse{Categories: []string{categoryEmotion}, Known: true, SourceTrack: "qa-k", Rules: emotionRule}, usageDeny},
		{infoUse{SourceTrack: "qa-k", Rules: emotionRule}, usageDeny}, // not known: it may carry emotion
	} {
		if got := usage(c.info, sceneOthers, extensionGrant{}); got != c.want {
			t.Errorf("%+v to others = %v, want %v", c.info, got, c.want)
		}
	}
	// A rule never loosens: allow over a sensitive floor keeps the floor.
	loose := infoUse{Categories: []string{categoryEmotion}, Known: true, SourceTrack: "qa-k",
		Rules: []assistantwire.TrackUsageRule{{Scene: "kb_write", Category: "*", Rule: "allow"}}}
	if got := usage(loose, sceneKBWrite, extensionGrant{}); got != usageDeny {
		t.Errorf("a looser rule = %v, want deny", got)
	}
	// Every scene and every kind of information: without rules usage is the assistant's own.
	for _, sc := range scenes {
		for _, info := range []infoUse{{Known: true}, {Categories: []string{categoryHealth}, Known: true}, {}} {
			if usage(info, sc, extensionGrant{}) != baseUsage(info, sc, extensionGrant{}) {
				t.Errorf("scene %v, %+v: rules-free usage changed", sc, info)
			}
		}
	}
}

// The track's rules reach the call sites through its registry entry: a track whose kind redacts the lock screen for
// everything redacts its pushes, without a sensitive category; the health space and the main space are unchanged.
func TestTrackUsageRulesReachPolicy(t *testing.T) {
	ctx := context.Background()
	s, _ := trackKindService(t)
	healthBefore := s.trackPolicy(ctx, SpaceHealth)
	focusBefore := s.infoTagFocus(ctx, SpaceHealth)
	cfg := validKind()
	cfg.Usage = []assistantwire.TrackUsageRule{{Scene: "lock_screen", Category: "*", Rule: "redact"}}
	body, _ := json.Marshal(assistantwire.TrackKindCreate{Slug: "qa-quiet", Title: "QA quiet", Config: cfg})
	if w := serve(s, http.MethodPost, "/api/v1/assistant/track-kinds", string(body)); w.Code != http.StatusCreated {
		t.Fatalf("create qa-quiet: %d %s", w.Code, w.Body)
	}
	if w := serve(s, http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-q","kind":"qa-quiet","title":"QA q"}`); w.Code != http.StatusCreated {
		t.Fatalf("create qa-q: %d %s", w.Code, w.Body)
	}
	if p := s.trackPolicy(ctx, "qa-q"); p != (TrackPolicy{Redact: true}) {
		t.Errorf("qa-q's policy = %+v, want redact only", p)
	}
	if p := s.exitPolicy(ctx, "qa words", "", "qa-q"); !p.Redact {
		t.Errorf("qa-q's exit policy = %+v, want redact", p)
	}
	if s.trackPolicy(ctx, "") != (TrackPolicy{}) || s.trackPolicy(ctx, SpaceHealth) != healthBefore ||
		!reflect.DeepEqual(s.infoTagFocus(ctx, SpaceHealth), focusBefore) || !slices.Equal(focusBefore, []string{categoryHealth}) {
		t.Error("the main or the health space changed")
	}
	if s.anyTrackCapped(ctx) {
		t.Error("no track caps its pushes")
	}
}
