package transcript

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// SupervisionRoundVisibility is one input round's user-visibility disposition.
// Silent is true only for a supervision round with a successful, paired silent
// supervision_finish and no explicit user promotion. End is display metadata for
// seen_at comparison; it never participates in the classification.
type SupervisionRoundVisibility struct {
	StartSeq int
	End      time.Time
	Silent   bool
	Closed   bool // the runtime closed this round; a trailing input, reply or tool call means still live
}

// SupervisionRoundVisibilities splits items at genuine input boundaries and
// applies the same paired tool/result rules as FoldWork. A partial prefix before
// the first input is ignored: a paged reader must fetch the preceding page
// rather than guessing which round it belongs to.
func SupervisionRoundVisibilities(items []proto.Item) []SupervisionRoundVisibility {
	var starts []int
	for i := range items {
		if !supervisionRoundInput(items, i) {
			continue
		}
		starts = append(starts, i)
	}
	out := make([]SupervisionRoundVisibility, 0, len(starts))
	for i, start := range starts {
		end := len(items)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		round := items[start:end]
		v := SupervisionRoundVisibility{StartSeq: items[start].Seq, Silent: CompletedSilentSupervisionRound(round), Closed: supervisionRoundClosed(round)}
		for _, it := range round {
			if it.TS.After(v.End) {
				v.End = it.TS
			}
			if it.Fold != nil && it.Fold.Until.After(v.End) {
				v.End = it.Fold.Until
			}
		}
		out = append(out, v)
	}
	return out
}

func supervisionRoundInput(items []proto.Item, i int) bool {
	it := items[i]
	if it.Role != "user" || it.ToolUseID != "" {
		return false
	}
	if it.Kind == proto.KindImage {
		return i == 0 || it.Seq != items[i-1].Seq
	}
	return it.Kind == proto.KindText || it.Wake
}

// supervisionRoundClosed follows the runtime's own end record, not a timestamp
// or a finished-looking tool receipt. Anything written after that record belongs
// to a later, still-open stretch of work.
func supervisionRoundClosed(round []proto.Item) bool {
	for i := len(round) - 1; i >= 0; i-- {
		it := round[i]
		switch {
		case it.Kind == "turn_duration":
			return true
		case it.Kind == proto.KindNotice && it.Text == InterruptedNotice:
			return true
		case it.Kind == proto.KindText, it.Kind == proto.KindToolUse, it.Kind == proto.KindToolResult, it.Kind == proto.KindThinking,
			it.Kind == proto.KindImage, it.Role == "user", it.Wake, it.Task != nil, it.Injected != nil:
			return false
		}
	}
	return false
}
