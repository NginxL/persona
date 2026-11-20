package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func reportedAgyConversation(a host.Agent) string {
	s := a.AgentSession
	if a.Agent == agyAgent && s != nil && s.Source == "herdr:antigravity_cli" &&
		s.Agent == agyAgent && s.Kind == "id" && uuidRe.MatchString(s.Value) {
		return s.Value
	}
	return ""
}

// agyConversationLocked checks the latest raw herdr snapshot as well as the pane row.
// The row ID is pane-derived. Cwd and previously bound session IDs cannot identify
// a native conversation, and ambiguous reports do not authorize a read or resume.
func (n *Node) agyConversationLocked(id, pane string) string {
	a := n.agents[id]
	conversation := reportedAgyConversation(a)
	if conversation == "" || a.PaneID != pane || id != otherSession(a) || host.ParsePane(pane).Kind != host.Herdr {
		return ""
	}
	matches := 0
	for _, raw := range n.hostAgents[host.Herdr] {
		reported := reportedAgyConversation(raw)
		if raw.PaneID == pane {
			if reported != conversation {
				return ""
			}
			matches++
		} else if reported == conversation {
			return ""
		}
	}
	if matches != 1 {
		return ""
	}
	return conversation
}

func (n *Node) agyConversation(id, pane string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.agyConversationLocked(id, pane)
}

// agyNotice is a pane's history while its conversation is not known: one notice, in the node's own language like its
// other lines (one watch serves every client of the session); the hub says it in each client's (transcript_text.go).
func agyNotice(id string) agyHistory {
	return agyHistory{token: "agy-pane:" + id, seq: 1, items: []proto.Item{{
		ID: "agy#0", Seq: 1, Role: "system", Kind: "notice", Text: i18n.T(i18n.Default, "node-engine.agy_pane_integration"), TS: time.Now().UTC(),
	}}}
}

type agyWatch struct {
	pane, history, conversation string
	since                       int
	file                        os.FileInfo
}

func agyStepsHistory(conversation string) agyHistory {
	return agyHistory{token: "agy-pane:" + conversation + ":steps:lazy-tool-media"}
}

// agyRead loads the compact export for pages and live updates. The revision memo
// distinguishes a rewrite of complete records from an append or partial write.
func (n *Node) agyRead(root *os.Root, key, conversation string, previous os.FileInfo) (agyHistory, os.FileInfo, error) {
	empty := agyStepsHistory(conversation)
	info, err := root.Stat(agyTranscriptPath)
	if err != nil {
		return empty, nil, err
	}
	if !info.Mode().IsRegular() {
		return empty, nil, os.ErrNotExist
	}
	if previous != nil && os.SameFile(info, previous) && info.Size() == previous.Size() &&
		info.ModTime() == previous.ModTime() && time.Since(info.ModTime()) > time.Second {
		return agyHistory{}, info, nil
	}
	raw, err := root.ReadFile(agyTranscriptPath)
	if err != nil {
		return empty, nil, err
	}
	raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	steps, rev := parseAgySteps(raw, conversation)
	n.mu.Lock()
	if n.agyRevisions == nil {
		n.agyRevisions = newLRU[string, agyRevision](256)
	}
	if old, ok := n.agyRevisions.get(key); ok && old.branch == rev.branch {
		if len(raw) < old.length || sha256.Sum256(raw[:old.length]) != old.hash {
			rev.rewrite = fmt.Sprintf("%x", rev.hash[:10])
		} else {
			rev.rewrite = old.rewrite
		}
	}
	n.agyRevisions.put(key, rev)
	n.mu.Unlock()

	h := mapAgySteps(steps, info.ModTime())
	h.token = empty.token
	if rev.branch != "" || rev.native != "" || rev.rewrite != "" {
		h.token += ":" + rev.branch + ":" + rev.native + ":" + rev.rewrite
	}
	return h, info, nil
}

