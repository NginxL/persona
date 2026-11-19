package node

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The assistant's live snapshot (the time, agents waiting, unread chats, today's meetings…) goes at the start of each
// message, in a <persona-context> block, not into the system prompt (docs/assistant/assistant.md "Live snapshots and prefix caching"). The prompt cache
// is a prefix: the system prompt comes before the whole conversation, so a system prompt that differs from one start
// to the next would make every request after a restart write the entire history again. Claude Code (2.1.314) keeps a
// resumed conversation on the system prompt it was first started with instead, so the snapshot there was the one
// from the conversation's first day.
//
// Only what changed since the conversation last got it is sent (the time always): what it has seen is kept per
// conversation in memory and forgotten when it is compacted, which summarizes the old blocks away. A node restart
// forgets it too, and the next message carries the whole snapshot again.
//
// A changed section is sent whole, except the ones whose rows carry an id of their own (rowIDs): of those only the
// rows that are new or changed go out, with the ids of the rows that are gone (#9078). One task's row changes on
// almost every message, and its siblings were most of what a conversation was told again.

// snapshotSection is one top-level line of the snapshot with the indented lines under it; key is what it is about
// (the "- Lark calendar" part of "- Lark calendar today next: ..." or "- Lark calendar: ...", in Chinese).
type snapshotSection struct{ key, text string }

func snapshotSections(text string) []snapshotSection {
	var out []snapshotSection
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(out) > 0 {
			out[len(out)-1].text += "\n" + line
			continue
		}
		out = append(out, snapshotSection{key: sectionKey(line), text: line})
	}
	// Two sections about the same thing stay two: the later one is told apart by its place.
	count := map[string]int{}
	for i, s := range out {
		if count[s.key]++; count[s.key] > 1 {
			out[i].key = s.key + " #" + strconv.Itoa(count[s.key])
		}
	}
	return out
}

// sectionCount is a count a heading ends with (" 3 个", three of them, in "- Lark unread chats 3": in Chinese).
var sectionCount = lazyre.New(` \d+ 个$`)

// sectionKey is a line's subject: up to the first full-width colon or parenthesis, cut before the word "today" and
// without a count at its end, so a section is the same one whatever it says: "Lark calendar today next:" and "Lark
// calendar: no more meetings today", "Following (3):" and "Following: nothing", "Lark unread chats 3 (…)" and
// "Lark unread chats 4 (…)" (all in Chinese).
func sectionKey(line string) string {
	k, _, _ := strings.Cut(line, "：")
	k, _, _ = strings.Cut(k, "（")
	k, _, _ = strings.Cut(k, "今天")
	return sectionCount.ReplaceAllString(strings.TrimSpace(k), "")
}

const (
	timeSection = "- 现在"
	// listMark starts the snapshot's own sections; the lines before them say what the conversation is.
	listMark = "- "
)

// rowIDs are the sections whose rows each carry an id of their own, and how a row tells it: a task's row its to-do
// id, an Agents or unread-chats row the label it starts with (one row per label). The keys are sectionKeys of
// prompts/assistant/snapshot.md and snapshot_own_tasks.md (TestSnapshotRowIDs renders them). The other sections have
// no such rows and are compared whole: one line of items joined by "；", which their free text may hold too; the
// Inbox follow-ups, which show a title and no id; the knowledge digest, whose rows sit under headings of its own.
var rowIDs = map[string]func(row string) string{
	"- Agents":       rowLabel,
	"- 优先显示的开放任务详情":  rowTodo,
	"- 这个对话派出或督办的任务": rowTodo,
	"- 飞书未读会话":       rowLabel,
}

// rowTodoID is the id a task's row gives after its title: "（todo_1）：" or "（todo_1，链接 …）：".
var rowTodoID = lazyre.New(`（(todo_[0-9A-Za-z_]+)(?:，链接 [^）]*)?）：`)

func rowTodo(row string) string {
	if m := rowTodoID.FindStringSubmatch(row); m != nil {
		return m[1]
	}
	return ""
}

// rowLabel is the label a row starts with: "- Needs you: ..." is "Needs you", "- Done (24 hours): ..." is "Done".
func rowLabel(row string) string {
	label, ok := strings.CutPrefix(strings.TrimSpace(row), listMark)
	if !ok || !strings.Contains(label, "：") {
		return ""
	}
	label, _, _ = strings.Cut(label, "：")
	label, _, _ = strings.Cut(label, "（")
	return strings.TrimSpace(label)
}

