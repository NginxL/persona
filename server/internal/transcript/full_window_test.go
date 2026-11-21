package transcript

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// windowedClaude writes a Claude transcript of 4,000 lines whose first one is a long question (cut on pages, so Full
// reads it again) and opens it by its end with a 4 KiB window. Every line but line bare (0: none) has a uuid and
// names the one before as its parent, so a uuid is also found in the line after the one that holds it.
func windowedClaude(t *testing.T, bare int) (*File, string) {
	t.Helper()
	windowBytes = 4 << 10
	first := strings.Repeat("the first question ", 2000) + "end" // 38 KB: past pageTextLen
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"user","uuid":"q0","timestamp":"2026-11-10T10:00:00Z","message":{"role":"user","content":%q}}`+"\n", first)
	parent := "q0"
	for i := 1; i < 4000; i++ {
		if i+1 == bare {
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-11-10T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"answer %d"}]}}`+"\n", i)
			continue
		}
		id := fmt.Sprintf("a%d", i)
		fmt.Fprintf(&b, `{"type":"assistant","uuid":%q,"parentUuid":%q,"timestamp":"2026-11-10T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"answer %d"}]}}`+"\n", id, parent, i)
		parent = id
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFile(p)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.base == 0 || f.win == nil {
		t.Fatalf("the file was not opened by its end (base %d)", f.base)
	}
	return f, first
}

// Full of an id no line holds (a stale id after the history token changed, a mistyped one) used to parse every
// window back to line 1 under the file's lock and keep all 4,000 items: now no line names it, and nothing loads.
// Lines 1 and 5 have a uuid, which names their items, so "line1#0" and "line5#0" are stale (from before a rewind).
func TestFullOfUnknownIDLoadsNothing(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	f, _ := windowedClaude(t, 0)
	base, items := f.base, len(f.items)
	for _, id := range []string{"nope#0", "nope", "nope-3#1", "fold:a1#0", `odd"id#0`, "line999999#0", "line5#0", "line1#0", ""} {
		_, err := f.Full(id)
		if proto.PositionalItemID(id) {
			if !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("Full(%q): %v", id, err)
			}
		} else if err == nil || err.Error() != "no item "+id {
			t.Fatalf("Full(%q): %v", id, err)
		}
		if f.base != base || len(f.items) != items {
			t.Fatalf("Full(%q) parsed back: base %d -> %d, items %d -> %d", id, base, f.base, items, len(f.items))
		}
	}
}

