package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/prompts"
)

const ShareSourceLimit = 16 << 20
const ShareLogicalLimit = 10000
const shareAdapter = "claude-jsonl.v1"

func shareDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// CaptureShareProjection reads a trusted resolver's file, never a client path.
// It pins the opened identity, then reopens the same path and rehashes the entire
// captured prefix. Appends are harmless; replacement and mixed rewrites fail closed.
func CaptureShareProjection(ctx context.Context, path, incarnation string, previous *devicewire.ShareFrontier) (devicewire.ShareProjection, error) {
	deny := func(reason string) (devicewire.ShareProjection, error) {
		return devicewire.ShareProjection{Reason: reason, Messages: []devicewire.ShareProjectionMessage{}}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return deny("source_incomplete")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return deny("source_unsupported")
	}
	if st.Size() > ShareSourceLimit {
		return deny("source_limit")
	}
	n := st.Size()
	data := make([]byte, int(n))
	if _, err = io.ReadFull(f, data); err != nil {
		return deny("source_incomplete")
	}
	if err = ctx.Err(); err != nil {
		return devicewire.ShareProjection{}, err
	}
	again, err := os.Open(path)
	if err != nil {
		return deny("source_incomplete")
	}
	defer again.Close()
	st2, err := again.Stat()
	if err != nil || !os.SameFile(st, st2) || st2.Size() < n {
		return deny("source_changed")
	}
	h := sha256.New()
	if _, err = io.CopyN(h, again, n); err != nil || hex.EncodeToString(h.Sum(nil)) != shareDigest(data) {
		return deny("source_changed")
	}
	source := shareDigest([]byte(fmt.Sprintf("%s\x00%d", path, inode(st))))
	return ProjectShareBytes(ctx, data, source, incarnation, previous)
}

type shareRecord struct {
	entry      entry
	start, end int64
	id         string
	blocks     []block
	human      bool
}

func shareBlocks(raw json.RawMessage) ([]block, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []block{{Type: "text", Text: text}}, true
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, false
	}
	return blocks, true
}
func shareHuman(e entry) bool {
	if e.IsMeta || e.IsSidechain || e.IsCompactSummary {
		return false
	}
	var origin struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(e.Origin, &origin) == nil && origin.Kind == "human" {
		return true
	}
	return e.Attachment != nil && e.Attachment.Type == "queued_command" && e.Attachment.CommandMode == "prompt" && e.Attachment.Origin != nil && e.Attachment.Origin.Kind == "human"
}

