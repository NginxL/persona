package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// infoUseService is a service with the registry tracks qa-plain (no default categories) and qa-finance (finance), a
// store that keeps information categories, and conversations noted in the main space, both tracks and the health space.
func infoUseService(t *testing.T) (*Service, *fakeHub, *infoCategoryMemStore) {
	t.Helper()
	ctx := context.Background()
	st := &infoCategoryMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "qa-finance", Kind: trackKindSpace, Template: trackTemplateSpace, DefaultCategories: []string{categoryFinance}},
		{Slug: "qa-plain", Kind: trackKindSpace, Template: trackTemplateSpace},
	}}}
	f := newFakeHub()
	f.infoTagModel = true
	s := newServiceOn(t, f, st)
	s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: "qa-machine", SessionID: "qa-main", Space: ""},
		{MachineID: "qa-machine", SessionID: "qa-plain-1", Space: "qa-plain"},
		{MachineID: "qa-machine", SessionID: "qa-finance-1", Space: "qa-finance"},
		{MachineID: "qa-machine", SessionID: "qa-health-1", Space: SpaceHealth}})
	return s, f, st
}

// A text on its way out (#13246; scenes 3, 4, 5 and 7) carries its own tags and its track's default categories, never
// its conversation's accumulated ones: a general text from a conversation once tagged finance goes as general. A
// track sensitive by default, or one the registry does not know, asks no model; a text that cannot be tagged takes
// its conversation's categories when the conversation was tagged, else it is sensitive.
func TestExitUse(t *testing.T) {
	ctx := context.Background()
	s, f, st := infoUseService(t)
	if !s.infoTagConfigured(ctx) {
		t.Fatal("info.tag has no model")
	}
	if got := s.exitPolicy(ctx, "qa: a step count", "qa-health-1", SpaceHealth); got != strictPolicy || len(f.infoTagAsked) != 0 {
		t.Errorf("health = %+v after %d calls, want strict without a call", got, len(f.infoTagAsked))
	}
	if got := s.exitPolicy(ctx, "qa: anything", "qa-x", "qa-unknown"); got != strictPolicy || len(f.infoTagAsked) != 0 {
		t.Errorf("an unknown track = %+v after %d calls, want strict without a call", got, len(f.infoTagAsked))
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.exitPolicy(ctx, "qa: the build passed", "qa-main", ""); got != (TrackPolicy{}) || len(f.infoTagAsked) != 1 {
		t.Errorf("a general text from a conversation tagged finance = %+v (%d calls), want all off", got, len(f.infoTagAsked))
	}
	if f.infoTagAsked[0].User != "qa: the build passed" {
		t.Errorf("tagged %q", f.infoTagAsked[0].User)
	}
	f.mu.Lock()
	f.infoTag = `{"categories":["legal_id"]}`
	f.mu.Unlock()
	if got := s.exitPolicy(ctx, "qa: a placeholder passport number", "qa-main", ""); got != strictPolicy {
		t.Errorf("a sensitive text = %+v, want strict", got)
	}
	if got := s.exitPolicy(ctx, "qa: the build passed", "qa-main", ""); got != (TrackPolicy{}) {
		t.Errorf("the same general text again (cached) = %+v, want all off", got)
	}
	if got := s.exitPolicy(ctx, "  ", "qa-plain-1", "qa-plain"); got != (TrackPolicy{}) {
		t.Errorf("an empty text = %+v, want its track's (all off)", got)
	}

	// Tagging fails: the conversation's categories, when it was tagged.
	f.mu.Lock()
	f.infoTagFails = true
	f.mu.Unlock()
	if got := s.exitPolicy(ctx, "qa: new text 1", "qa-plain-1", "qa-plain"); got != strictPolicy {
		t.Errorf("failed, conversation never tagged = %+v, want strict", got)
	}
	if err := st.AddInfoCategories(ctx, db.InfoCategoryConversation, "qa-plain-1", []string{infoNoneTagged}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.exitPolicy(ctx, "qa: new text 2", "qa-plain-1", "qa-plain"); got != (TrackPolicy{}) {
		t.Errorf("failed, conversation tagged with none = %+v, want all off", got)
	}
	if got := s.exitPolicy(ctx, "qa: new text 3", "qa-main", ""); got != (TrackPolicy{}) {
		t.Errorf("failed, main-space conversation tagged finance = %+v, want all off (general)", got)
	}
	s.infoTagTurnEnded(ctx, "qa-main")
	if got := s.exitPolicy(ctx, "qa: new text 3b", "qa-main", ""); got != (TrackPolicy{}) {
		t.Errorf("failed, main-space conversation with a turn waiting = %+v, want all off (general)", got)
	}
	s.infoTagTurnEnded(ctx, "qa-plain-1")
	if got := s.exitPolicy(ctx, "qa: new text 4", "qa-plain-1", "qa-plain"); got != strictPolicy {
		t.Errorf("failed, a turn waiting to be tagged = %+v, want strict", got)
	}
	if got := s.exitPolicy(ctx, "qa: new text 5", "", ""); got != strictPolicy {
		t.Errorf("failed, no conversation = %+v, want strict", got)
	}
}

// A conversation as a whole (scenes 6 and 2) carries its accumulated categories: its track's floor and its tags; while
// a turn of it waits to be tagged they are not known.
func TestConversationUse(t *testing.T) {
	ctx := context.Background()
	s, _, _ := infoUseService(t)
	for sid, want := range map[string]TrackPolicy{"qa-main": {}, "qa-plain-1": {}, "qa-finance-1": strictPolicy, "qa-health-1": strictPolicy} {
		if got := s.conversationPolicy(ctx, sid); got != want {
			t.Errorf("%s = %+v, want %+v", sid, got, want)
		}
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryEmotion}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.conversationPolicy(ctx, "qa-main"); got != strictPolicy {
		t.Errorf("a conversation tagged emotion = %+v, want strict", got)
	}
	if !s.infoTagConfigured(ctx) {
		t.Fatal("info.tag has no model")
	}
	s.infoTagTurnEnded(ctx, "qa-plain-1")
	if got := s.conversationUse(ctx, "qa-plain-1"); got.Known {
		t.Errorf("a turn waiting to be tagged: %+v, want not known", got)
	}
	s.infoTagWaiting.set("qa-plain-1", false)
	if got := s.conversationPolicy(ctx, "qa-plain-1"); got != (TrackPolicy{}) {
		t.Errorf("tagged since = %+v, want all off", got)
	}
}

// Outside the health space a text is judged by what it says (#13246): a notify_user from the main space whose words
// are tagged sensitive reaches the lock screen as news without its words (a notice that names no space), and an
// agent_send whose words are tagged sensitive is confirmed on the phone with a card that names no space; general words
// go out as before.
func TestSensitiveTextOutsideHealth(t *testing.T) {
	f := healthHub()
	f.infoTagModel = true
	f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "code-0000-0000", Cwd: "/home/me/src/app", Agent: "claude",
		Status: proto.StatusIdle, Title: "app"})
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	if !s.infoTagConfigured(context.Background()) {
		t.Fatal("info.tag has no model")
	}
	if _, isErr := spaceTool(t, s, "work-0000-0000", "notify_user", `{"title":"qa build","body":"all green"}`); isErr {
		t.Fatal("general notify_user")
	}
	f.mu.Lock()
	f.infoTag = `{"categories":["finance"]}`
	f.mu.Unlock()
	if _, isErr := spaceTool(t, s, "work-0000-0000", "notify_user", `{"title":"qa invoice","body":"qa amount 100"}`); isErr {
		t.Fatal("sensitive notify_user")
	}
	l := s.lang(context.Background())
	p := f.pushed()
	if len(p) != 2 || p[0].Title != "qa build" || p[0].Body != "all green" ||
		p[1].Title != i18n.T(l, "assistant-health.sensitive_notice_title") || p[1].Body != i18n.T(l, "assistant-health.sensitive_notice_body") {
		t.Fatalf("pushes: %+v", p)
	}
	// Words the owner just confirmed on the phone (an Inbox suggestion's instruction) are not asked about again.
	if ok, _, err := s.confirmOutbound(ownerConfirmed(context.Background()), "server", "work-0000-0000", "agent_start", "qa invoice total",
		"qa", nil); err != nil || !ok {
		t.Fatalf("owner-confirmed words: %v %v", ok, err)
	}
	answerOutboundIn(t, s, "work-0000-0000", "agent_send", "qa invoice total", "/home/me/.persona/assistant", false)
	out, isErr := spaceTool(t, s, "work-0000-0000", "agent_send", `{"session_id":"code-0000-0000","text":"qa invoice total"}`)
	if isErr || !strings.Contains(out, "declined") || len(f.agentSent) != 0 {
		t.Fatalf("declined agent_send: %s, sent %v", out, f.agentSent)
	}
}

// A connector call says where it comes from (#13246): the conversation's track ("main" for the main track), its
// kind, and its categories as a whole; none for a conversation the manager cannot place.
func TestConnectorCallContext(t *testing.T) {
	ctx := context.Background()
	s, _, _ := infoUseService(t)
	main := s.connectorCallContext(ctx, "qa-main")
	if main.Track != "main" || main.TrackKind != "" || main.Categories == nil || len(main.Categories) != 0 {
		t.Errorf("main: %+v", main)
	}
	fin := s.connectorCallContext(ctx, "qa-finance-1")
	if fin.Track != "qa-finance" || fin.TrackKind != trackKindSpace || len(fin.Categories) != 1 || fin.Categories[0] != categoryFinance {
		t.Errorf("finance track: %+v", fin)
	}
	if got := s.connectorCallContext(ctx, "qa-nowhere"); got.Track != "" || got.Categories != nil {
		t.Errorf("a conversation the manager cannot place: %+v", got)
	}
}
