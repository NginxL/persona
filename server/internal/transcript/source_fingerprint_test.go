package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPositionalSourceFingerprint(t *testing.T) {
	type event struct {
		Type      string            `json:"type"`
		Timestamp string            `json:"timestamp"`
		Message   map[string]string `json:"message"`
		UUID      string            `json:"uuid,omitempty"`
	}
	line := func(text, uuid string) []byte {
		b, _ := json.Marshal(event{Type: "user", Timestamp: "2026-11-20T00:00:00Z", UUID: uuid,
			Message: map[string]string{"role": "user", "content": text}})
		return b
	}
	prefix := strings.Repeat("a", pageTextLen+1024)
	original := line(prefix+"original", "")
	replacement := line(prefix+"modified", "")
	if len(original) != len(replacement) {
		t.Fatal("fixture must replace only the hidden tail at the same byte length")
	}
	path := filepath.Join(t.TempDir(), "work.jsonl")
	write := func(b []byte) {
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(original)
	file := NewFile(path)
	items, err := file.Refresh()
	if err != nil || len(items) != 1 {
		t.Fatalf("refresh: %d items, %v", len(items), err)
	}
	first := items[0]
	if first.ID != "line1#0" || len(first.SourceFingerprint) != 32 || first.FullSize == 0 {
		t.Fatalf("unexpected positional preview: id %s, proof length %d, full size %d", first.ID, len(first.SourceFingerprint), first.FullSize)
	}
	whole, err := file.Full(first.ID)
	if err != nil || whole.SourceFingerprint != first.SourceFingerprint || !strings.HasSuffix(whole.Text, "original") {
		t.Fatalf("full text did not carry the preview's proof: %v", err)
	}

	write(replacement)
	// The old index cannot safely read a changed raw line as if it belonged to the old preview.
	if _, err := file.Full(first.ID); err == nil {
		t.Fatal("changed line returned full text under the old proof")
	}
	restarted := NewFile(path)
	items, err = restarted.Refresh()
	if err != nil || len(items) != 1 || items[0].ID != first.ID || items[0].Text != first.Text ||
		items[0].FullSize != first.FullSize || items[0].SourceFingerprint == first.SourceFingerprint {
		t.Fatalf("same position and preview reused the old proof: %v", err)
	}
	stable := ParseLine(1, line("Stable runtime item", "example-runtime-id"), &Meta{})
	if len(stable) != 1 || stable[0].SourceFingerprint != "" {
		t.Fatalf("a runtime-owned ID unexpectedly acquired a fingerprint: %v", stable)
	}
}

func TestCodexFallbackSourceFingerprint(t *testing.T) {
	parse := func(text string) []string {
		data, err := json.Marshal(map[string]any{
			"type": "event_msg", "timestamp": "2026-11-20T00:00:00Z",
			"payload": map[string]any{"type": "item_completed", "item": map[string]any{
				"type": "AgentMessage", "phase": "final_answer",
				"content": []map[string]string{{"type": "Text", "text": text}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		items := ParseLine(12, data, &Meta{Codex: true})
		if len(items) != 1 || len(items[0].SourceFingerprint) != 32 {
			t.Fatalf("missing fallback proof: %+v", items)
		}
		return []string{items[0].ID, items[0].SourceFingerprint}
	}
	a, b := parse("Original Codex answer"), parse("Different Codex reply")
	if a[0] != "line12#0" || a[0] != b[0] || a[1] == b[1] {
		t.Fatal("Codex replacement at the same position reused the original proof")
	}
}

func TestShortPositionalFullRejectsSameSizeRewrite(t *testing.T) {
	const original = `{"type":"user","timestamp":"2026-11-20T00:00:00Z","message":{"role":"user","content":"First words"}}` + "\n"
	const replacement = `{"type":"user","timestamp":"2026-11-20T00:00:00Z","message":{"role":"user","content":"Other words"}}` + "\n"
	if len(original) != len(replacement) {
		t.Fatal("fixture must preserve the file size")
	}
	path := filepath.Join(t.TempDir(), "work.jsonl")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(original)
	file := NewFile(path)
	items, err := file.Refresh()
	if err != nil || len(items) != 1 || items[0].FullSize != 0 || items[0].SourceFingerprint == "" {
		t.Fatalf("expected one uncut positional item: %+v, %v", items, err)
	}
	first := items[0]
	if current, err := file.Full(first.ID); err != nil || current.SourceFingerprint != first.SourceFingerprint {
		t.Fatalf("the original should be readable before the rewrite: %v", err)
	}
	write(replacement)
	if fresh, err := file.Refresh(); err != nil || len(fresh) != 0 {
		t.Fatalf("the equal-size rewrite should exercise the old cached page: %d items, %v", len(fresh), err)
	}
	if _, err := file.Full(first.ID); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Full verified an obsolete uncut positional item: %v", err)
	}
}