// ProjectShareBytes bypasses the ordinary Item cache and all parser clipping.
// Unknown provenance is omitted, never authorized from role=user or text matching.
func ProjectShareBytes(ctx context.Context, data []byte, source, incarnation string, previous *devicewire.ShareFrontier) (devicewire.ShareProjection, error) {
	first := data
	if end := bytes.IndexByte(data, '\n'); end >= 0 {
		first = data[:end]
	}
	if isCodexLine(first) {
		return projectCodexShare(ctx, data, source, incarnation, previous)
	}
	return projectShareBytes(ctx, data, source, incarnation, previous)
}
func projectShareBytes(ctx context.Context, data []byte, source, incarnation string, previous *devicewire.ShareFrontier) (devicewire.ShareProjection, error) {
	out := devicewire.ShareProjection{Messages: []devicewire.ShareProjectionMessage{}, Omissions: []string{}}
	deny := func(reason string) (devicewire.ShareProjection, error) {
		out.Reason = reason
		out.Messages = []devicewire.ShareProjectionMessage{}
		return out, nil
	}
	if len(data) > ShareSourceLimit {
		return deny("source_limit")
	}
	boundary := bytes.LastIndexByte(data, '\n') + 1
	frontier := devicewire.ShareFrontier{Incarnation: incarnation, Adapter: shareAdapter, Source: source, Boundary: int64(boundary), EOF: int64(len(data)), PrefixHash: shareDigest(data[:boundary]), PartialHash: shareDigest(data[boundary:]), FirstSeen: map[string]int64{}}
	if previous != nil {
		p := previous
		if p.Incarnation != incarnation || p.Adapter != shareAdapter || p.Source != source || p.Boundary < 0 || p.EOF < p.Boundary || p.EOF > int64(len(data)) || p.Boundary > int64(boundary) {
			return deny("source_changed")
		}
		if shareDigest(data[:p.Boundary]) != p.PrefixHash || shareDigest(data[p.Boundary:p.EOF]) != p.PartialHash {
			return deny("source_changed")
		}
		for id, offset := range p.FirstSeen {
			frontier.FirstSeen[id] = offset
		}
	}
	var records []shareRecord
	var tree branchTree
	seen := map[string]string{}
	for start := 0; start < boundary; {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		end := start + bytes.IndexByte(data[start:boundary], '\n') + 1
		raw := bytes.TrimSpace(data[start:end])
		if len(raw) == 0 {
			start = end
			continue
		}
		var e entry
		if json.Unmarshal(raw, &e) != nil {
			return deny("source_incomplete")
		}
		if e.Type == "session_meta" || e.Type == "response_item" || e.Type == "event_msg" || e.Type == "history_mutation" {
			return deny("source_unsupported")
		}
		id := e.UUID
		if id == "" {
			id = fmt.Sprintf("offset:%d", start)
		}
		digest := shareDigest(raw)
		if prior, ok := seen[id]; ok {
			if prior != digest {
				return deny("source_changed")
			}
			start = end
			continue
		}
		seen[id] = digest
		human := shareHuman(e)
		var blocks []block
		var valid bool
		if e.Message != nil {
			blocks, valid = shareBlocks(e.Message.Content)
		} else if e.Attachment != nil && e.Attachment.Type == "queued_command" {
			blocks, valid = shareBlocks(e.Attachment.Prompt)
		} else {
			valid = true
		}
		if !valid {
			return deny("source_incomplete")
		}
		prompt := e.Type == "user" && !e.IsMeta && !e.IsSidechain
		for _, b := range blocks {
			if b.Type == "tool_result" {
				prompt = false
			}
		}
		round := false // a round of the background supervisor (branchTree.add, #7315)
		for _, b := range blocks {
			round = round || prompt && b.Type == "text" && prompts.IsSuperviseRound(b.Text)
		}
		tree.add(treeRec{uuid: id, parent: e.ParentUUID, prompt: prompt, reply: e.Type == "assistant" && !e.IsSidechain, system: e.Type == "system", turnEnd: e.Type == "system" && e.Subtype == "turn_duration", round: round})
		records = append(records, shareRecord{entry: e, start: int64(start), end: int64(end), id: id, blocks: blocks, human: human})
		start = end
		if len(records) > 100000 {
			return deny("source_limit")
		}
	}
	roots := make([]string, 0, len(tree.roots))
	for id := range tree.roots {
		roots = append(roots, id)
	}
	sort.Strings(roots)
	frontier.Branch = shareDigest([]byte(strings.Join(roots, "\x00")))
	if previous != nil && previous.Branch != frontier.Branch {
		return deny("branch_changed")
	}
	omission := func(reason string) {
		out.Omitted++
		for _, r := range out.Omissions {
			if r == reason {
				return
			}
		}
		out.Omissions = append(out.Omissions, reason)
	}
	add := func(identity string, first int64, timestamp time.Time, message devicewire.PublicShareMessage) bool {
		identity = shareDigest([]byte(identity))
		if prior, ok := frontier.FirstSeen[identity]; ok {
			first = prior
		} else {
			frontier.FirstSeen[identity] = first
		}
		if len(frontier.FirstSeen) > ShareLogicalLimit {
			return false
		}
		out.Messages = append(out.Messages, devicewire.ShareProjectionMessage{Identity: identity, FirstSeen: first, Timestamp: timestamp, Message: message})
		return len(out.Messages) <= ShareLogicalLimit
	}
	type turn struct {
		id        string
		first     int64
		timestamp time.Time
		texts     []string
		execution []devicewire.ShareExecution
		tools     map[string]int
		bad       bool
	}
	var current *turn
	finish := func() bool {
		if current == nil {
			return true
		}
		defer func() { current = nil }()
		if current.bad || len(current.texts) == 0 {
			omission("component_incomplete")
			return true
		}
		// The final/latest text segment alone is outside manually expanded execution.
		segments, err := sanitizeShareSegments(current.texts)
		if err != nil {
			omission("component_limit")
			return true
		}
		final := segments[len(segments)-1]
		for _, clean := range segments[:len(segments)-1] {
			current.execution = append(current.execution, devicewire.ShareExecution{Kind: "text", Text: ShareDisplayText(clean)})
		}
		return add("turn:"+current.id, current.first, current.timestamp, devicewire.PublicShareMessage{Role: "assistant", Text: ShareDisplayText(final), Execution: current.execution})
	}
	for _, r := range records {
		if tree.hidden[r.id] || r.entry.IsSidechain {
			continue
		}
		e := r.entry
		if r.human {
			var texts []string
			for _, b := range r.blocks {
				if b.Type == "text" {
					texts = append(texts, b.Text)
				}
			}
			text, err := SanitizeShareText(strings.Join(texts, "\n"))
			if err != nil {
				omission("component_limit")
			} else if text != "" {
				if !add("human:"+r.id, r.start, e.Timestamp, devicewire.PublicShareMessage{Role: "user", Text: ShareDisplayText(text)}) {
					return deny("source_limit")
				}
			}
		} else if e.Type == "user" {
			onlyResults := len(r.blocks) > 0
			for _, b := range r.blocks {
				onlyResults = onlyResults && b.Type == "tool_result"
			}
			if !onlyResults && !e.IsMeta {
				omission("unknown_provenance")
			}
		}
		if e.Type == "assistant" && !e.IsMeta && len(e.Origin) == 0 {
			if current == nil {
				current = &turn{id: r.id, first: r.start, timestamp: e.Timestamp, tools: map[string]int{}}
			}
			for _, b := range r.blocks {
				switch b.Type {
				case "text":
					current.texts = append(current.texts, b.Text)
				case "tool_use":
					if b.ID == "" {
						current.bad = true
						continue
					}
					if _, exists := current.tools[b.ID]; exists {
						current.bad = true
						continue
					}
					name, err := SanitizeShareText(b.Name)
					if err != nil {
						current.bad = true
						continue
					}
					args, err := SanitizeShareArguments(b.Input)
					if err != nil {
						current.bad = true
						continue
					}
					current.tools[b.ID] = len(current.execution)
					current.execution = append(current.execution, devicewire.ShareExecution{Kind: "tool", ToolID: shareDigest([]byte(b.ID)), Name: ShareDisplayText(name), Arguments: ShareDisplayText(args), Results: []devicewire.ShareResult{}})
				}
			}
		}
		if e.Type == "user" && current != nil && !e.IsMeta && len(e.Origin) == 0 {
			for _, b := range r.blocks {
				if b.Type != "tool_result" {
					continue
				}
				index, ok := current.tools[b.ToolUseID]
				if !ok {
					omission("unassociated_execution")
					continue
				}
				blocks, ok := shareBlocks(b.Content)
				if !ok {
					current.bad = true
					continue
				}
				var texts []string
				for _, part := range blocks {
					if part.Type == "text" {
						texts = append(texts, part.Text)
					}
				}
				text, err := SanitizeShareText(strings.Join(texts, "\n"))
				if err != nil {
					current.bad = true
					continue
				}
				current.execution[index].Results = append(current.execution[index].Results, devicewire.ShareResult{Text: ShareDisplayText(text)})
			}
		}
		if e.Type == "system" && e.Subtype == "turn_duration" {
			if !finish() {
				return deny("source_limit")
			}
		}
		if e.Type == "user" && !e.IsMeta && current != nil {
			hasText := false
			for _, b := range r.blocks {
				hasText = hasText || b.Type == "text"
			}
			if hasText {
				omission("unfinished_turn")
				current = nil
			}
		}
	}
	if current != nil {
		identity := shareDigest([]byte("turn:" + current.id))
		if _, ok := frontier.FirstSeen[identity]; !ok {
			frontier.FirstSeen[identity] = current.first
		}
		omission("unfinished_turn")
	}
	if len(frontier.FirstSeen) > ShareLogicalLimit {
		return deny("source_limit")
	}
	out.Complete = true
	out.Frontier = frontier
	return out, nil
}