type agyPageParams struct {
	ItemID     string `json:"item_id"`
	SinceSeq   int    `json:"since_seq"`
	BeforeSeq  int    `json:"before_seq"`
	AfterSeq   *int   `json:"after_seq"`
	Limit      int    `json:"limit"`
	Turns      int    `json:"turns"`
	FromSeq    int    `json:"from_seq"`
	ThroughSeq int    `json:"through_seq"`
	UserInputs bool   `json:"user_inputs"`
	FoldWork   bool   `json:"fold_work"`
}

func agyLightItems(items []proto.Item) []proto.Item {
	out := make([]proto.Item, 0, len(items))
	for _, it := range items {
		out = append(out, transcript.LightItem(it))
	}
	return out
}

func agyIndex(items []proto.Item, seq int) int {
	return sort.Search(len(items), func(i int) bool { return items[i].Seq >= seq })
}

func agyAfter(items []proto.Item, seq int) int {
	return sort.Search(len(items), func(i int) bool { return items[i].Seq > seq })
}

func (h agyHistory) after(since int) (items []proto.Item, span transcript.Span, more bool) {
	items = h.items[agyAfter(h.items, since):]
	span = transcript.Span{History: h.token, From: since, To: max(since, h.seq)}
	if len(items) > 500 {
		// Keep every item from the same native step on the same page.
		end := agyAfter(items, items[499].Seq)
		more = end < len(items)
		items = items[:end]
		if more {
			span.To = items[end-1].Seq
		}
	}
	return agyLightItems(items), span, more
}

func (h agyHistory) page(p agyPageParams) any {
	if p.UserInputs {
		index := proto.UserInputIndex{History: h.token, Inputs: []proto.UserInputPosition{}}
		for _, it := range h.items {
			if it.Role == "user" && it.Kind == proto.KindText {
				index.Inputs = append(index.Inputs, proto.UserInputPosition{ID: it.ID, Seq: it.Seq})
			}
		}
		index.Total = len(index.Inputs)
		return index
	}
	items := h.items
	if p.ThroughSeq > 0 {
		from, to := agyIndex(items, p.FromSeq), agyAfter(items, p.ThroughSeq)
		items = items[min(from, to):to]
		if len(items) > 1000 {
			end := agyAfter(items, items[999].Seq)
			return map[string]any{"items": agyLightItems(items[:end]), "more": end < len(items)}
		}
		return map[string]any{"items": agyLightItems(items), "more": false}
	}
	if p.AfterSeq != nil {
		page, span, more := h.after(*p.AfterSeq)
		return withSpan(map[string]any{"items": page, "has_more": false, "more_after": more}, span, true)
	}
	span := transcript.Span{History: h.token, To: h.seq}
	if p.BeforeSeq > 0 {
		items = items[:agyIndex(items, p.BeforeSeq)]
		span.To = 0
		if len(items) > 0 {
			span.To = items[len(items)-1].Seq
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 80
	}
	start := max(0, len(items)-min(limit, 500))
	if p.Turns > 0 {
		turns := min(p.Turns, 10)
		start = max(0, len(items)-2000)
		for i := len(items) - 1; i >= start; i-- {
			if items[i].Role == "user" && items[i].Kind == proto.KindText {
				turns--
				if turns == 0 {
					start = i
					break
				}
			}
		}
	}
	if start > 0 {
		start = agyIndex(items, items[start].Seq)
	}
	items = agyLightItems(items[start:])
	if p.FoldWork && p.BeforeSeq == 0 {
		items = transcript.FoldWorkLatest(items)
	} else if p.FoldWork {
		items = transcript.FoldWork(items)
	}
	return withSpan(map[string]any{"items": nonNil(items), "has_more": start > 0}, span, false)
}

func (n *Node) agyPaneCall(ctx context.Context, method, id, pane string, raw json.RawMessage) (any, error, bool) {
	switch method {
	case "session.unwatch":
		n.unwatch(id)
		return okResult(nil), nil, true
	case "session.items", "session.item", "session.image", "session.watch":
	default:
		return nil, nil, false
	}
	var p agyPageParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err, true
	}
	if method == "session.watch" {
		n.watchAgyPane(rpc.ConnContext(ctx), id, pane, p.SinceSeq)
		return okResult(nil), nil, true
	}

	mu := n.pushLock(id)
	mu.Lock()
	defer mu.Unlock()
	h := agyNotice(id)
	conversation := n.agyConversation(id, pane)
	if conversation != "" {
		h = agyStepsHistory(conversation)
		dir := n.agyDataRoot()
		root, err := agyBrain(dir, conversation)
		if err == nil {
			defer root.Close()
			h, _, err = n.agyRead(root, filepath.Join(dir, conversation), conversation, nil)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read Antigravity transcript: %w", err), true
		}
		if method == "session.image" {
			media, found := h.media[p.ItemID]
			if found && n.agyConversation(id, pane) == conversation {
				image, err := media.image(root)
				if n.agyConversation(id, pane) == conversation {
					return image, err, true
				}
			}
		}
		if method == "session.item" {
			for _, it := range h.items {
				if it.ID != p.ItemID {
					continue
				}
				if step, ok := h.outputs[it.ID]; ok {
					path := filepath.Join(".system_generated", "steps", fmt.Sprint(step), "output.txt")
					if output, err := agyReadFile(root, path); err == nil {
						it.Text = agyToolOutput(string(output))
					}
				}
				if n.agyConversation(id, pane) == conversation {
					return it, nil, true
				}
				break
			}
		}
		if n.agyConversation(id, pane) != conversation {
			h = agyNotice(id)
		}
	}
	if method == "session.item" {
		return nil, fmt.Errorf("no item"), true
	}
	if method == "session.image" {
		return nil, fmt.Errorf("no image"), true
	}
	return h.page(p), nil, true
}

