package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func agyParseLine(index int, typ, status, created, content string) string {
	return fmt.Sprintf(`{"step_index":%d,"type":%q,"status":%q,"created_at":%q,"content":%q}`+"\n", index, typ, status, created, content)
}

// agyReadNow is what agyRead makes of conversation agyTestID's transcript under data root dir as it is now.
func agyReadNow(t *testing.T, n *Node, dir string) agyHistory {
	t.Helper()
	root, err := agyBrain(dir, agyTestID)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h, _, err := n.agyRead(root, filepath.Join(dir, agyTestID), agyTestID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// agyParseNow is agyParse of the transcript file as agyRead reads it (complete lines only), and those bytes.
func agyParseNow(t *testing.T, n *Node, dir string) ([]agyStep, agyRevision, []byte) {
	t.Helper()
	file := filepath.Join(dir, "brain", agyTestID, agyTranscriptPath)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	steps, rev := n.agyParse(filepath.Join(dir, agyTestID), agyTestID, info, raw)
	return steps, rev, raw
}

// Going on from the last parse gives what a whole parse gives, whatever happened to the file in between (#13470): the
// same steps and revision, and agyRead the same history as a node that read the file as often but parsed it whole
// each time (its own memory of earlier revisions is what says a rewrite).
func TestAgyParseIncrementalMatchesFull(t *testing.T) {
	n, _ := testNode(t)
	whole, _ := testNode(t)
	dir := t.TempDir()
	file := agyTestFile(t, dir, agyTestID, "")
	writeRaw := func(s string) {
		if err := os.WriteFile(file, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	user := agyParseLine(0, "USER_INPUT", "DONE", "2026-01-01T00:00:00Z", "Hello")
	planner := agyParseLine(1, "PLANNER_RESPONSE", "RUNNING", "2026-01-01T00:00:01Z", "")
	plannerDone := agyParseLine(1, "PLANNER_RESPONSE", "DONE", "2026-01-01T00:00:01Z", "Working on it")
	tool := agyParseLine(3, "GENERIC", "DONE", "2026-01-01T00:00:03Z", "output three")
	late := agyParseLine(2, "GENERIC", "ERROR", "2026-01-01T00:00:02Z", "failed at once")
	rewind := agyParseLine(1, "PLANNER_RESPONSE", "DONE", "2026-01-01T00:00:09Z", "Another way")
	steps := []struct {
		name   string
		change func()
	}{
		{"first lines", func() { writeRaw(user + planner) }},
		{"an append", func() { agyTestAppend(t, file, plannerDone) }},
		{"a partial line", func() { agyTestAppend(t, file, tool[:20]) }},
		{"the line completed", func() { agyTestAppend(t, file, tool[20:]) }},
		{"a late arrival", func() { agyTestAppend(t, file, late) }},
		{"a rewind", func() { agyTestAppend(t, file, rewind) }},
		{"nothing new", func() {}},
		{"an earlier line rewritten in place", func() {
			writeRaw(strings.Replace(user+planner+plannerDone+tool+late+rewind, "Hello", "Howdy", 1))
		}},
		{"a truncation", func() { writeRaw(user) }},
		{"a replaced file", func() {
			next := file + ".new"
			if err := os.WriteFile(next, []byte(user+plannerDone), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(next, file); err != nil {
				t.Fatal(err)
			}
		}},
		{"an append after the replacement", func() { agyTestAppend(t, file, tool) }},
	}
	for _, s := range steps {
		s.change()
		got, gotRev, raw := agyParseNow(t, n, dir)
		want, wantRev := parseAgySteps(raw, agyTestID)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotRev, wantRev) {
			t.Fatalf("%s: incremental parse differs from a whole one\n got %+v\nwant %+v", s.name, gotRev, wantRev)
		}
		whole.mu.Lock()
		whole.agyParses = nil // nothing to go on from: a whole parse
		whole.mu.Unlock()
		if h, w := agyReadNow(t, n, dir), agyReadNow(t, whole, dir); h.token != w.token || !reflect.DeepEqual(h.items, w.items) {
			t.Fatalf("%s: history %s %+v, a whole parse %s %+v", s.name, h.token, h.items, w.token, w.items)
		}
	}
}

// agyRead does not parse the bytes it parsed before again on an append; a rewrite of them it does.
func TestAgyParseGoesOnFromTheLastFold(t *testing.T) {
	n, _ := testNode(t)
	dir := t.TempDir()
	file := agyTestFile(t, dir, agyTestID, agyParseLine(0, "USER_INPUT", "DONE", "2026-01-01T00:00:00Z", "Hello"))
	agyReadNow(t, n, dir)
	// Mark the kept fold: a read that went on from it keeps the mark, one that parsed the file again does not.
	n.mu.Lock()
	var kept *agyParsed
	ok := false
	if n.agyParses != nil {
		kept, ok = n.agyParses.peek(filepath.Join(dir, agyTestID))
	}
	if ok && kept != nil {
		kept.fold.steps[0].Content = "marked"
	}
	n.mu.Unlock()
	if !ok || kept == nil {
		t.Fatal("agyRead kept no parse to go on from")
	}
	agyTestAppend(t, file, agyParseLine(1, "PLANNER_RESPONSE", "DONE", "2026-01-01T00:00:01Z", "Hi"))
	h := agyReadNow(t, n, dir)
	if len(h.items) != 2 || h.items[0].Text != "marked" || h.items[1].Text != "Hi" {
		t.Fatalf("append: %+v, want the kept fold and the new line only", h.items)
	}
	raw, _ := os.ReadFile(file)
	if err := os.WriteFile(file, bytes.Replace(raw, []byte("Hello"), []byte("Howdy"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := agyReadNow(t, n, dir); len(h.items) != 2 || h.items[0].Text != "Howdy" {
		t.Fatalf("rewrite: %+v, want a whole parse", h.items)
	}
}

// agyParse hands out steps of the caller's own (#13470): agyRead restoring a truncated reply from the full export
// changes its copy, not the fold the next read goes on from, and the next read replacing a step in place leaves the
// steps an earlier read was given as they were.
func TestAgyParseHandsOutItsOwnSteps(t *testing.T) {
	n, _ := testNode(t)
	dir := t.TempDir()
	middle := strings.Repeat("words ", 40)
	reply := "Start\n" + middle + "\nEnd"
	preview := fmt.Sprintf("Start\n<truncated %d bytes>\nEnd", len(middle)+2)
	const record = `{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-01-01T00:00:01Z","content":%q%s}` + "\n"
	user := agyParseLine(0, "USER_INPUT", "DONE", "2026-01-01T00:00:00Z", "Hello")
	file := agyTestFile(t, dir, agyTestID, user+fmt.Sprintf(record, preview, `,"truncated_fields":["content"]`))
	agyTestWrite(t, filepath.Join(dir, "brain", agyTestID, agyFullTranscriptPath), user+fmt.Sprintf(record, reply, ""))
	if h := agyReadNow(t, n, dir); len(h.items) != 2 || h.items[1].Text != reply {
		t.Fatalf("history %+v, want the reply restored from the full export", h.items)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := parseAgySteps(raw, agyTestID)
	n.mu.Lock()
	kept, ok := n.agyParses.peek(filepath.Join(dir, agyTestID))
	n.mu.Unlock()
	if !ok || kept == nil || !reflect.DeepEqual(kept.fold.steps, want) {
		t.Fatal("restoring the reply changed the parse kept to go on from")
	}

	// A tool call still running when a later step is logged, then its finished record: the next parse replaces it in
	// place in the fold.
	agyTestAppend(t, file, agyParseLine(2, "GENERIC", "RUNNING", "2026-01-01T00:00:02Z", "")+
		agyParseLine(3, "PLANNER_RESPONSE", "DONE", "2026-01-01T00:00:03Z", "Meanwhile"))
	first, _, _ := agyParseNow(t, n, dir)
	agyTestAppend(t, file, agyParseLine(2, "GENERIC", "DONE", "2026-01-01T00:00:02Z", "finished"))
	second, _, _ := agyParseNow(t, n, dir)
	if len(first) != 4 || len(second) != 4 {
		t.Fatalf("parsed %d steps, then %d, want 4", len(first), len(second))
	}
	if first[2].Status != "RUNNING" || second[2].Status != "DONE" {
		t.Fatalf("the first parse's steps changed under it: %q, then %q", first[2].Status, second[2].Status)
	}
}

// An item or image a pane does not have is not_found (404), not an uncoded error the hub serves as 502 (#13470).
func TestAgyPaneMissingItemIsNotFound(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID), agentOf(cursorAgent, "", proto.StatusIdle, "w1:p2", "w1:t1", "w1", home, "cursor"))
	agyTestFile(t, n.agyDataRoot(), agyTestID, agyParseLine(0, "USER_INPUT", "DONE", "2026-01-01T00:00:00Z", "Hello"))
	for _, c := range []struct{ method, session string }{
		{"session.item", agyTestRow},
		{"session.image", agyTestRow},
		{"session.item", "cursor-w1-p2"},
		{"session.image", "cursor-w1-p2"},
	} {
		raw, _ := json.Marshal(map[string]any{"session_id": c.session, "item_id": "missing#0"})
		_, err := n.handle(t.Context(), c.method, raw)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != proto.CodeNotFound {
			t.Fatalf("%s of %s: %v, want %s", c.method, c.session, err, proto.CodeNotFound)
		}
	}
}
