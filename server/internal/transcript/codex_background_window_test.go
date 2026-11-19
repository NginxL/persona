package transcript

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A backgrounded command can finish turns after it started. A read opened by its end (its window starting at a later
// turn) and paged back must give what a full read gives: the call once, where it started, and only the result where
// it ended (#10817). Each rollout is opened with windows starting at every turn the sizes reach.
func TestCodexBackgroundAcrossWindows(t *testing.T) {
	turn := func(id string, items ...string) []string {
		out := []string{codexTestEvent(`{"type":"task_started","turn_id":"` + id + `"}`), codexTestItem(codexTestUser("u"+id, "turn "+id))}
		for _, it := range items {
			out = append(out, codexTestItem(it))
		}
		return append(out, codexTestEvent(`{"type":"task_complete","turn_id":"`+id+`"}`))
	}
	bg := func(id string) string { return codexCommandItem(t, id, "backgrounded", "started\n", 0, 1) }
	done := func(id, out string) string { return codexCommandItem(t, id, "completed", out, 0, 3) }
	rollout := func(turns ...[]string) []string {
		lines := []string{codexTestSession}
		for _, tr := range turns {
			lines = append(lines, tr...)
		}
		return lines
	}
	for _, c := range []struct {
		name  string
		lines []string
	}{
		{"finishes in the next turn", rollout(turn("t1", bg("bg-1")), turn("t2", done("bg-1", "final\n")), turn("t3"))},
		{"finishes turns later", rollout(turn("t1", bg("bg-1")), turn("t2"), turn("t3"), turn("t4", done("bg-1", "final\n")), turn("t5"))},
		{"recorded again in a later turn", rollout(turn("t1", bg("bg-1")), turn("t2", bg("bg-1")), turn("t3", done("bg-1", "final\n")))},
		{"finished, then a late repeat in its turn", rollout(turn("t1", bg("bg-1"), done("bg-1", "final\n"), bg("bg-1")), turn("t2"), turn("t3", done("e3", "sync\n")))},
		{"id used again after it finished", rollout(turn("t1", bg("bg-1")), turn("t2", done("bg-1", "first\n")), turn("t3", bg("bg-1")), turn("t4", done("bg-1", "second\n")))},
		{"two at once, finishing apart", rollout(turn("t1", bg("bg-1"), bg("bg-2")), turn("t2", done("bg-2", "two\n")), turn("t3"), turn("t4", done("bg-1", "one\n")))},
		{"still running at the end", rollout(turn("t1", bg("bg-1")), turn("t2"), turn("t3"))},
		{"the word in a message is no command", rollout(turn("t1", codexTestUser("m1", "the job was backgrounded")), turn("t2", done("bg-1", "sync\n")), turn("t3"))},
	} {
		t.Run(c.name, func(t *testing.T) { codexColdEqualsFull(t, c.lines) })
	}
}

// codexColdEqualsFull opens lines cold with windows starting at each turn its sizes reach, and checks that paging back,
// After(0) and Full give what a full read gives.
func codexColdEqualsFull(t *testing.T, lines []string) {
	t.Helper()
	defer func(w int64) { windowBytes = w }(windowBytes)
	data := []byte(strings.Join(lines, "\n") + "\n")
	path := filepath.Join(t.TempDir(), "rollout-2026-11-16T08-00-00-01a1.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewFile(path)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	starts := map[int]bool{}
	for k := 1; k < 40; k++ {
		windowBytes = int64(len(data)) * int64(k) / 40
		cold := NewFile(path)
		if _, err := cold.Refresh(); err != nil {
			t.Fatal(err)
		}
		if cold.base == 0 || starts[cold.base] {
			continue
		}
		starts[cold.base] = true
		from := cold.base + 1
		var paged []proto.Item
		for before := 0; ; {
			items, more, _ := cold.Page(before, 2)
			paged = append(append([]proto.Item(nil), items...), paged...)
			if !more || len(items) == 0 {
				break
			}
			before = items[0].Seq
		}
		if !bytes.Equal(marshal(t, paged), marshal(t, all)) {
			t.Fatalf("window from line %d, paged back:\n%s\nfull read:\n%s", from, codexItemShapes(paged), codexItemShapes(all))
		}
		after := NewFile(path)
		if _, err := after.Refresh(); err != nil {
			t.Fatal(err)
		}
		got, _, _ := after.After(0, 1<<30)
		if !bytes.Equal(marshal(t, got), marshal(t, all)) {
			t.Fatalf("window from line %d, After(0):\n%s\nfull read:\n%s", from, codexItemShapes(got), codexItemShapes(all))
		}
		for _, it := range all {
			if it.Kind != proto.KindToolUse {
				continue
			}
			want, err := full.Full(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			one := NewFile(path)
			if _, err := one.Refresh(); err != nil {
				t.Fatal(err)
			}
			if got, err := one.Full(it.ID); err != nil || got.Seq != want.Seq {
				t.Fatalf("window from line %d: Full(%s) gave seq %d (%v), a full read's gives %d", from, it.ID, got.Seq, err, want.Seq)
			}
		}
	}
	if len(starts) < 2 {
		t.Fatalf("only %d window starts were tried: the test checks too little", len(starts))
	}
}

// A command still running when a read is opened by its end finishes later: the new lines give only its result, as an
// incremental full read gives, and paging back then agrees with a full read (#10817).
func TestCodexBackgroundFinishesAfterColdOpen(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	var head []string
	head = append(head, codexTestSession)
	for i, items := range [][]string{{codexCommandItem(t, "bg-1", "backgrounded", "started\n", 0, 1)}, nil, nil, nil} {
		id := fmt.Sprintf("t%d", i+1)
		head = append(head, codexTestEvent(`{"type":"task_started","turn_id":"`+id+`"}`), codexTestItem(codexTestUser("u"+id, "turn "+id)))
		for _, it := range items {
			head = append(head, codexTestItem(it))
		}
		head = append(head, codexTestEvent(`{"type":"task_complete","turn_id":"`+id+`"}`))
	}
	tail := []string{
		codexTestEvent(`{"type":"task_started","turn_id":"t5"}`),
		codexTestItem(codexTestUser("ut5", "turn t5")),
		codexTestItem(codexCommandItem(t, "bg-1", "completed", "final\n", 0, 9)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t5"}`),
	}
	path := filepath.Join(t.TempDir(), "rollout-2026-11-16T08-00-00-01a1.jsonl")
	data := []byte(strings.Join(head, "\n") + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = int64(len(data)) / 4
	cold := NewFile(path)
	if _, err := cold.Refresh(); err != nil {
		t.Fatal(err)
	}
	if cold.base == 0 {
		t.Fatal("the file was not opened by its end: the test checks nothing")
	}
	if err := os.WriteFile(path, []byte(strings.Join(append(head, tail...), "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := cold.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range fresh {
		if it.ID == "bg-1#0" {
			t.Fatalf("the command's end after a cold open gave a second call:\n%s", codexItemShapes(fresh))
		}
	}
	var paged []proto.Item
	for before := 0; ; {
		items, more, _ := cold.Page(before, 2)
		paged = append(append([]proto.Item(nil), items...), paged...)
		if !more || len(items) == 0 {
			break
		}
		before = items[0].Seq
	}
	windowBytes = 1 << 40
	full := NewFile(path)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	if !bytes.Equal(marshal(t, paged), marshal(t, all)) {
		t.Fatalf("paged back after the command ended:\n%s\nfull read:\n%s", codexItemShapes(paged), codexItemShapes(all))
	}
}

func codexItemShapes(items []proto.Item) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "  %s seq=%d %s\n", it.ID, it.Seq, it.Kind)
	}
	return b.String()
}
