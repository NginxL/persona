package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// infoCategoryMemStore is a registry store that keeps information categories as assistant_info_categories does: a
// tagger only adds, and a category the user took away comes back only when the user adds it.
type infoCategoryMemStore struct {
	*trackRegistryStore
	mu      sync.Mutex
	rows    map[string]map[string]db.InfoCategory // ref → category → row
	readErr error
}

func (st *infoCategoryMemStore) AddInfoCategories(_ context.Context, kind, ref string, cats []string, by, model string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rows == nil {
		st.rows = map[string]map[string]db.InfoCategory{}
	}
	if st.rows[kind+"/"+ref] == nil {
		st.rows[kind+"/"+ref] = map[string]db.InfoCategory{}
	}
	for _, c := range cats {
		was, ok := st.rows[kind+"/"+ref][c]
		if ok && !(was.RemovedByUser && by == db.InfoCategoryByUser) {
			continue
		}
		st.rows[kind+"/"+ref][c] = db.InfoCategory{Ref: ref, Category: c, By: by, Model: model}
	}
	return nil
}

func (st *infoCategoryMemStore) RemoveInfoCategories(_ context.Context, kind, ref string, cats []string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rows == nil {
		st.rows = map[string]map[string]db.InfoCategory{}
	}
	if st.rows[kind+"/"+ref] == nil {
		st.rows[kind+"/"+ref] = map[string]db.InfoCategory{}
	}
	for _, c := range cats {
		st.rows[kind+"/"+ref][c] = db.InfoCategory{Ref: ref, Category: c, By: db.InfoCategoryByUser, RemovedByUser: true}
	}
	return nil
}

func (st *infoCategoryMemStore) InfoCategories(_ context.Context, kind string, refs []string) (map[string][]db.InfoCategory, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.readErr != nil {
		return nil, st.readErr
	}
	out := map[string][]db.InfoCategory{}
	for _, ref := range refs {
		for _, c := range st.rows[kind+"/"+ref] {
			out[ref] = append(out[ref], c)
		}
	}
	return out, nil
}

// infoTagModel answers a tagging call as told, keeping what it was asked.
type infoTagModel struct {
	answer string
	err    error
	system []string
	user   []string
}

func (m *infoTagModel) JSON(_ context.Context, system, user string, out any) error {
	m.system, m.user = append(m.system, system), append(m.user, user)
	if m.err != nil {
		return m.err
	}
	return json.Unmarshal([]byte(m.answer), out)
}

// The tagging prompt (#13246), byte for byte, with and without a track's focus; the text goes as the user message
// as it is; an answer keeps only the categories the manager knows, each once, in order.
func TestInfoTagPrompt(t *testing.T) {
	ctx := context.Background()
	m := &infoTagModel{answer: `{"categories":["Finance","someday","emotion","finance"," legal_id "]}`}
	got, err := tagInfoWith(ctx, m, "qa: a placeholder turn about a monthly budget", nil)
	if err != nil || !slices.Equal(got, []string{categoryEmotion, categoryFinance, categoryLegalID}) {
		t.Fatalf("tagged %v, %v", got, err)
	}
	if _, err := tagInfoWith(ctx, m, "qa: another placeholder", []string{categoryHealth, categoryFinance}); err != nil {
		t.Fatal(err)
	}
	golden(t, "info_tag", m.system[0])
	golden(t, "info_tag.focus", m.system[1])
	if m.user[0] != "qa: a placeholder turn about a monthly budget" {
		t.Errorf("user message = %q", m.user[0])
	}
	m.answer = `{"categories":[]}`
	if got, err := tagInfoWith(ctx, m, "qa: general", nil); err != nil || len(got) != 0 {
		t.Errorf("general text = %v, %v", got, err)
	}
	m.err = errors.New("qa: model down")
	if _, err := tagInfoWith(ctx, m, "qa: general", nil); err == nil {
		t.Error("a failed call tags nothing and says so")
	}
}