// A Claude line can hold a tool input shaped like a Codex rollout line; it is still a Claude line whose uuid names its
// items, so a stale "line5#0" loads nothing there either, while its own item and the later lines' items are found.
func TestFullOfStaleIDOfLineWithCodexShapedInputLoadsNothing(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	f, _ := windowedClaude(t, 0)
	data, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	lines[4] = `{"type":"assistant","uuid":"a4","parentUuid":"a3","timestamp":"2026-11-10T10:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp__x__y","input":{"type":"event_msg","payload":{}}}]}}`
	if err := os.WriteFile(f.Path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	g := NewFile(f.Path)
	if _, err := g.Refresh(); err != nil {
		t.Fatal(err)
	}
	base, items := g.base, len(g.items)
	if _, err := g.Full("line5#0"); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Full(line5#0): %v", err)
	}
	if g.base != base || len(g.items) != items {
		t.Fatalf("Full(line5#0) parsed back: base %d -> %d, items %d -> %d", base, g.base, items, len(g.items))
	}
	if got, err := g.Full("a4#0"); err != nil || got.ToolName != "mcp__x__y" || got.Seq != 5 {
		t.Fatalf("Full(a4#0) of the line with the Codex-shaped input: %+v, %v", got, err)
	}
	if got, err := g.Full("a5#0"); err != nil || got.Text != "answer 5" || got.Seq != 6 {
		t.Fatalf("Full(a5#0) of the line after it: %+v, %v", got, err)
	}
}

func TestFullOfOlderPositionalItemReportsOpenFailure(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	f, _ := windowedClaude(t, 5)
	if err := os.Remove(f.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Full("line5#0"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Full treated an unreadable window as absent: %v", err)
	}
}

// The search must not miss what is there: the first line's item (named again by the line after it) comes back
// whole, and an item in the middle loads only the lines from it on.
func TestFullOfOldItemStillFound(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	f, first := windowedClaude(t, 0)
	mid, err := f.Full("a2000#0")
	if err != nil || mid.Text != "answer 2000" || mid.Seq != 2001 {
		t.Fatalf("Full of a middle item: %+v, %v", mid, err)
	}
	if f.base == 0 || f.base > 2000 {
		t.Fatalf("base %d after Full of line 2001: parsed too little or everything", f.base)
	}
	got, err := f.Full("q0#0")
	if err != nil || got.Text != first || got.FullSize != 0 || got.Seq != 1 {
		t.Fatalf("Full of the first item: %d bytes (want %d), full size %d, %v", len(got.Text), len(first), got.FullSize, err)
	}
	if f.base != 0 {
		t.Fatalf("base %d after Full of line 1", f.base)
	}
	// A line without a uuid is named by its seq.
	g, _ := windowedClaude(t, 5)
	if got, err := g.Full("line5#0"); err != nil || got.Text != "answer 4" || got.Seq != 5 {
		t.Fatalf("Full(line5#0) of a line without a uuid: %+v, %v", got, err)
	}
	if g.base > 4 {
		t.Fatalf("Full(line5#0) did not parse back to line 5: base %d", g.base)
	}
}

// A Codex FileChange's items are "<id>-<n>#<k>" while its line says "id":"<id>": the search tries the id without its
// "-<n>" too.
func TestFullOfOldCodexFileChange(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	windowBytes = 4 << 10
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	line(`{"timestamp":"2026-11-10T10:00:00.000Z","type":"session_meta","payload":{"id":"01a1","cwd":"/r/x","cli_version":"0.159.3"}}`)
	line(`{"timestamp":"2026-11-10T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t0"}}`)
	line(`{"timestamp":"2026-11-10T10:00:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"f1","changes":{"/r/x/a.txt":{"type":"update","unified_diff":"@@ -1 +1 @@\n-a\n+b\n"},"/r/x/b.txt":{"type":"add","content":"b\n"}},"status":"completed"}}}`)
	line(`{"timestamp":"2026-11-10T10:00:03.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t0"}}`)
	for i := 1; i < 500; i++ {
		line(fmt.Sprintf(`{"timestamp":"2026-11-10T10:01:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t%d"}}`, i))
		line(fmt.Sprintf(`{"timestamp":"2026-11-10T10:01:01.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u%d","content":[{"type":"text","text":"question %d"}]}}}`, i, i))
		line(fmt.Sprintf(`{"timestamp":"2026-11-10T10:01:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"m%d","phase":"final_answer","content":[{"type":"Text","text":"answer %d"}]}}}`, i, i))
		line(fmt.Sprintf(`{"timestamp":"2026-11-10T10:01:03.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t%d"}}`, i))
	}
	p := filepath.Join(t.TempDir(), "rollout-2026-11-10T10-00-00-01a1.jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFile(p)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.base == 0 || f.win == nil {
		t.Fatalf("the rollout was not opened by its end (base %d)", f.base)
	}
	base, items := f.base, len(f.items)
	if _, err := f.Full("f9-0#0"); err == nil || f.base != base || len(f.items) != items {
		t.Fatalf("Full of an unknown FileChange: %v, base %d -> %d", err, base, f.base)
	}
	if got, err := f.Full("m250#0"); err != nil || got.Text != "answer 250" || f.base == 0 {
		t.Fatalf("Full(m250#0): %+v, %v, base %d", got, err, f.base)
	}
	for _, id := range []string{"f1-1#2", "f1-0#1"} {
		got, err := f.Full(id)
		if err != nil || got.ID != id || got.Seq != 3 {
			t.Fatalf("Full(%s): %+v, %v", id, got, err)
		}
	}
}
