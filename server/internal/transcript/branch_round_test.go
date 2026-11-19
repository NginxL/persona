package transcript

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A background supervisor's rounds (#7315): each is resumed at the end of the conversation's first turn, so every round's
// message hangs from that line, beside the rounds before it. The shapes are the tree Claude Code 2.1.322 writes for
// `--resume <id> --resume-session-at <uuid>`, reduced to its messages, answers and turn ends; the attachment lines it
// also writes around each turn are in branch_round_shapes_test.go.

// roundText is a supervision message as the manager renders a round of it (the marker on its own line), or without the
// marker when marked is false.
func roundText(n int, marked bool) string {
	return prompts.RenderSupervise(prompts.Supervise{Leader: true, Background: true, Fresh: marked,
		Entries: []prompts.SuperviseEntry{{ID: fmt.Sprintf("t%d", n), Title: fmt.Sprintf("Round %d", n), Kind: "idle"}}})
}

// rounds is the start turn (u0 → a0, the anchor) and three rounds from a0, each answered and ended; marked says the
// rounds carry the marker.
func rounds(marked bool) *tline {
	l := &tline{}
	l.user("u0", "", "这是后台督办对话。回一句「好」就结束。", 1)
	l.reply("a0", "u0", "好", 2)
	l.turnEnd("t0", "a0", 3)
	for i := 1; i <= 3; i++ {
		u, a, e := fmt.Sprintf("r%d", i), fmt.Sprintf("ra%d", i), fmt.Sprintf("rt%d", i)
		l.user(u, "a0", roundText(i, marked), 10*i)
		l.reply(a, u, fmt.Sprintf("第 %d 轮处理完了", i), 10*i+1)
		l.turnEnd(e, a, 10*i+2)
	}
	return l
}

func roundWords(items []proto.Item) []string {
	var out []string
	for _, it := range items {
		if it.Kind != proto.KindText {
			continue
		}
		if it.Role == "user" && strings.HasPrefix(it.Text, "📋") {
			out = append(out, "📋")
			continue
		}
		out = append(out, it.Text)
	}
	return out
}

// Every round stays in view, in file order, and the history token says nothing was abandoned: a client keeps what it
// has. An owner's message between rounds (a plain resume, so it continues the latest round) stays too, and so does a
// recap written beside the rounds.
func TestSuperviseRoundsStayVisible(t *testing.T) {
	l := rounds(true)
	p := writeLines(t, l)
	f := NewFile(p)
	fresh, span, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"这是后台督办对话。回一句「好」就结束。", "好", "📋", "第 1 轮处理完了", "📋", "第 2 轮处理完了", "📋", "第 3 轮处理完了"}
	if got := roundWords(fresh); strings.Join(got, "|") != strings.Join(all, "|") {
		t.Fatalf("items %q, want %q", got, all)
	}
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q says a round was abandoned", span.History)
	}

	more := &tline{}
	more.user("o1", "rt3", "顺便看看第二个任务", 40) // the owner's own message: a plain resume of the latest round
	more.reply("oa1", "o1", "看了", 41)
	more.turnEnd("ot1", "oa1", 42)
	more.user("r4", "a0", roundText(4, true), 50)
	more.reply("ra4", "r4", "第 4 轮处理完了", 51)
	more.turnEnd("rt4", "ra4", 52)
	more.add(`{"type":"system","subtype":"away_summary","uuid":"s1","parentUuid":"a0","timestamp":"2026-11-05T10:01:00Z","content":"recap"}`)
	appendLines(t, p, more)
	fresh, span2, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	if got := roundWords(fresh); strings.Join(got, "|") != "顺便看看第二个任务|看了|📋|第 4 轮处理完了" {
		t.Fatalf("new items %q", got)
	}
	if span2.History != span.History {
		t.Fatalf("history %q → %q", span.History, span2.History)
	}
	if got := roundWords(allItems(f)); len(got) != len(all)+4 {
		t.Fatalf("all items %q", got)
	}
	// A node that reads the file from the start arrives at the same items and token.
	g := NewFile(p)
	_, span3, _ := g.RefreshSpan()
	if span3.History != span2.History || strings.Join(roundWords(allItems(g)), "|") != strings.Join(roundWords(allItems(f)), "|") {
		t.Fatalf("a fresh read: %q %q", span3.History, roundWords(allItems(g)))
	}

	// The node's quick read of the end keeps the last round's words as the conversation's last words.
	var tail Tail
	var meta Meta
	for _, line := range strings.Split(strings.TrimSpace(l.b.String()+more.b.String()), "\n") {
		tail.Line(&meta, parseLine(0, []byte(line), &meta))
	}
	if tail.Settle(&meta) {
		t.Fatal("the tail dropped words of a round")
	}
}

// Without the marker the same shape is what it always was: going back to the first answer, three times. Only the last
// one stays, and the token counts the two abandoned.
func TestRewindWithoutMarkerStillHidden(t *testing.T) {
	f := NewFile(writeLines(t, rounds(false)))
	_, span, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	if got := roundWords(allItems(f)); strings.Join(got, "|") != "这是后台督办对话。回一句「好」就结束。|好|📋|第 3 轮处理完了" {
		t.Fatalf("items %q", got)
	}
	if !strings.HasSuffix(span.History, ".r2") {
		t.Fatalf("history %q", span.History)
	}
	// An owner's message that goes back beside an unmarked one is a rewind as before, also after rounds.
	l := rounds(true)
	l.user("o1", "rt3", "问一下", 40)
	l.reply("oa1", "o1", "答", 41)
	l.turnEnd("ot1", "oa1", 42)
	l.user("o2", "rt3", "问一下（改）", 43)
	l.reply("oa2", "o2", "答（改）", 44)
	g := NewFile(writeLines(t, l))
	_, span, _ = g.RefreshSpan()
	got := roundWords(allItems(g))
	if strings.Contains(strings.Join(got, "|"), "问一下|") || !strings.HasSuffix(strings.Join(got, "|"), "问一下（改）|答（改）") || !strings.HasSuffix(span.History, ".r1") {
		t.Fatalf("items %q, history %q", got, span.History)
	}
}

// The share projection reads the same tree: every round's answer is part of the conversation.
func TestShareProjectionKeepsRounds(t *testing.T) {
	answers := func(data []byte) []string {
		t.Helper()
		out, err := ProjectShareBytes(context.Background(), data, "source", "incarnation", nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range out.Messages {
			if strings.Contains(m.Message.Text, "轮处理完了") {
				got = append(got, m.Message.Text)
			}
		}
		return got
	}
	if got := answers([]byte(rounds(true).b.String())); len(got) != 3 {
		t.Fatalf("marked rounds: %q", got)
	}
	if got := answers([]byte(rounds(false).b.String())); len(got) != 1 || got[0] != "第 3 轮处理完了" {
		t.Fatalf("unmarked: %q", got)
	}
	// The round marker in a block next to the message-source context, as the manager sends it.
	text, _ := json.Marshal(prompts.MessageSourceText(roundText(1, true), &proto.MessageSource{Kind: proto.SourceSupervision}))
	l := rounds(true)
	l.add(fmt.Sprintf(`{"type":"user","uuid":"r5","parentUuid":"a0","message":{"role":"user","content":[{"type":"text","text":%s}]}}`, text))
	l.reply("ra5", "r5", "第 5 轮处理完了", 60)
	l.turnEnd("rt5", "ra5", 61)
	if got := answers([]byte(l.b.String())); len(got) != 4 {
		t.Fatalf("with a source block: %q", got)
	}
}