// A conversation's categories (#13246): its track's default categories, which nobody takes away, and what was tagged
// since; a tagger only adds, the user takes one away for good unless they add it back; a store that cannot be read
// says the categories are not all known.
func TestConversationCategories(t *testing.T) {
	ctx := context.Background()
	st := &infoCategoryMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "qa-finance", Kind: trackKindSpace, Template: trackTemplateSpace, DefaultCategories: []string{categoryFinance}},
		{Slug: "qa-plain", Kind: trackKindSpace, Template: trackTemplateSpace},
	}}}
	s := newServiceOn(t, newFakeHub(), st)
	s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: "qa-machine", SessionID: "qa-main", Space: ""},
		{MachineID: "qa-machine", SessionID: "qa-plain-1", Space: "qa-plain"},
		{MachineID: "qa-machine", SessionID: "qa-finance-1", Space: "qa-finance"},
		{MachineID: "qa-machine", SessionID: "qa-health-1", Space: SpaceHealth}})
	cats := func(sid string) []string {
		t.Helper()
		got, ok := s.conversationCategories(ctx, sid)
		if !ok {
			t.Fatalf("%s: categories not known", sid)
		}
		return got
	}
	for sid, want := range map[string][]string{"qa-main": nil, "qa-plain-1": nil, "qa-finance-1": {categoryFinance},
		"qa-health-1": {categoryHealth}} {
		if got := cats(sid); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", sid, got, want)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.tagConversation(ctx, "qa-plain-1", []string{categoryFinance, "someday"}, infoTaggedByModel, ""))
	must(s.tagConversation(ctx, "qa-health-1", []string{categoryEmotion}, infoTaggedByAgent, ""))
	if got := cats("qa-plain-1"); !slices.Equal(got, []string{categoryFinance}) {
		t.Errorf("tagged plain = %v", got)
	}
	if got := cats("qa-health-1"); !slices.Equal(got, []string{categoryEmotion, categoryHealth}) {
		t.Errorf("tagged health = %v", got)
	}
	// The user takes finance away from the plain conversation and health from the health one: the first stays away
	// whatever the model tags again; the second is the track's floor and stays.
	must(st.RemoveInfoCategories(ctx, db.InfoCategoryConversation, "qa-plain-1", []string{categoryFinance}))
	must(st.RemoveInfoCategories(ctx, db.InfoCategoryConversation, "qa-health-1", []string{categoryHealth}))
	must(s.tagConversation(ctx, "qa-plain-1", []string{categoryFinance}, infoTaggedByModel, ""))
	if got := cats("qa-plain-1"); len(got) != 0 {
		t.Errorf("a category the user took away came back: %v", got)
	}
	if got := cats("qa-health-1"); !slices.Contains(got, categoryHealth) {
		t.Errorf("the health space's floor went away: %v", got)
	}
	must(s.tagConversation(ctx, "qa-plain-1", []string{categoryFinance}, infoTaggedByUser, ""))
	if got := cats("qa-plain-1"); !slices.Equal(got, []string{categoryFinance}) {
		t.Errorf("the user added it back: %v", got)
	}
	st.readErr = errors.New("qa: store down")
	if got, ok := s.conversationCategories(ctx, "qa-finance-1"); ok || !slices.Equal(got, []string{categoryFinance}) {
		t.Errorf("unreadable store = %v (%v), want the floor and not known", got, ok)
	}
}

// The text tagged of a turn: what the user said, then the reply, at most infoTagTurnText runes.
func TestInfoTagTurn(t *testing.T) {
	if got := infoTagTurn("  ask ", " reply  "); got != "ask\n\nreply" {
		t.Errorf("turn = %q", got)
	}
	if got := infoTagTurn("", ""); got != "" {
		t.Errorf("empty turn = %q", got)
	}
	long := infoTagTurn("q", string(make([]rune, 2000)))
	if n := len([]rune(long)); n != infoTagTurnText {
		t.Errorf("long turn has %d runes", n)
	}
}
