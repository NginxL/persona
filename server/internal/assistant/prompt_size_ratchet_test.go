package assistant

// The size ratchet for the assistant's prompts (docs/assistant/prompts.md "Golden tests", #6881; the proposal's
// step P0, docs/design/proposals/assistant-instructions.md 4a). The shared instructions, the MCP tool descriptions
// and the supervision events grow one rule at a time, and nothing stops a removed manual from coming back; this
// test makes growth a visible decision. It measures the UTF-8 size of every watched piece, compares it with the
// baseline in testdata/prompt_sizes.json and fails when a piece grows past it. The baseline's total only goes
// down, like the Apple token ratchet (apple/PersonaTests TokenRatchetTests): a rule that has to grow is shortened
// or moved elsewhere first. A single piece may still grow when text moves between prompts and the total falls by
// more (a move into the shared instructions shrinks the goldens it came from); such a PR says where the text moved
// to. After a shrink or such a move, lower the baseline in the same PR by running
//
//	PROMPT_SIZE_UPDATE=1 go test ./internal/assistant -run PromptSizeRatchet
//
// which also adds new keys and drops removed ones, but refuses to write a larger total.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const promptSizeBaseline = "testdata/prompt_sizes.json"

// promptSizePieces returns the UTF-8 size in bytes of every piece the ratchet watches, keyed by a stable name:
// each "## " section of the assistant_claude_md goldens ("assistant_claude_md.full#<heading>", with the part before
// the first heading as "#head"), each tool of the assistant MCP list ("mcp_tools.assistant#<tool>", the compact
// json.Marshal of the whole tool object: description and input schema both count), and each supervision event
// golden as a whole ("supervise.<event>"). The sizes of one file's sections add up to the file's size.
func promptSizePieces(t *testing.T) map[string]int {
	t.Helper()
	sizes := map[string]int{}
	set := func(key string, size int) {
		if _, dup := sizes[key]; dup {
			t.Fatalf("duplicate prompt size key %q", key)
		}
		sizes[key] = size
	}

	// The shared instructions, one entry per "## " section.
	files, err := filepath.Glob(filepath.Join("testdata", "prompts", "assistant_claude_md.*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".golden")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(content) == 0 {
			t.Fatalf("%s is empty", path)
		}
		sectionKeys := []string{name + "#head"}
		sectionStarts := []int{0}
		for lineStart := 0; lineStart < len(content); {
			lineEnd := bytes.IndexByte(content[lineStart:], '\n')
			end := len(content)
			if lineEnd >= 0 {
				end = lineStart + lineEnd
			}
			if line := content[lineStart:end]; bytes.HasPrefix(line, []byte("## ")) {
				sectionKeys = append(sectionKeys, name+"#"+strings.TrimSpace(string(line[3:])))
				sectionStarts = append(sectionStarts, lineStart)
			}
			if lineEnd < 0 {
				break
			}
			lineStart = end + 1
		}
		for i, start := range sectionStarts {
			end := len(content)
			if i+1 < len(sectionStarts) {
				end = sectionStarts[i+1]
			}
			set(sectionKeys[i], end-start)
		}
	}

	// The assistant's MCP tools, one entry per tool object.
	toolsPath := filepath.Join("..", "hub", "testdata", "prompts", "mcp_tools.assistant.golden")
	raw, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers re-marshal exactly as written
	var tools []any
	if err := dec.Decode(&tools); err != nil {
		t.Fatalf("%s: %v", toolsPath, err)
	}
	for _, tool := range tools {
		obj, ok := tool.(map[string]any)
		if !ok {
			t.Fatalf("%s: tool is not an object: %T", toolsPath, tool)
		}
		name, _ := obj["name"].(string)
		if name == "" {
			t.Fatalf("%s: tool without a name", toolsPath)
		}
		compact, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("%s: %v", toolsPath, err)
		}
		set("mcp_tools.assistant#"+name, len(compact))
	}

	// The supervision events, one entry per golden.
	events, err := filepath.Glob(filepath.Join("testdata", "prompts", "supervise.*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range events {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		set(strings.TrimSuffix(filepath.Base(path), ".golden"), len(content))
	}
	return sizes
}

// promptSizeChange is one baseline entry that grew, with its old and new size in bytes.
type promptSizeChange struct {
	key      string
	from, to int
}

// promptSizeVerdict is whether the baseline may be rewritten for the measured sizes, with the entries that grew
// and the two totals the decision was made on.
type promptSizeVerdict struct {
	accept                      bool
	raised                      []promptSizeChange
	baselineTotal, currentTotal int
}

// decidePromptSizeUpdate reports whether the baseline may be rewritten for the measured sizes: yes when the total
// does not grow, so text can move between entries (one rises while others fall by more, as when a rule leaves the
// supervision events for the shared instructions), no when the total grows. The totals are the sums over the whole
// maps, so a new entry counts against the baseline and a removed one stops counting.
func decidePromptSizeUpdate(baseline, current map[string]int) promptSizeVerdict {
	verdict := promptSizeVerdict{}
	for _, size := range baseline {
		verdict.baselineTotal += size
	}
	for _, size := range current {
		verdict.currentTotal += size
	}
	for key, from := range baseline {
		if to, ok := current[key]; ok && to > from {
			verdict.raised = append(verdict.raised, promptSizeChange{key: key, from: from, to: to})
		}
	}
	sort.Slice(verdict.raised, func(i, j int) bool { return verdict.raised[i].key < verdict.raised[j].key })
	verdict.accept = verdict.currentTotal <= verdict.baselineTotal
	return verdict
}

