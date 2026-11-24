package conversationlog

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestSegmentProjectionKeepsLegacyIdentityAndPhysicalLineOffsets(t *testing.T) {
	end := 100 // Only two normalized items, but one hundred physical lines.
	first := Segment{ID: "one", Ordinal: 1, FrozenEnd: &end}
	second := Segment{ID: "two", Ordinal: 2, Offset: end}
	physical := proto.Item{ID: "line1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "same words", SourceFingerprint: "record"}
	legacy, ok, err := first.Project(physical, "root", "chat", "h1")
	if err != nil || !ok || legacy.ID != physical.ID || legacy.Seq != 1 || legacy.SourceFingerprint != "record" {
		t.Fatalf("changed the first segment: %+v %v", legacy, err)
	}
	next, ok, err := second.Project(physical, "root", "chat", "h1")
	if err != nil || !ok || next.ID == legacy.ID || next.Seq != 101 || !proto.PositionalItemID(next.ID) {
		t.Fatalf("overlapping segment identity: %+v %v", next, err)
	}
	segment, source, ok := proto.ParseSegmentItemID(next.ID)
	if !ok || segment != "two" || source != physical.ID {
		t.Fatalf("source reference %q %q", segment, source)
	}
	// Filling a missing earlier physical line changes no later cursor.
	physical.Seq = 50
	if _, ok, err := first.Project(physical, "root", "chat", "h1"); err != nil || !ok {
		t.Fatal(err)
	}
	if next, _, _ := second.Project(proto.Item{ID: "later", Seq: 1}, "root", "chat", "h1"); next.Seq != 101 {
		t.Fatal(next.Seq)
	}
	for _, kind := range []string{"title", "wake", "queued"} {
		if _, ok, err := first.Project(proto.Item{ID: kind, Seq: 101, Wake: kind == "wake", Queued: kind == "queued"}, "root", "chat", "h1"); err != nil || ok {
			t.Fatalf("retired tail projected: %s %v", kind, err)
		}
	}
	from, through, err := second.Span(0, 0)
	if err != nil || from != 100 || through != 100 {
		t.Fatalf("missing empty boundary bridge: %d %d %v", from, through, err)
	}
}

// A source answers a cursor past its end (it renumbered its lines under a new history) with from > through: that
// is the empty span at its end, so the batch still reaches its reader, never an error.
func TestSegmentSpanPastTheSourceEndIsEmptyAtTheEnd(t *testing.T) {
	end := 100
	retired := Segment{ID: "one", Ordinal: 1, FrozenEnd: &end}
	active := Segment{ID: "two", Ordinal: 2, Offset: end}
	if from, through, err := active.Span(9, 4); err != nil || from != 104 || through != 104 {
		t.Fatalf("cursor past the end: %d %d %v", from, through, err)
	}
	if from, through, err := retired.Span(120, 90); err != nil || from != 90 || through != 90 {
		t.Fatalf("cursor past a retired source's end: %d %d %v", from, through, err)
	}
	for _, span := range [][2]int{{-1, 4}, {4, -1}} {
		if _, _, err := active.Span(span[0], span[1]); err == nil {
			t.Fatalf("negative span %v accepted", span)
		}
	}
}

func TestSegmentProjectionRewritesReferencesWithoutMutatingSource(t *testing.T) {
	s := Segment{ID: "second", Ordinal: 2, Offset: 100}
	it := proto.Item{ID: "tool", Seq: 2, Stored: &proto.StoredItem{MachineID: "physical", SessionID: "private", History: "source", ItemID: "tool"},
		Fold: &proto.Fold{ThroughSeq: 5, ThroughID: "last", LatestThinking: &proto.Item{ID: "thought", Seq: 3}}}
	before, _ := json.Marshal(it)
	got, ok, err := s.Project(it, "logical-machine", "logical-chat", "logical-history")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Stored.MachineID != "logical-machine" || got.Stored.SessionID != "logical-chat" || got.Stored.History != "logical-history" || got.Stored.ItemID != got.ID {
		t.Fatalf("physical reference escaped %+v", got.Stored)
	}
	if got.Fold.ThroughSeq != 105 || got.Fold.ThroughID != s.ItemID("last") || got.Fold.LatestThinking.Seq != 103 {
		t.Fatalf("fold is not projected %+v", got.Fold)
	}
	after, _ := json.Marshal(it)
	if string(before) != string(after) {
		t.Fatal("mutated runtime-owned pointers")
	}
}

func TestSegmentProjectionRejectsInt32Overflow(t *testing.T) {
	s := Segment{ID: "boundary", Ordinal: 2, Offset: MaxLogicalSeq - 1}
	if seq, err := s.Seq(1); err != nil || seq != MaxLogicalSeq {
		t.Fatalf("last safe cursor %d %v", seq, err)
	}
	if _, err := s.Seq(2); !errors.Is(err, ErrSequenceOverflow) {
		t.Fatalf("overflow accepted: %v", err)
	}
	if _, err := s.Seq(-1); err == nil {
		t.Fatal("negative cursor accepted")
	}
}
