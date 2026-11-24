package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClosedTurnCoverageRequiresCurrentFile(t *testing.T) {
	const prompt = `{"type":"user","uuid":"u1","message":{"role":"user","content":"hello"},"timestamp":"2026-01-01T00:00:01Z"}`
	const reply = `{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"timestamp":"2026-01-01T00:00:02Z"}`
	const done = `{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","durationMs":2000,"timestamp":"2026-01-01T00:00:03Z"}`
	dir := t.TempDir()
	path := writeFile(t, dir, []string{prompt, reply, done}, "")
	f := NewFile(path)
	_, end, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	sentAt := time.Date(2026, 1, 1, 0, 0, 1, 500_000_000, time.UTC)
	if !f.CoversClosedTurnAfter(end, end.History, 1, sentAt) {
		t.Fatal("the current file's completed turn did not cover the send")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.jsonl")
	if err := os.WriteFile(replacement, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if f.CoversClosedTurnAfter(end, end.History, 1, sentAt) {
		t.Fatal("a replacement file inherited the old completed turn")
	}
	_, current, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	if current.History == end.History || !f.CoversClosedTurnAfter(current, current.History, 1, sentAt) {
		t.Fatal("the replacement's completed turn was not recognized after refresh")
	}
}