// sectionRows is a section's heading line and its rows: each indented line, with the lines indented deeper under it.
func sectionRows(text string) (head string, rows []string) {
	head, rest, _ := strings.Cut(text, "\n")
	indent := -1
	for line := range strings.SplitSeq(rest, "\n") {
		if line == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 {
			indent = n
		}
		if n > indent && len(rows) > 0 {
			rows[len(rows)-1] += "\n" + line
			continue
		}
		rows = append(rows, line)
	}
	return head, rows
}

// idsOf are the ids of rows, ok when each has one of its own that a list of ids can hold ("；" separates them).
func idsOf(rows []string, id func(string) string) (ids []string, ok bool) {
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		k := id(r)
		if k == "" || taken[k] || strings.Contains(k, "；") {
			return nil, false
		}
		taken[k] = true
		ids = append(ids, k)
	}
	return ids, true
}

// rowDelta is what changed in section key between the text a conversation has (old) and the snapshot's (text), row
// by row: the rows that are new or changed, the ids of those that are gone, and the rows' order when the reader
// could not tell it (prompts.TurnSection). ok is false when the section is not one to tell by rows: no rowIDs for
// it, or a row on either side without an id of its own.
func rowDelta(key, old, text string) (delta prompts.TurnSection, ok bool) {
	id := rowIDs[key]
	if id == nil {
		return delta, false
	}
	oldHead, oldRows := sectionRows(old)
	head, rows := sectionRows(text)
	oldIDs, oldOK := idsOf(oldRows, id)
	ids, newOK := idsOf(rows, id)
	if !oldOK || !newOK {
		return delta, false
	}
	delta = prompts.TurnSection{Partial: true, Name: key}
	if head != oldHead {
		delta.Text = head
	}
	had := make(map[string]string, len(oldRows))
	for i, k := range oldIDs {
		had[k] = oldRows[i]
	}
	has := make(map[string]bool, len(rows))
	var added []string
	for i, k := range ids {
		has[k] = true
		before, known := had[k]
		if !known {
			added = append(added, k)
		}
		if !known || before != rows[i] {
			delta.Rows = append(delta.Rows, rows[i])
		}
	}
	var kept []string // the order the reader has without being told: the rows it knew, then the new ones
	for _, k := range oldIDs {
		if has[k] {
			kept = append(kept, k)
		} else {
			delta.Removed = append(delta.Removed, k)
		}
	}
	if !slices.Equal(append(kept, added...), ids) {
		delta.Order = ids
	}
	return delta, true
}

// changedSection is how section s goes to a conversation that has old of it: by rows where that is shorter (the
// two are rendered to compare), else whole, which replaces all the conversation had of it.
func changedSection(s snapshotSection, old string) prompts.TurnSection {
	whole := prompts.TurnSection{Text: s.text}
	if delta, ok := rowDelta(s.key, old, s.text); ok && turnSectionLen(delta) < turnSectionLen(whole) {
		return delta
	}
	return whole
}

func turnSectionLen(s prompts.TurnSection) int {
	return len(prompts.RenderTurnContext(prompts.TurnContext{Sections: []prompts.TurnSection{s}}))
}

// contextDelta is the block for a message and what the conversation will have seen once it is sent: everything when
// seen is nil, else the sections that are new or changed (changedSection), the time always, and the names of the
// snapshot's sections that are gone, so what the conversation has equals the snapshot again. A line before the
// snapshot's own sections (what the conversation is, a one-time notice) is never told gone: it is not the state of
// something, and one that is no longer repeated still holds. "" when there is no snapshot.
func contextDelta(seen map[string]string, snapshot string) (string, map[string]string) {
	sections := snapshotSections(snapshot)
	if len(sections) == 0 {
		return "", seen
	}
	next := make(map[string]string, len(sections))
	c := prompts.TurnContext{Full: seen == nil}
	for _, s := range sections {
		old, known := seen[s.key]
		switch {
		case !known:
			c.Sections = append(c.Sections, prompts.TurnSection{Text: s.text})
		case old != s.text:
			c.Sections = append(c.Sections, changedSection(s, old))
		case s.key == timeSection:
			c.Sections = append(c.Sections, prompts.TurnSection{Text: s.text})
		}
		next[s.key] = s.text
	}
	for k, v := range seen {
		if _, still := next[k]; still {
			continue
		}
		if name, listed := strings.CutPrefix(k, listMark); listed {
			c.Gone = append(c.Gone, name)
		} else {
			next[k] = v
		}
	}
	slices.Sort(c.Gone)
	return prompts.RenderTurnContext(c), next
}

