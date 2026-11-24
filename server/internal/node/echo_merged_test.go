package node

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The blocks of one assistant message as the node writes them (assistant.go): the live snapshot, the interjection
// note when the message comes mid-turn, its pictures, its text.
func mergedMessageBlocks(text string, images int, midTurn bool) []map[string]any {
	blocks := []map[string]any{{"type": "text", "text": "<persona-context>\nLive snapshot (as of this message):\n- Time: 2026-01-01 Thu 09:00\n</persona-context>"}}
	if midTurn {
		blocks = append(blocks, interjectNote(false))
	}
	for range images {
		blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}})
	}
	if strings.TrimSpace(text) != "" || images == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return blocks
}

func mergedUserLine(t *testing.T, uuid string, at time.Time, messages ...[]map[string]any) string {
	t.Helper()
	var blocks []map[string]any
	for _, m := range messages {
		blocks = append(blocks, m...)
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return userBlocks(uuid, at, string(raw))
}

// wantMergedTags is every item of line uuid tagged with the send whose blocks hold it: owners[i] is the client_msg_id
// of the line's i-th block (items are named <uuid>#<block>).
func wantMergedTags(t *testing.T, items []proto.Item, uuid string, owners []string) map[string]string {
	t.Helper()
	want := map[string]string{}
	for _, it := range items {
		key, block, ok := strings.Cut(it.ID, "#")
		if !ok || key != uuid {
			continue
		}
		i, err := strconv.Atoi(block)
		if err != nil || i >= len(owners) {
			t.Fatalf("item %s is not one of the line's %d blocks", it.ID, len(owners))
		}
		want[it.ID] = owners[i]
	}
	return want
}

func blockOwners(id string, blocks []map[string]any) []string {
	out := make([]string, len(blocks))
	for i := range out {
		out[i] = id
	}
	return out
}

// Pictures sent through a channel while the assistant answers the first one: Claude Code queues the rest and, when
// the turn ends, writes them as one user line, each message's blocks after the previous one's (#14338). Every send is
// tied to its own blocks, so the channel run that folded all of them finds each message and answers with both turns.
func TestEchoTiesEachChannelMessageClaudeCodeQueuedIntoOneLine(t *testing.T) {
	t0 := time.Now()
	chat := func(text string) string {
		return prompts.RenderChannelMessage(prompts.ChannelMessage{Persona: true, Name: "WeChat", Language: "English", Plain: true, Asks: true, Text: text})
	}
	type send struct {
		id     string
		text   string
		images int
	}
	sends := []send{{"m1", chat(""), 1}, {"m2", chat(""), 1}, {"m3", chat(""), 1}, {"m4", chat(""), 1}, {"m5", chat("Which of these should I print?"), 0}}
	var l echoLog
	for i, s := range sends {
		l.expect(echoRecord{SessionID: "s", ClientMsgID: s.id, Text: s.text, Images: s.images, SentAt: t0.Add(time.Duration(i) * time.Second), AfterSeq: 1})
	}
	first := mergedMessageBlocks(sends[0].text, 1, false)
	var queued [][]map[string]any
	var owners []string
	for _, s := range sends[1:] {
		blocks := mergedMessageBlocks(s.text, s.images, true)
		queued = append(queued, blocks)
		owners = append(owners, blockOwners(s.id, blocks)...)
	}
	items := echoItems(t,
		assistantLine("a0", t0.Add(-time.Minute), "Earlier answer"),
		mergedUserLine(t, "first", t0.Add(500*time.Millisecond), first),
		assistantLine("a1", t0.Add(10*time.Second), "First answer"),
		mergedUserLine(t, "queued", t0.Add(11*time.Second), queued...),
		assistantLine("a2", t0.Add(20*time.Second), "Second answer"),
	)
	found := l.match("s", echoInTranscript, items[1:])
	if len(found) != len(sends) {
		t.Fatalf("tied %d of %d sends: %+v", len(found), len(sends), found)
	}
	if found[0].ClientMsgID != "m1" || found[0].Line != "first" || len(found[0].Parts) != 0 {
		t.Fatalf("the first message's own line: %+v", found[0])
	}
	for i, r := range found[1:] {
		if r.ClientMsgID != sends[i+1].id || r.Line != "queued" || len(r.Parts) == 0 {
			t.Fatalf("queued send %d: %+v", i+1, r)
		}
	}
	want := wantMergedTags(t, items, "first", blockOwners("m1", first))
	for id, c := range wantMergedTags(t, items, "queued", owners) {
		want[id] = c
	}
	tagged := l.tag("s", echoInTranscript, items)
	sameTags(t, tagged, want)
	if _, waiting := l.waitingFrom("s", echoInTranscript); waiting {
		t.Fatal("a queued send is still expected")
	}

	// The channel run folded the five messages: with each one tied, it answers with both turns' words.
	answer, all := channel.RunAnswer(&channel.Run{MsgIDs: []string{"m1", "m2", "m3", "m4", "m5"}}, tagged[1:])
	if !all {
		t.Fatal("the run still waits for one of its messages")
	}
	var texts []string
	for _, it := range answer {
		texts = append(texts, it.Text)
	}
	if got := strings.Join(texts, " | "); got != "First answer | Second answer" {
		t.Fatalf("answer %q", got)
	}

	// The hub keeps each record and hands it back to a node that started again: the parts come back with it.
	var restored echoLog
	for _, r := range found {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var back echoRecord
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		restored.expect(back)
	}
	sameTags(t, restored.tag("s", echoInTranscript, items), want)
}

// The same for messages sent from the app while the assistant answers, with words only: each takes its own text.
func TestEchoTiesEachAppMessageClaudeCodeQueuedIntoOneLine(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "plan", Text: "Here is the plan for tomorrow, please check it", SentAt: t0})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "remind", Text: "Add a reminder", SentAt: t0.Add(time.Second)})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "book", Text: "and book a table for seven people at eight", SentAt: t0.Add(2 * time.Second)})
	remind := mergedMessageBlocks("Add a reminder", 0, true)
	book := mergedMessageBlocks("and book a table for seven people at eight", 0, true)
	items := echoItems(t,
		mergedUserLine(t, "plan", t0, mergedMessageBlocks("Here is the plan for tomorrow, please check it", 0, false)),
		assistantLine("a1", t0.Add(5*time.Second), "Looks good"),
		mergedUserLine(t, "queued", t0.Add(6*time.Second), remind, book),
	)
	if found := l.match("s", echoInTranscript, items); len(found) != 3 {
		t.Fatalf("found %+v", found)
	}
	want := wantMergedTags(t, items, "plan", blockOwners("plan", mergedMessageBlocks("", 0, false)))
	for id, c := range wantMergedTags(t, items, "queued", append(blockOwners("remind", remind), blockOwners("book", book)...)) {
		want[id] = c
	}
	sameTags(t, l.tag("s", echoInTranscript, items), want)
}

