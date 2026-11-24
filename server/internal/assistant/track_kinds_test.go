package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// trackKindMemStore is a trackRegistryStore that also keeps the user's kinds and takes registry writes.
type trackKindMemStore struct {
	*trackRegistryStore
	kmu   sync.Mutex
	kinds []db.TrackKind
}

func (st *trackKindMemStore) TrackKinds(context.Context) ([]db.TrackKind, error) {
	st.kmu.Lock()
	defer st.kmu.Unlock()
	return slices.Clone(st.kinds), nil
}

func (st *trackKindMemStore) InsertTrack(_ context.Context, t db.Track) error {
	st.kmu.Lock()
	defer st.kmu.Unlock()
	for _, r := range st.rows {
		if r.Slug == t.Slug {
			return db.ErrTrackExists
		}
	}
	t.Revision = 1
	st.rows = append(st.rows, t)
	return nil
}

func (st *trackKindMemStore) UpdateTrack(_ context.Context, t db.Track, revision int64) (int64, error) {
	st.kmu.Lock()
	defer st.kmu.Unlock()
	for i, r := range st.rows {
		if r.Slug != t.Slug {
			continue
		}
		if r.Revision != revision {
			return 0, db.ErrTrackRevision
		}
		r.Title, r.Icon, r.Description, r.DefaultCategories, r.Template = t.Title, t.Icon, t.Description, t.DefaultCategories, t.Template
		r.ConfigOverride, r.ArchivedAt, r.Revision = t.ConfigOverride, t.ArchivedAt, r.Revision+1
		st.rows[i] = r
		return r.Revision, nil
	}
	return 0, db.ErrTrackNotFound
}

func (st *trackKindMemStore) InsertTrackKind(_ context.Context, k db.TrackKind) error {
	st.kmu.Lock()
	defer st.kmu.Unlock()
	for _, r := range st.kinds {
		if r.Slug == k.Slug {
			return db.ErrTrackExists
		}
	}
	k.Revision = 1
	st.kinds = append(st.kinds, k)
	return nil
}

func (st *trackKindMemStore) UpdateTrackKind(_ context.Context, k db.TrackKind, revision int64) (int64, error) {
	st.kmu.Lock()
	defer st.kmu.Unlock()
	for i, r := range st.kinds {
		if r.Slug != k.Slug {
			continue
		}
		if r.Revision != revision {
			return 0, db.ErrTrackRevision
		}
		r.Title, r.Config, r.Revision = k.Title, k.Config, r.Revision+1
		st.kinds[i] = r
		return r.Revision, nil
	}
	return 0, db.ErrTrackNotFound
}

// validKind is a valid kind config to change field by field.
func validKind() assistantwire.TrackKindConfig { return builtinKindConfigs()[trackKindSpace] }

