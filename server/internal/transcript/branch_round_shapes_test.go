package transcript

import (
	"fmt"
	"strings"
	"testing"
)

// Two more shapes of a background supervisor's rounds (#7315): the attachment lines Claude Code 2.1.322 writes around
// each turn in -p mode, and the first deploy's file, older rounds chained one after another and then a round from the
// first turn.

// attachment writes an attachment line of kind (prompt_snapshot, deferred_tools_delta) hanging from parent.
func (l *tline) attachment(uuid, parent, kind string, n int) {
	l.add(fmt.Sprintf(`{"type":"attachment","uuid":%q,"parentUuid":%s,"timestamp":%q,"attachment":{"type":%q}}`, uuid, parentJSON(parent), ts(n), kind))
}

// As Claude Code 2.1.322 writes rounds in -p mode (recorded against a local mock of the model API): a prompt_snapshot
// attachment after every turn's answer (the start turn's hangs from the anchor beside the rounds), a
// deferred_tools_delta attachment between each round's message and its answer (the answer hangs from it), and the
// owner's plain resume hanging from the latest round's prompt_snapshot. Every round and the owner's message stay in
// view in file order, and history never moves.
func TestSuperviseRoundsWithCLIAttachments(t *testing.T) {
	l := &tline{}
	l.user("u0", "", "这是后台督办对话。回一句「好」就结束。", 1)
	l.reply("a0", "u0", "好", 2)
	l.attachment("p0", "a0", "prompt_snapshot", 3)
	round := func(l *tline, i int) {
		r := fmt.Sprintf("r%d", i)
		l.user(r, "a0", roundText(i, true), 10*i)
		l.attachment(r+"d", r, "deferred_tools_delta", 10*i+1)
		l.reply(r+"a", r+"d", fmt.Sprintf("第 %d 轮处理完了", i), 10*i+2)
		l.attachment(r+"p", r+"a", "prompt_snapshot", 10*i+3)
	}
	for i := 1; i <= 3; i++ {
		round(l, i)
	}
	p := writeLines(t, l)
	f := NewFile(p)
	_, span, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want := "这是后台督办对话。回一句「好」就结束。|好|📋|第 1 轮处理完了|📋|第 2 轮处理完了|📋|第 3 轮处理完了"
	if got := strings.Join(roundWords(allItems(f)), "|"); got != want {
		t.Fatalf("items %q, want %q", got, want)
	}
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q says a round was abandoned", span.History)
	}

	more := &tline{}
	more.user("o1", "r3p", "顺便看看第二个任务", 40)
	more.reply("o1a", "o1", "看了", 41)
	more.attachment("o1p", "o1a", "prompt_snapshot", 42)
	round(more, 5)
	appendLines(t, p, more)
	_, span2, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want += "|顺便看看第二个任务|看了|📋|第 5 轮处理完了"
	if got := strings.Join(roundWords(allItems(f)), "|"); got != want || span2.History != span.History {
		t.Fatalf("items %q, history %q → %q", got, span.History, span2.History)
	}
	// Read from the start, and read one line at a time as a node following the file does: the same items, and history
	// never moves.
	g := NewFile(p)
	_, span3, _ := g.RefreshSpan()
	if span3.History != span2.History || strings.Join(roundWords(allItems(g)), "|") != want {
		t.Fatalf("a fresh read: %q %q", span3.History, roundWords(allItems(g)))
	}
	h, histories := readByLine(t, l.b.String()+more.b.String())
	if got := strings.Join(roundWords(allItems(h)), "|"); got != want {
		t.Fatalf("read line by line: %q", got)
	}
	for _, v := range histories {
		if v != histories[len(histories)-1] {
			t.Fatalf("history moved as the lines came: %q", histories)
		}
	}
}

// readByLine writes content's lines to a new file one at a time, reading it after each as a node following the file
// does; the histories it read after the first line.
func readByLine(t *testing.T, content string) (*File, []string) {
	t.Helper()
	p := writeLines(t, &tline{})
	f := NewFile(p)
	var histories []string
	for _, line := range strings.SplitAfter(content, "\n") {
		if line == "" {
			continue
		}
		one := &tline{}
		one.b.WriteString(line)
		appendLines(t, p, one)
		_, span, err := f.RefreshSpan()
		if err != nil {
			t.Fatal(err)
		}
		histories = append(histories, span.History)
	}
	return f, histories
}

// The first deploy: a background supervisor whose earlier rounds went on one after another (each plain-resumed after
// the last, no marker), then a round from the end of its first turn, then the owner writing (a plain resume of that
// round). Nothing is abandoned: the old rounds, the new one and the owner's message all stay, and history has no .r.
func TestSuperviseRoundAfterChainedHistory(t *testing.T) {
	l := &tline{}
	l.user("u0", "", "这是后台督办对话。回一句「好」就结束。", 1)
	l.reply("a0", "u0", "好", 2)
	l.turnEnd("t0", "a0", 3)
	parent := "t0"
	for i := 1; i <= 3; i++ { // before #7315: each wake went on after the round before
		u, a, e := fmt.Sprintf("c%d", i), fmt.Sprintf("ca%d", i), fmt.Sprintf("ct%d", i)
		l.user(u, parent, roundText(i, false), 10*i)
		l.reply(a, u, fmt.Sprintf("第 %d 轮处理完了", i), 10*i+1)
		l.turnEnd(e, a, 10*i+2)
		parent = e
	}
	p := writeLines(t, l)
	f := NewFile(p)
	_, before, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	more := &tline{}
	more.user("r4", "a0", roundText(4, true), 50) // the first round from the first turn, beside the chain's start
	more.reply("r4a", "r4", "第 4 轮处理完了", 51)
	more.turnEnd("r4t", "r4a", 52)
	more.user("o1", "r4t", "顺便看看第二个任务", 60) // the owner's plain resume goes on from that round
	more.reply("o1a", "o1", "看了", 61)
	more.turnEnd("o1t", "o1a", 62)
	appendLines(t, p, more)
	_, after, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want := "这是后台督办对话。回一句「好」就结束。|好|📋|第 1 轮处理完了|📋|第 2 轮处理完了|📋|第 3 轮处理完了|📋|第 4 轮处理完了|顺便看看第二个任务|看了"
	if got := strings.Join(roundWords(allItems(f)), "|"); got != want {
		t.Fatalf("items %q, want %q", got, want)
	}
	if after.History != before.History || strings.Contains(after.History, ".r") {
		t.Fatalf("history %q → %q", before.History, after.History)
	}
	g := NewFile(p)
	_, fresh, _ := g.RefreshSpan()
	if fresh.History != after.History || strings.Join(roundWords(allItems(g)), "|") != want {
		t.Fatalf("a fresh read: %q %q", fresh.History, roundWords(allItems(g)))
	}
}