// turnContext is the context for conversation id's next message from the hub's answer (assistantContext): the
// <persona-context> block, then the <persona-view-errors> one when its last replies drew rich views that failed their
// check. commit, called once the message is on its way, records the snapshot as seen and acks the view errors (the
// hub keeps them until then: a message that fails to go out leaves them for the next one).
func (n *Node) turnContext(id string, c contextAnswer) (block string, commit func()) {
	n.mu.Lock()
	seen := n.ctxSent[id]
	n.mu.Unlock()
	block, next := contextDelta(seen, c.Text)
	snapshot := block != ""
	if c.ViewErrors != "" {
		block = strings.TrimSpace(block + "\n" + c.ViewErrors)
	}
	return block, func() {
		n.state.update(id, func(v *sessionState) bool {
			if !v.AwaitingFirstMessage {
				return false
			}
			v.AwaitingFirstMessage = false
			return true
		})
		if len(c.Receipts) > 0 {
			n.ackViewErrors(id, c.Receipts)
		}
		if !snapshot {
			return
		}
		n.mu.Lock()
		if n.ctxSent == nil {
			n.ctxSent = map[string]map[string]string{}
		}
		n.ctxSent[id] = next
		n.mu.Unlock()
	}
}

// contextCompacted forgets what conversation id has seen: its old blocks were summarized away.
func (n *Node) contextCompacted(id string) {
	n.mu.Lock()
	delete(n.ctxSent, id)
	n.mu.Unlock()
	n.forgetFallback(id)     // the refused turns are summarized away: its own model again (#3788)
	n.clearModelRecovery(id) // the temporary switch ended with the refused turns (#7824)
}

// toolsAtStart notes the tools a conversation's process started with (claude's init) and says so when they differ
// from its last start: the tool definitions come before the history in every request, so a different set (a tool
// added or gone: a deploy, another phone's tools) writes the whole conversation again, like a different system
// prompt would. Measured 2026-11-01: one MCP tool fewer, 0 of 275K tokens read from the cache; the same tools with
// their schema keys in another order, all of it read.
func (n *Node) toolsAtStart(id string, tools []string) {
	tools = slices.Sorted(slices.Values(tools))
	n.mu.Lock()
	if n.ctxTools == nil {
		n.ctxTools = map[string][]string{}
	}
	before, known := n.ctxTools[id]
	n.ctxTools[id] = tools
	n.mu.Unlock()
	if !known || slices.Equal(before, tools) {
		return
	}
	var added, gone []string
	for _, t := range tools {
		if !slices.Contains(before, t) {
			added = append(added, t)
		}
	}
	for _, t := range before {
		if !slices.Contains(tools, t) {
			gone = append(gone, t)
		}
	}
	n.logger.Printf("assistant %s: tools changed since its last start (+%v -%v): its prompt cache starts over", short(id), added, gone)
}

// contextAnswer is the hub's assistant.context answer (assistantwire.ContextResult).
type contextAnswer struct {
	Text       string          `json:"text"`
	ViewErrors string          `json:"view_errors"`
	Receipts   json.RawMessage `json:"view_error_receipts"`
}

// assistantContext asks the hub for the live snapshot (prompts.AssistantContext) and the view errors to report. The
// hub answers from what it read lately and waits only briefly for a new read, so this doesn't hold the message up.
func (n *Node) assistantContext(id string) contextAnswer {
	if n.peer == nil {
		return contextAnswer{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var res contextAnswer
	if err := n.peer.Call(ctx, "assistant.context", map[string]any{"session_id": id}, &res); err != nil {
		n.logger.Printf("assistant context: %v", err)
		return contextAnswer{}
	}
	if string(res.Receipts) == "null" {
		res.Receipts = nil
	}
	return res
}

// ackViewErrors tells the hub the message carrying the view errors of receipts was delivered. The hub scopes it to
// this machine; a lost ack only means they are told again with the next message.
func (n *Node) ackViewErrors(id string, receipts json.RawMessage) {
	if n.peer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := n.peer.Call(ctx, "assistant.context", map[string]any{"session_id": id, "ack": receipts}, nil); err != nil {
		n.logger.Printf("assistant view errors ack: %v", err)
	}
}
