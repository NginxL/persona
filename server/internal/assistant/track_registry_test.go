package assistant

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
)

// trackRegistryStore is a memStore with a registry (assistant_tracks) of its own.
type trackRegistryStore struct {
	*memStore
	rows []db.Track
}

func (st *trackRegistryStore) Tracks(context.Context) ([]db.Track, error) { return st.rows, nil }

// The registry and the policy (#13104): the main space turns nothing on, the health space everything, and a slug the
// registry does not know is as strict as the health space.
func TestTrackPolicy(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub())
	if got := s.trackPolicy(ctx, ""); got != (TrackPolicy{}) {
		t.Errorf("main = %+v, want all off", got)
	}
	if got := s.trackPolicy(ctx, SpaceHealth); got != strictPolicy {
		t.Errorf("health = %+v, want all on", got)
	}
	if got := s.trackPolicy(ctx, "qa-unknown"); got != strictPolicy {
		t.Errorf("an unregistered slug = %+v, want strict", got)
	}
	if !s.trackKnown(ctx, "") || !s.trackKnown(ctx, SpaceHealth) || s.trackKnown(ctx, "qa-unknown") {
		t.Error("trackKnown: want the main and health spaces only")
	}
}

// Registered tracks (#13104, #13246): a track's policy follows its default categories (none: all off; a sensitive
// one: all on), and only the second's directory is denied the other conversations; the built-in row cannot change
// the health space; a directory is data (a new track's defaults to tracks/<slug> whatever its kind); a row the manager
// cannot use (an unknown template, category or kind, a bad slug or directory) is left out, so it stays strict and unknown;
// an archived track is no longer one to name while its conversations keep its policy.
func TestTracksFromRegistry(t *testing.T) {
	ctx := context.Background()
	gone := time.Unix(1_800_000_000, 0)
	st := &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "health", Template: trackTemplateSpace, DirName: "elsewhere", Builtin: true},
		{Slug: "qa-plain", Kind: "space", Template: trackTemplateSpace},
		{Slug: "qa-finance", Kind: trackKindSpace, Template: trackTemplateSpace, DefaultCategories: []string{categoryFinance}, DirName: "kept/finance"},
		{Slug: "qa-odd-kind", Kind: "later-kind", Template: trackTemplateSpace},
		{Slug: "qa-odd-template", Template: "odd"},
		{Slug: "qa-odd-category", Template: trackTemplateSpace, DefaultCategories: []string{"someday"}},
		{Slug: "qa-odd-dir", Template: trackTemplateSpace, DirName: "../out"},
		{Slug: "tracks", Template: trackTemplateSpace},
		{Slug: "qa-gone", Template: trackTemplateSpace, ArchivedAt: &gone},
	}}
	s := newServiceOn(t, newFakeHub(), st)
	if e, ok := s.trackEntryOf(ctx, SpaceHealth); !ok || e.Template != trackTemplateHealth || e.DirName != "health" || !e.Builtin ||
		!slices.Equal(e.DefaultCategories, []string{categoryHealth}) {
		t.Errorf("health = %+v (%v), want the built-in entry", e, ok)
	}
	if e, ok := s.trackEntryOf(ctx, "qa-plain"); !ok || e.DirName != "tracks/qa-plain" {
		t.Errorf("qa-plain = %+v (%v), want the default directory", e, ok)
	}
	if e, ok := s.trackEntryOf(ctx, "qa-finance"); !ok || e.DirName != "kept/finance" || e.Kind != trackKindSpace {
		t.Errorf("qa-finance = %+v (%v), want its own directory and kind", e, ok)
	}
	if e, ok := s.trackEntryOf(ctx, "qa-plain"); !ok || e.Kind != trackKindSpace || e.Config.Template != trackTemplateSpace {
		t.Errorf("qa-plain = %+v (%v), want a space", e, ok)
	}
	for _, slug := range []string{"qa-odd-template", "qa-odd-category", "qa-odd-dir", "qa-odd-kind", "tracks"} {
		if s.trackKnown(ctx, slug) || s.trackPolicy(ctx, slug) != strictPolicy {
			t.Errorf("%s: known or not strict", slug)
		}
	}
	if s.trackKnown(ctx, "qa-gone") || !s.trackKnown(ctx, "qa-plain") {
		t.Error("an archived track is no longer one to name, a live one is")
	}
	if s.trackPolicy(ctx, "qa-plain") != (TrackPolicy{}) || s.trackPolicy(ctx, "qa-gone") != (TrackPolicy{}) ||
		s.trackPolicy(ctx, "qa-finance") != strictPolicy {
		t.Error("a track without default categories has every switch off, one with finance every switch on")
	}
	got := s.trackDenyDirs(ctx)
	want := []denyDir{{compat.StateDir + "/kept/finance", "qa-finance"}}
	if !slices.Equal(got, want) {
		t.Errorf("deny %+v, want %+v", got, want)
	}
	st.rows = st.rows[:1]
	s.tracksChanged()
	if got := s.trackDenyDirs(ctx); got == nil || len(got) != 0 {
		t.Errorf("no tracks but the health space: deny %+v, want an empty list", got)
	}
}