func (n *Node) watchAgyPane(parent context.Context, id, pane string, since int) {
	n.mu.Lock()
	if cur := n.watches[id]; cur != nil {
		n.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w := &watch{cancel: cancel, ctx: ctx, parent: parent, id: id, agy: &agyWatch{pane: pane, since: since}}
	n.watches[id] = w
	if !n.pumping {
		n.pumping = true
		go n.watchPump()
	}
	n.mu.Unlock()
	context.AfterFunc(ctx, func() { n.watchEnded(w) })
	n.pollAgyPane(w)
	w.armed.Store(true)
}

func (n *Node) pollAgyPane(w *watch) {
	mu := n.pushLock(w.id)
	mu.Lock()
	defer mu.Unlock()
	if w.ctx.Err() != nil {
		return
	}
	a := w.agy
	h := agyNotice(w.id)
	conversation := n.agyConversation(w.id, a.pane)
	var info os.FileInfo
	if conversation != "" {
		h = agyStepsHistory(conversation)
		dir := n.agyDataRoot()
		root, err := agyBrain(dir, conversation)
		if err == nil {
			defer root.Close()
			var previous os.FileInfo
			if a.conversation == conversation {
				previous = a.file
			}
			h, info, err = n.agyRead(root, filepath.Join(dir, conversation), conversation, previous)
		}
		if err != nil && (!errors.Is(err, os.ErrNotExist) || a.file != nil && a.conversation == conversation) {
			return
		}
		if h.token == "" || n.agyConversation(w.id, a.pane) != conversation {
			return
		}
	}
	since := a.since
	reset := a.history != "" && a.history != h.token || since > h.seq
	initial := a.history == ""
	if initial && since == 0 {
		a.since = h.seq // the first page is the subscriber's backlog
		since = h.seq
	}
	if reset {
		since = 0
	}
	for reset || since < h.seq {
		items, span, more := h.after(since)
		if n.agyConversation(w.id, a.pane) != conversation {
			return
		}
		if n.peer != nil {
			err := n.peer.NotifyContext(w.ctx, "session.items", withSpan(map[string]any{"session_id": w.id, "items": items}, span, true))
			if err != nil {
				return
			}
		}
		a.since, since, reset = span.To, span.To, false
		a.history = h.token
		if !more {
			break
		}
	}
	a.history, a.file, a.conversation = h.token, info, conversation
}
