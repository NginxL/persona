package db

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// Registry writes (#13472): a track or kind is added once, and changed only against the revision it was read at.
func TestTrackAndKindWrites(t *testing.T) {
	d, ctx := open(t), context.Background()
	tr := Track{Slug: "qa-finance", Kind: "space", Title: "QA finance", Description: "A test space", DefaultCategories: []string{"finance"},
		Template: "space", DirName: "tracks/qa-finance"}
	if err := d.InsertTrack(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertTrack(ctx, tr); !errors.Is(err, ErrTrackExists) {
		t.Fatalf("a second insert: %v", err)
	}
	rows, err := d.Tracks(ctx)
	if err != nil || len(rows) != 1 || rows[0].Revision != 1 || !slices.Equal(rows[0].DefaultCategories, []string{"finance"}) ||
		string(rows[0].ConfigOverride) != "{}" || rows[0].ArchivedAt != nil {
		t.Fatalf("tracks: %+v %v", rows, err)
	}
	now := time.Now()
	tr.Title, tr.ConfigOverride, tr.ArchivedAt = "Renamed", []byte(`{"capture_target": false}`), &now
	if next, err := d.UpdateTrack(ctx, tr, 1); err != nil || next != 2 {
		t.Fatalf("update: %d %v", next, err)
	}
	if _, err := d.UpdateTrack(ctx, tr, 1); !errors.Is(err, ErrTrackRevision) {
		t.Fatalf("a stale update: %v", err)
	}
	if _, err := d.UpdateTrack(ctx, Track{Slug: "qa-none"}, 1); !errors.Is(err, ErrTrackNotFound) {
		t.Fatalf("an unknown track: %v", err)
	}
	rows, _ = d.Tracks(ctx)
	if rows[0].Title != "Renamed" || rows[0].ArchivedAt == nil || rows[0].Revision != 2 || string(rows[0].ConfigOverride) != `{"capture_target": false}` {
		t.Fatalf("after the update: %+v", rows[0])
	}

	k := TrackKind{Slug: "qa-kind", Title: "QA kind", Config: []byte(`{"schema": 1}`)}
	if err := d.InsertTrackKind(ctx, k); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertTrackKind(ctx, k); !errors.Is(err, ErrTrackExists) {
		t.Fatalf("a second kind insert: %v", err)
	}
	k.Config = []byte(`{"schema": 1, "context": "none"}`)
	if next, err := d.UpdateTrackKind(ctx, k, 1); err != nil || next != 2 {
		t.Fatalf("kind update: %d %v", next, err)
	}
	if _, err := d.UpdateTrackKind(ctx, k, 1); !errors.Is(err, ErrTrackRevision) {
		t.Fatalf("a stale kind update: %v", err)
	}
	if _, err := d.UpdateTrackKind(ctx, TrackKind{Slug: "qa-none", Config: []byte(`{}`)}, 1); !errors.Is(err, ErrTrackNotFound) {
		t.Fatalf("an unknown kind: %v", err)
	}
	kinds, err := d.TrackKinds(ctx)
	if err != nil || len(kinds) != 1 || kinds[0].Revision != 2 || string(kinds[0].Config) != `{"schema": 1, "context": "none"}` {
		t.Fatalf("kinds: %+v %v", kinds, err)
	}
}