// Kind configs (#13472): the built-in ones are valid; every field takes only its options; a newer schema is not
// enabled; a field this manager does not know is refused.
func TestKindConfigValidation(t *testing.T) {
	for slug, c := range builtinKindConfigs() {
		got, err := normalKindConfig(c)
		if err != nil || !reflect.DeepEqual(got, c) {
			t.Errorf("built-in %s: %v (%+v)", slug, err, got)
		}
	}
	bad := map[string]func(c *assistantwire.TrackKindConfig){
		"template":     func(c *assistantwire.TrackKindConfig) { c.Template = "odd" },
		"context":      func(c *assistantwire.TrackKindConfig) { c.Context = "everything" },
		"supervision":  func(c *assistantwire.TrackKindConfig) { c.Supervision = "boss" },
		"assembly":     func(c *assistantwire.TrackKindConfig) { c.Assembly = "" },
		"push_cap":     func(c *assistantwire.TrackKindConfig) { c.Attention.PushCap = "P9" },
		"deep_default": func(c *assistantwire.TrackKindConfig) { c.Attention.DeepDefault = "deeper" },
		"enter_mode":   func(c *assistantwire.TrackKindConfig) { c.Attention.EnterMode = "always" },
		"channel":      func(c *assistantwire.TrackKindConfig) { c.Channels = []string{"broadcast"} },
		"category":     func(c *assistantwire.TrackKindConfig) { c.DefaultCategories = []string{"passwords"} },
		"tag_focus":    func(c *assistantwire.TrackKindConfig) { c.TagFocus = []string{"someday"} },
		"icon":         func(c *assistantwire.TrackKindConfig) { c.Sidebar.Icon = "heart.fill" },
		"schema":       func(c *assistantwire.TrackKindConfig) { c.Schema = assistantwire.TrackKindSchema + 1 },
		"cron": func(c *assistantwire.TrackKindConfig) {
			c.Rhythms = []assistantwire.TrackRhythm{{Title: "Weekly", Cron: "every monday", Prompt: "Review", TopicPolicy: "new"}}
		},
		"tz": func(c *assistantwire.TrackKindConfig) {
			c.Rhythms = []assistantwire.TrackRhythm{{Title: "Weekly", Cron: "0 9 * * 1", TZ: "Mars/Olympus", Prompt: "Review", TopicPolicy: "new"}}
		},
		"topic_policy": func(c *assistantwire.TrackKindConfig) {
			c.Rhythms = []assistantwire.TrackRhythm{{Title: "Weekly", Cron: "0 9 * * 1", Prompt: "Review", TopicPolicy: "sometimes"}}
		},
	}
	for name, change := range bad {
		c := validKind()
		change(&c)
		if _, err := normalKindConfig(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := validKind()
	c.Rhythms = []assistantwire.TrackRhythm{{Title: "Weekly", Cron: "0 9 * * 1", TZ: "UTC", Prompt: "Review the week", TopicPolicy: "continue", Enabled: true}}
	c.DefaultCategories = []string{"finance", "finance"}
	got, err := normalKindConfig(c)
	if err != nil || !slices.Equal(got.DefaultCategories, []string{"finance"}) {
		t.Errorf("a valid rhythm and categories: %v %+v", err, got.DefaultCategories)
	}
	if _, err := parseKindConfig([]byte(`{"schema": 2, "whatever": true}`)); !errors.Is(err, errKindSchema) {
		t.Errorf("a newer schema: %v, want errKindSchema", err)
	}
	raw, _ := json.Marshal(validKind())
	withExtra := strings.Replace(string(raw), `{`, `{"later_field": 1, `, 1)
	if _, err := parseKindConfig([]byte(withExtra)); err == nil {
		t.Error("a field this manager does not know: accepted")
	}
	if got, err := parseKindConfig(raw); err != nil || !reflect.DeepEqual(got, validKind()) {
		t.Errorf("a stored config reads back: %v", err)
	}
	if w := kindWarnings(builtinKindConfigs()[trackKindOrchestration]); !slices.Equal(w, []string{trackWarnSupervisionLeaders}) {
		t.Errorf("leaders' supervision warns: %v", w)
	}
}

// Usage (acceptance 24): a kind or a track only tightens how information is used, never loosens it; "*" covers the
// strictest category.
func TestKindUsageOnlyTightens(t *testing.T) {
	for _, tc := range []struct {
		scene, category, rule string
		ok                    bool
	}{
		{"elsewhere", "finance", "deny", true},
		{"elsewhere", "finance", "minimal", true},
		{"elsewhere", "finance", "relevant", false},
		{"elsewhere", "*", "relevant", false},
		{"elsewhere", "*", "redact", true},
		{"source_track", "*", "allow", true},
		{"source_track", "health", "confirm", true},
		{"lock_screen", "emotion", "allow", false},
		{"lock_screen", "emotion", "redact", true},
		{"others", "legal_id", "redact", false},
		{"others", "legal_id", "confirm", true},
		{"kb_write", "*", "confirm", false},
		{"kb_write", "*", "deny", true},
		{"extension", "relationships", "deny", true},
		{"nowhere", "finance", "deny", false},
		{"elsewhere", "passwords", "deny", false},
		{"elsewhere", "finance", "sometimes", false},
	} {
		err := tightensUsage(assistantwire.TrackUsageRule{Scene: tc.scene, Category: tc.category, Rule: tc.rule})
		if (err == nil) != tc.ok {
			t.Errorf("%s/%s/%s: %v, want ok=%v", tc.scene, tc.category, tc.rule, err, tc.ok)
		}
	}
}

// A track's override (#13472): only the fields a track may set, the sidebar's icon only; its usage comes on top of
// its kind's; attention changes only the fields given.
func TestTrackOverride(t *testing.T) {
	kind := validKind()
	kind.Usage = []assistantwire.TrackUsageRule{{Scene: "kb_write", Category: "*", Rule: "deny"}}
	got, err := effectiveTrackConfig(kind, trackTemplateSpace, []string{"finance"},
		[]byte(`{"attention": {"push_cap": "P2"}, "usage": [{"scene": "lock_screen", "category": "*", "rule": "redact"}], "sidebar": {"icon": "wallet"}, "capture_target": false}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Attention.PushCap != "P2" || got.Attention.DeepDefault != "quick" || len(got.Usage) != 2 || got.Sidebar.Icon != "wallet" ||
		got.Sidebar.Group != "spaces" || got.CaptureTarget || !slices.Equal(got.DefaultCategories, []string{"finance"}) {
		t.Errorf("effective = %+v", got)
	}
	for _, o := range []string{
		`{"template": "health"}`,
		`{"default_categories": ["health"]}`,
		`{"supervision": "leaders"}`,
		`{"sidebar": {"group": "elsewhere"}}`,
		`{"usage": [{"scene": "elsewhere", "category": "*", "rule": "allow"}]}`,
		`{"attention": {"push_cap": "P7"}}`,
		`[1, 2]`,
	} {
		if _, err := effectiveTrackConfig(kind, trackTemplateSpace, nil, []byte(o)); err == nil {
			t.Errorf("override %s: accepted", o)
		}
	}
	if _, err := effectiveTrackConfig(kind, "odd", nil, nil); err == nil {
		t.Error("an unknown template: accepted")
	}
}

// trackKindService is an assistant on a store with a registry it can write.
func trackKindService(t *testing.T) (*Service, *trackKindMemStore) {
	t.Helper()
	st := &trackKindMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}}}
	return newServiceOn(t, newFakeHub(), st), st
}

func decodeTrackJSON[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return v
}

// The tracks and kinds API (#13472): only the user writes; a built-in track or kind is never changed; a change is made
// against its revision; a kind defined in config gives a track the same as a built-in kind would; a kind newer than
// this manager is listed as disabled and its tracks are left out; the health space is unchanged.
func TestTracksAPI(t *testing.T) {
	ctx := context.Background()
	s, st := trackKindService(t)
	healthBefore, _ := s.trackEntryOf(ctx, SpaceHealth)
	policyBefore := s.trackPolicy(ctx, SpaceHealth)

	w := serve(s, http.MethodGet, "/api/v1/assistant/track-kinds", "")
	kinds := decodeTrackJSON[assistantwire.TrackKinds](t, w.Body.Bytes())
	if w.Code != http.StatusOK || len(kinds.Kinds) != 2 || kinds.Kinds[0].Slug != trackKindOrchestration || kinds.Kinds[1].Slug != trackKindSpace ||
		!kinds.Kinds[0].Builtin || !slices.Equal(kinds.Kinds[0].Warnings, []string{trackWarnSupervisionLeaders}) {
		t.Fatalf("built-in kinds: %d %s", w.Code, w.Body)
	}
	w = serve(s, http.MethodGet, "/api/v1/assistant/tracks", "")
	tracks := decodeTrackJSON[assistantwire.Tracks](t, w.Body.Bytes())
	if w.Code != http.StatusOK || len(tracks.Tracks) != 1 || tracks.Tracks[0].Slug != SpaceHealth || !tracks.Tracks[0].Builtin ||
		tracks.Tracks[0].Config.Template != trackTemplateHealth || !slices.Equal(tracks.Tracks[0].Config.DefaultCategories, []string{categoryHealth}) {
		t.Fatalf("tracks: %d %s", w.Code, w.Body)
	}

	// Only the user writes.
	for _, role := range []string{roleExtension, "scoped"} {
		for _, req := range [][3]string{
			{http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-plain","kind":"space","title":"QA plain"}`},
			{http.MethodPut, "/api/v1/assistant/tracks/qa-plain", `{"revision":1,"title":"x"}`},
			{http.MethodPost, "/api/v1/assistant/track-kinds", `{"slug":"qa-kind","title":"QA","config":{}}`},
			{http.MethodPut, "/api/v1/assistant/track-kinds/qa-kind", `{"revision":1}`},
		} {
			if w := serveAs(s, role, req[0], req[1], req[2]); w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: %d, want 403", role, req[0], req[1], w.Code)
			}
		}
	}

	// A track of a built-in kind takes the kind's template and categories.
	w = serve(s, http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-plain","kind":"space","title":"QA plain","icon":"leaf"}`)
	plain := decodeTrackJSON[assistantwire.Track](t, w.Body.Bytes())
	if w.Code != http.StatusCreated || plain.Template != trackTemplateSpace || len(plain.DefaultCategories) != 0 || plain.Revision != 1 ||
		plain.Config.UserInstructions != "space_md" || !plain.Config.CaptureTarget {
		t.Fatalf("create qa-plain: %d %s", w.Code, w.Body)
	}
	if e, _ := s.trackEntryOf(ctx, "qa-plain"); e.DirName != "tracks/qa-plain" {
		t.Errorf("qa-plain's directory = %q", e.DirName)
	}
	for _, body := range []string{
		`{"slug":"qa-plain","kind":"space","title":"Again"}`,
	} {
		if w := serve(s, http.MethodPost, "/api/v1/assistant/tracks", body); w.Code != http.StatusConflict {
			t.Errorf("%s: %d, want 409", body, w.Code)
		}
	}
	for _, body := range []string{
		`{"slug":"health","kind":"space","title":"Health again"}`,
		`{"slug":"Bad Slug","kind":"space","title":"x"}`,
		`{"slug":"qa-x","kind":"nowhere","title":"x"}`,
		`{"slug":"qa-x","kind":"space","title":""}`,
		`{"slug":"qa-x","kind":"space","title":"x","icon":"heart.fill"}`,
		`{"slug":"qa-x","kind":"space","title":"x","default_categories":["passwords"]}`,
		`{"slug":"qa-x","kind":"space","title":"x","config_override":{"supervision":"leaders"}}`,
		`{"slug":"qa-x","kind":"space","title":"x","config_override":{"usage":[{"scene":"elsewhere","category":"*","rule":"allow"}]}}`,
		`{"slug":"qa-x","kind":"space","title":"x","later":true}`,
	} {
		if w := serve(s, http.MethodPost, "/api/v1/assistant/tracks", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}

	// Changes go against the revision; the built-in track is never changed.
	if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/health", `{"revision":1,"title":"Mine"}`); w.Code != http.StatusForbidden {
		t.Errorf("change health: %d, want 403", w.Code)
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-none", `{"revision":1,"title":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("change an unknown track: %d, want 404", w.Code)
	}
	w = serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain", `{"revision":1,"title":"QA plain renamed","archived":true}`)
	plain = decodeTrackJSON[assistantwire.Track](t, w.Body.Bytes())
	if w.Code != http.StatusOK || plain.Title != "QA plain renamed" || !plain.Archived || plain.Revision != 2 {
		t.Fatalf("change qa-plain: %d %s", w.Code, w.Body)
	}
	if s.trackKnown(ctx, "qa-plain") {
		t.Error("an archived track is no longer one to name")
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain", `{"revision":1,"title":"Stale"}`); w.Code != http.StatusConflict {
		t.Errorf("a stale change: %d, want 409", w.Code)
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain", `{"revision":2,"archived":false}`); w.Code != http.StatusOK || !s.trackKnown(ctx, "qa-plain") {
		t.Errorf("unarchive: %d %s", w.Code, w.Body)
	}

	// A kind in config: a finance space with a weekly rhythm and a stricter lock screen.
	cfg := validKind()
	cfg.DefaultCategories = []string{categoryFinance}
	cfg.Rhythms = []assistantwire.TrackRhythm{{Title: "Weekly review", Cron: "0 9 * * 1", Prompt: "Review the week's spending", TopicPolicy: "continue", Enabled: true}}
	cfg.Usage = []assistantwire.TrackUsageRule{{Scene: "lock_screen", Category: "*", Rule: "redact"}}
	body, _ := json.Marshal(assistantwire.TrackKindCreate{Slug: "qa-kind", Title: "QA finance kind", Config: cfg})
	if w := serve(s, http.MethodPost, "/api/v1/assistant/track-kinds", string(body)); w.Code != http.StatusCreated {
		t.Fatalf("create qa-kind: %d %s", w.Code, w.Body)
	}
	for _, bad := range []assistantwire.TrackKindCreate{
		{Slug: "space", Title: "Mine", Config: validKind()},
		{Slug: "qa-kind", Title: "Again", Config: validKind()},
		{Slug: "qa-loose", Title: "Loose", Config: func() assistantwire.TrackKindConfig {
			c := validKind()
			c.Usage = []assistantwire.TrackUsageRule{{Scene: "kb_write", Category: "finance", Rule: "allow"}}
			return c
		}()},
	} {
		b, _ := json.Marshal(bad)
		if w := serve(s, http.MethodPost, "/api/v1/assistant/track-kinds", string(b)); w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Errorf("kind %s: %d, want 400 or 409", bad.Slug, w.Code)
		}
	}
	w = serve(s, http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-finance","kind":"qa-kind","title":"QA finance"}`)
	fin := decodeTrackJSON[assistantwire.Track](t, w.Body.Bytes())
	if w.Code != http.StatusCreated || !slices.Equal(fin.DefaultCategories, []string{categoryFinance}) || len(fin.Config.Rhythms) != 1 ||
		fin.Template != trackTemplateSpace {
		t.Fatalf("create qa-finance: %d %s", w.Code, w.Body)
	}
	if s.trackPolicy(ctx, "qa-finance") != strictPolicy || s.trackPolicy(ctx, "qa-plain") != (TrackPolicy{}) {
		t.Error("a track's policy follows its categories, whatever its kind")
	}

	// Kind changes go against the revision; a built-in kind is never changed; a change may not leave a track invalid.
	if w := serve(s, http.MethodPut, "/api/v1/assistant/track-kinds/space", `{"revision":1,"title":"Mine"}`); w.Code != http.StatusForbidden {
		t.Errorf("change a built-in kind: %d, want 403", w.Code)
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/track-kinds/qa-kind", `{"revision":2,"title":"Stale"}`); w.Code != http.StatusConflict {
		t.Errorf("a stale kind change: %d, want 409", w.Code)
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/track-kinds/qa-kind", `{"revision":1,"title":"QA finance kind 2"}`); w.Code != http.StatusOK {
		t.Errorf("rename qa-kind: %d %s", w.Code, w.Body)
	}

	// A kind whose config is newer than this manager is disabled; its tracks are left out (and strict).
	st.kmu.Lock()
	st.kinds = append(st.kinds, db.TrackKind{Slug: "qa-later", Title: "Later", Config: []byte(`{"schema": 2, "future": true}`), Revision: 1})
	st.rows = append(st.rows, db.Track{Slug: "qa-later-track", Kind: "qa-later", Template: trackTemplateSpace, Revision: 1})
	st.kmu.Unlock()
	s.tracksChanged()
	w = serve(s, http.MethodGet, "/api/v1/assistant/track-kinds", "")
	kinds = decodeTrackJSON[assistantwire.TrackKinds](t, w.Body.Bytes())
	if k := kinds.Kinds[len(kinds.Kinds)-1]; k.Slug != "qa-later" || k.Disabled != kindDisabledSchema {
		t.Errorf("the newer kind: %+v", k)
	}
	if s.trackKnown(ctx, "qa-later-track") || s.trackPolicy(ctx, "qa-later-track") != strictPolicy {
		t.Error("a track of a disabled kind is left out, and strict")
	}
	if w := serve(s, http.MethodPost, "/api/v1/assistant/tracks", `{"slug":"qa-y","kind":"qa-later","title":"y"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a track of a disabled kind: %d, want 400", w.Code)
	}

	// The health space is unchanged.
	healthAfter, _ := s.trackEntryOf(ctx, SpaceHealth)
	if !reflect.DeepEqual(healthBefore, healthAfter) || s.trackPolicy(ctx, SpaceHealth) != policyBefore || policyBefore != strictPolicy {
		t.Errorf("health changed: %+v -> %+v", healthBefore, healthAfter)
	}
}
