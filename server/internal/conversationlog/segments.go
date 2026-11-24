package conversationlog

import (
	"errors"
	"math"

	"github.com/nwverse/persona/server/internal/proto"
)

// Logical cursors must also fit clients whose sequence type is a signed int32.
const MaxLogicalSeq = math.MaxInt32 - 1

var ErrSequenceOverflow = errors.New("conversation sequence exceeds the supported range")

// Segment is a physical source's immutable position in a logical conversation.
// FrozenEnd counts source lines, including lines without normalized items.
type Segment struct {
	Launcher       string `json:"launcher,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	ID             string `json:"id"`
	Ordinal        int    `json:"ordinal"`
	MachineID      string `json:"machine_id"`
	SessionID      string `json:"session_id"`
	Engine         string `json:"engine"`
	SourceHistory  string `json:"source_history"`
	SourceRevision int64  `json:"source_revision"`
	Offset         int    `json:"offset"`
	FrozenEnd      *int   `json:"frozen_end,omitempty"`
	State          string `json:"state"`
	ClosePending   bool   `json:"close_pending,omitempty"`
}

func (s Segment) Seq(source int) (int, error) {
	if source < 0 || s.Offset < 0 || s.Offset > MaxLogicalSeq || source > MaxLogicalSeq-s.Offset {
		return 0, ErrSequenceOverflow
	}
	return s.Offset + source, nil
}

func (s Segment) ItemID(id string) string {
	if s.Ordinal == 1 || id == "" {
		return id
	}
	return proto.SegmentItemID(s.ID, id)
}

// Project copies reference-bearing fields, never mutating a shared runtime item.
// A retired tail stays in physical storage but is outside its frozen projection.
func (s Segment) Project(it proto.Item, machine, session, history string) (proto.Item, bool, error) {
	if s.FrozenEnd != nil && it.Seq > *s.FrozenEnd {
		return proto.Item{}, false, nil
	}
	seq, err := s.Seq(it.Seq)
	if err != nil {
		return proto.Item{}, false, err
	}
	it.ID, it.Seq = s.ItemID(it.ID), seq
	if s.Ordinal > 1 {
		if it.ToolUseID != "" {
			it.ToolUseID = s.ItemID(it.ToolUseID)
		}
		if it.SourceFingerprint != "" {
			it.SourceFingerprint = s.ItemID(it.SourceFingerprint)
		}
	}
	if it.Stored != nil {
		copy := *it.Stored
		copy.MachineID, copy.SessionID, copy.History, copy.ItemID = machine, session, history, it.ID
		it.Stored = &copy
	}
	if it.Fold != nil {
		copy := *it.Fold
		copy.ThroughSeq, err = s.Seq(copy.ThroughSeq)
		if err != nil {
			return proto.Item{}, false, err
		}
		copy.ThroughID = s.ItemID(copy.ThroughID)
		copy.Images = append([]proto.FoldImage(nil), copy.Images...)
		for i := range copy.Images {
			if copy.Images[i].ItemID != "" {
				copy.Images[i].ItemID = s.ItemID(copy.Images[i].ItemID)
			}
			if copy.Images[i].ToolUseID != "" {
				copy.Images[i].ToolUseID = s.ItemID(copy.Images[i].ToolUseID)
			}
		}
		copy.Tasks = append([]proto.FoldTaskSnapshot(nil), copy.Tasks...)
		for i := range copy.Tasks {
			copy.Tasks[i].SourceItemID = s.ItemID(copy.Tasks[i].SourceItemID)
			if copy.Tasks[i].SourceToolUseID != "" {
				copy.Tasks[i].SourceToolUseID = s.ItemID(copy.Tasks[i].SourceToolUseID)
			}
			copy.Tasks[i].SourceSeq, err = s.Seq(copy.Tasks[i].SourceSeq)
			if err != nil {
				return proto.Item{}, false, err
			}
		}
		if copy.LatestThinking != nil {
			projected, ok, e := s.Project(*copy.LatestThinking, machine, session, history)
			if e != nil {
				return proto.Item{}, false, e
			}
			copy.LatestThinking = nil
			if ok {
				copy.LatestThinking = &projected
			}
		}
		it.Fold = &copy
	}
	return it, true, nil
}

// Span includes an empty bridge at a segment boundary. Never advance over a gap
// within a physical source, nor beyond a retired source's frozen line end.
// A source answers a cursor past its end with from > through: the cursor is from
// an earlier numbering of the source (agy renumbers a turn's live steps when its
// export replaces them, under a new history). That is the empty span at the
// source's end. The batch must still reach its reader: its new history is what
// tells the reader to start over, and refusing it would leave the reader on a
// cursor the source may not reach again for many turns.
func (s Segment) Span(from, through int) (int, int, error) {
	if from < 0 || through < 0 {
		return 0, 0, errors.New("invalid source span")
	}
	from = min(from, through)
	if s.FrozenEnd != nil {
		from, through = min(from, *s.FrozenEnd), min(through, *s.FrozenEnd)
	}
	a, err := s.Seq(from)
	if err != nil {
		return 0, 0, err
	}
	b, err := s.Seq(through)
	return a, b, err
}