// A line of one message is not split, even when a send expected after it could fit a part of it (a picture-only
// message after a message with words and a picture): the line stays the first send's, the other waits for its own.
func TestEchoDoesNotSplitALineOfOneMessage(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "words", Text: "Look at this error and tell me what broke", Images: 1, SentAt: t0})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "picture", Images: 1, SentAt: t0.Add(time.Second)})
	line := mergedMessageBlocks("Look at this error and tell me what broke", 1, false)
	items := echoItems(t, mergedUserLine(t, "u1", t0.Add(2*time.Second), line))
	found := l.match("s", echoInTranscript, items)
	if len(found) != 1 || found[0].ClientMsgID != "words" || len(found[0].Parts) != 0 {
		t.Fatalf("found %+v", found)
	}
	sameTags(t, l.tag("s", echoInTranscript, items), wantMergedTags(t, items, "u1", blockOwners("words", line)))
	if _, waiting := l.waitingFrom("s", echoInTranscript); !waiting {
		t.Fatal("the picture-only send should still be expected")
	}
	later := echoItems(t, mergedUserLine(t, "u1", t0.Add(2*time.Second), line), mergedUserLine(t, "u2", t0.Add(3*time.Second), mergedMessageBlocks("", 1, false)))
	if found := l.match("s", echoInTranscript, later); len(found) != 1 || found[0].ClientMsgID != "picture" || found[0].Line != "u2" {
		t.Fatalf("found %+v", found)
	}
}
