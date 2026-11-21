// Package conversationlog describes the manager's durable, runtime-neutral assistant history.
package conversationlog

import (
	"encoding/json"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// MaxItemBytes includes all inline content, including tools and attachments.
	MaxItemBytes = 64 * 1024
	PreviewBytes = 4096
)

// Range covers source positions after From through Through, including empty source lines.
type Range struct {
	From    int `json:"from"`
	Through int `json:"through"`
}

// MergeRanges coalesces overlapping and adjacent coverage. A gap is never inferred covered.
func MergeRanges(ranges []Range, add Range) []Range {
	out := append(append([]Range(nil), ranges...), add)
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	merged := out[:0]
	for _, r := range out {
		if r.From < 0 || r.Through < r.From {
			continue
		}
		if len(merged) > 0 && r.From <= merged[len(merged)-1].Through {
			merged[len(merged)-1].Through = max(merged[len(merged)-1].Through, r.Through)
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

func Covers(ranges []Range, from, through int) bool {
	for _, r := range ranges {
		if r.From <= from && r.Through >= through {
			return true
		}
	}
	return false
}

// Item retains a source reference even when its payload is incomplete. The reference
// is the enclosing history's machine/session/generation plus Item.ID, never a local path.
type Item struct {
	Item        proto.Item
	Complete    bool
	SourceBytes int
}

// Bound retains small normalized items verbatim and bounds every other field together.
// It does not mutate input or pretend a runtime's truncated/lazy/folded page is complete.
func Bound(it proto.Item) (Item, error) {
	if it.ID == "" || len(it.ID) > 512 || it.Seq <= 0 || len(it.Role) > 64 || len(it.Kind) > 64 {
		return Item{}, errors.New("invalid conversation item identity")
	}
	raw, err := json.Marshal(it)
	if err != nil {
		return Item{}, err
	}
	complete := it.FullSize == 0 && it.Fold == nil && it.Kind != proto.KindVisualization &&
		(it.Image == nil || (!it.Image.Lazy && it.Image.Data != ""))
	row := Item{Item: it, Complete: complete, SourceBytes: len(raw)}
	if len(raw) <= MaxItemBytes {
		return row, nil
	}
	// An overflow can be in any nested metadata, not only Text or ToolInput.
	// Keep a bounded skeleton and the runtime item reference for on-demand recovery.
	row.Item = proto.Item{ID: it.ID, Seq: it.Seq, TS: it.TS, Role: it.Role, Kind: it.Kind,
		Text: prefix(it.Text, PreviewBytes), ToolName: prefix(it.ToolName, 256),
		ToolUseID: prefix(it.ToolUseID, 512), ClientMsgID: prefix(it.ClientMsgID, 512),
		IsError: it.IsError, Queued: it.Queued, Wake: it.Wake,
		FullSize: max(it.FullSize, len(it.Text))}
	row.Item.From = prefix(it.From, 512)
	if it.Source != nil {
		row.Item.Source = &proto.MessageSource{Kind: prefix(it.Source.Kind, 64),
			ConversationID: prefix(it.Source.ConversationID, 512), Title: prefix(it.Source.Title, 512)}
	}
	if it.Injected != nil {
		row.Item.Injected = &proto.Injected{Kind: prefix(it.Injected.Kind, 64), State: prefix(it.Injected.State, 64),
			Level: prefix(it.Injected.Level, 64), Title: prefix(it.Injected.Title, 512),
			Machine: prefix(it.Injected.Machine, 512), TodoID: prefix(it.Injected.TodoID, 512), Line: prefix(it.Injected.Line, PreviewBytes)}
	}
	if it.Image != nil {
		row.Item.Image = &proto.Image{Mime: prefix(it.Image.Mime, 128), Lazy: true,
			Width: it.Image.Width, Height: it.Image.Height}
	}
	row.Complete = false
	// Escaping can expand one preview byte into six JSON bytes. Bound the
	// serialized skeleton too, so one escaped output cannot block page ingestion.
	for budget := PreviewBytes; ; budget /= 2 {
		raw, err := json.Marshal(row.Item)
		if err != nil {
			return Item{}, err
		}
		if len(raw) <= MaxItemBytes {
			break
		}
		if budget == 0 {
			return Item{}, errors.New("conversation item reference exceeds limit")
		}
		row.Item.Text = prefix(row.Item.Text, budget/2)
		if row.Item.Injected != nil {
			row.Item.Injected.Line = prefix(row.Item.Injected.Line, budget/2)
		}
	}
	return row, nil
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