func TestPromptSizeRatchet(t *testing.T) {
	baseline := map[string]int{}
	if raw, err := os.ReadFile(promptSizeBaseline); err == nil {
		if err := json.Unmarshal(raw, &baseline); err != nil {
			t.Fatalf("%s: %v", promptSizeBaseline, err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	current := promptSizePieces(t)

	if os.Getenv("PROMPT_SIZE_UPDATE") != "" {
		verdict := decidePromptSizeUpdate(baseline, current)
		if !verdict.accept {
			for _, r := range verdict.raised {
				t.Errorf("%s grew from %d to %d bytes", r.key, r.from, r.to)
			}
			t.Fatalf("not rewriting %s: the total grew from %d to %d bytes; the total only goes down",
				promptSizeBaseline, verdict.baselineTotal, verdict.currentTotal)
		}
		for _, r := range verdict.raised {
			t.Logf("raised %s from %d to %d bytes (moved text; total went from %d to %d)",
				r.key, r.from, r.to, verdict.baselineTotal, verdict.currentTotal)
		}
		added, removed, lowered := 0, 0, 0
		for key, to := range current {
			switch from, ok := baseline[key]; {
			case !ok:
				added++
				t.Logf("added %s at %d bytes", key, to)
			case to < from:
				lowered++
				t.Logf("lowered %s from %d to %d bytes", key, from, to)
			}
		}
		for key := range baseline {
			if _, ok := current[key]; !ok {
				removed++
				t.Logf("removed %s", key)
			}
		}
		out, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(promptSizeBaseline, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s: %d entries (%d added, %d removed, %d lowered, %d raised by a move), total %d -> %d bytes; read the diff before committing it",
			promptSizeBaseline, len(current), added, removed, lowered, len(verdict.raised), verdict.baselineTotal, verdict.currentTotal)
		return
	}

	keys := make([]string, 0, len(current)+len(baseline))
	for key := range current {
		keys = append(keys, key)
	}
	for key := range baseline {
		if _, ok := current[key]; !ok {
			keys = append(keys, key)
		}
	}
	// Sorted so a failure lists the same keys in the same order on every machine.
	sort.Strings(keys)
	for _, key := range keys {
		old, inBaseline := baseline[key]
		cur, inCurrent := current[key]
		switch {
		case !inCurrent:
			t.Errorf("%s is no longer measured; PROMPT_SIZE_UPDATE=1 go test ./internal/assistant -run PromptSizeRatchet rewrites the baseline", key)
		case !inBaseline:
			t.Errorf("%s is new at %d bytes; PROMPT_SIZE_UPDATE=1 go test ./internal/assistant -run PromptSizeRatchet adds it", key, cur)
		case cur > old:
			t.Errorf("%s grew from %d to %d bytes; growth is decided when the baseline is updated, never slipped in: when moving text between prompts, PROMPT_SIZE_UPDATE=1 go test ./internal/assistant -run PromptSizeRatchet accepts it as long as the total goes down", key, old, cur)
		case cur < old:
			t.Logf("%s shrank from %d to %d bytes; PROMPT_SIZE_UPDATE=1 go test ./internal/assistant -run PromptSizeRatchet lowers the baseline", key, old, cur)
		}
	}
}

func TestDecidePromptSizeUpdate(t *testing.T) {
	cases := []struct {
		name                        string
		baseline, current           map[string]int
		accept                      bool
		raised                      []string
		baselineTotal, currentTotal int
	}{
		{"all shrink", map[string]int{"a": 100, "b": 50}, map[string]int{"a": 90, "b": 40}, true, nil, 150, 130},
		{"a move: one rises, another falls by more", map[string]int{"a": 100, "b": 200}, map[string]int{"a": 110, "b": 150},
			true, []string{"a"}, 300, 260},
		{"one rises and the total grows", map[string]int{"a": 100, "b": 50}, map[string]int{"a": 160, "b": 50},
			false, []string{"a"}, 150, 210},
		{"a new key grows the total", map[string]int{"a": 100}, map[string]int{"a": 100, "c": 50}, false, nil, 100, 150},
		{"a removed key shrinks the total", map[string]int{"a": 100, "b": 50}, map[string]int{"a": 100}, true, nil, 150, 100},
		{"a move that keeps the total", map[string]int{"a": 100, "b": 100}, map[string]int{"a": 120, "b": 80},
			true, []string{"a"}, 200, 200},
	}
	for _, c := range cases {
		verdict := decidePromptSizeUpdate(c.baseline, c.current)
		if verdict.accept != c.accept {
			t.Errorf("%s: accept = %v, want %v", c.name, verdict.accept, c.accept)
		}
		if verdict.baselineTotal != c.baselineTotal || verdict.currentTotal != c.currentTotal {
			t.Errorf("%s: totals = %d -> %d, want %d -> %d", c.name,
				verdict.baselineTotal, verdict.currentTotal, c.baselineTotal, c.currentTotal)
		}
		var raised []string
		for _, r := range verdict.raised {
			raised = append(raised, r.key)
			if r.to <= r.from {
				t.Errorf("%s: raised entry %s must grow, got %d -> %d", c.name, r.key, r.from, r.to)
			}
		}
		if !reflect.DeepEqual(raised, c.raised) {
			t.Errorf("%s: raised = %v, want %v", c.name, raised, c.raised)
		}
	}
}
