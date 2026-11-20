package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const agyTranscriptPath = ".system_generated/logs/transcript.jsonl"

type agyStep struct {
	Index           *int            `json:"step_index"`
	Type            string          `json:"type"`
	Source          string          `json:"source"`
	Status          string          `json:"status"`
	CreatedAt       string          `json:"created_at"`
	Content         string          `json:"content"`
	Thinking        string          `json:"thinking"`
	Error           string          `json:"error"`
	TruncatedFields []string        `json:"truncated_fields"`
	Media           json.RawMessage `json:"media"`
	ToolCalls       []*struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	seq    int
	key    string
	record [32]byte
}

type agyHistory struct {
	token string
	seq   int
	items []proto.Item
	// A compact export can leave full tool output in its step directory.
	outputs map[string]int
	media   map[string]agyMedia
}

type agyRevision struct {
	length                  int
	hash                    [32]byte
	branch, native, rewrite string
}

func agyDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:10])
}

// agyBrain opens exactly the conversation directory. The directory entry must be
// a real directory, and subsequent reads stay inside it even through symlinks.
func agyBrain(dir, id string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	path := filepath.Join("brain", id)
	entry, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir() {
		return nil, os.ErrNotExist
	}
	brain, err := root.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	info, err := brain.Stat(".")
	if err != nil || !os.SameFile(entry, info) {
		brain.Close()
		return nil, os.ErrNotExist
	}
	return brain, nil
}

func agyReadFile(brain *os.Root, path string) ([]byte, error) {
	info, err := brain.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return brain.ReadFile(path)
}

// parseAgySteps follows the native export rules in integrations/antigravity/lib/map.js.
// A same-step completion replaces its earlier record. A rewind invalidates the
// abandoned tail and gives replacement steps a new identity.
func parseAgySteps(raw []byte, conversation string) ([]agyStep, agyRevision) {
	var steps []agyStep
	rev := agyRevision{length: len(raw), hash: sha256.Sum256(raw)}
	for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var step agyStep
		if json.Unmarshal(line, &step) != nil {
			continue
		}
		step.record = sha256.Sum256(line)
		if step.Index != nil && *step.Index >= 0 {
			step.seq = *step.Index + 1
		} else if len(steps) > 0 {
			step.seq = steps[len(steps)-1].seq + 1
		} else {
			step.seq = 1
		}
		at := len(steps)
		if len(steps) > 0 && step.seq <= steps[len(steps)-1].seq {
			last := steps[len(steps)-1]
			// A step whose sequence nothing retained has is a late arrival, not a rewind: parallel tool calls are
			// exported as they finish, so a call that fails at once lands before its sibling's result. It takes its
			// place in order, keeps the branch (every item ID) and marks the content as changed.
			floor := len(steps) - 1
			for floor >= 0 && steps[floor].seq > step.seq {
				floor--
			}
			if step.seq < last.seq && (floor < 0 || steps[floor].seq != step.seq) {
				at = floor + 1
				rev.native = fmt.Sprintf("%x", step.record[:10])
			} else {
				if step.seq < last.seq || step.Type != last.Type || step.CreatedAt != last.CreatedAt {
					rev.branch = agyDigest(rev.branch + "\n" + string(line))
					rev.native = ""
				} else if step.record != last.record {
					rev.native = fmt.Sprintf("%x", step.record[:10])
				}
				for len(steps) > 0 && steps[len(steps)-1].seq >= step.seq {
					steps = steps[:len(steps)-1]
				}
				at = len(steps)
			}
		}
		// Pane item IDs deliberately differ from the companion's. Mixing in the conversation
		// isolates IDs across pane session switches, and named suffixes survive step completions.
		step.key = strconv.Itoa(step.seq) + "@" + agyDigest(conversation+"\n"+step.CreatedAt+"\n"+rev.branch)
		steps = slices.Insert(steps, at, step)
	}
	return steps, rev
}

// Match the whitespace used by JavaScript's \s and String.trim in cleanUserText.
const agyJSWhitespace = `\x09-\x0D\x20\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
const agySpace = `[` + agyJSWhitespace + `]`

var agyUserRequest = lazyre.New(`(?s)<USER_REQUEST>` + agySpace + `*(.*?)` + agySpace + `*</USER_REQUEST>`)

// Go regexp has no backreferences. Use one alternative per context tag (compat.ContextNames: the current and older
// names, as the companion's SNAPSHOT_RE).
var agySnapshot = lazyre.New(`(?s)(?:` + agyContextBlocks() + `|<persona-view-errors\b[^>]*>.*?</persona-view-errors>)` + agySpace + `*`)

// Keep s/S ASCII-only as in the companion's case-insensitive regexp (Go's (?i) would also match ſ).
var agyContextEnd = lazyre.New(`^` + agySpace + `*(?i:</)(?:` + agyContextEndNames() + `)(?i:>)` + agySpace + `*`)

func agyContextBlocks() string {
	var alt []string
	for _, n := range compat.ContextNames() {
		q := regexp.QuoteMeta(n)
		alt = append(alt, `<`+q+`>.*?</`+q+`>`)
	}
	return strings.Join(alt, "|")
}

func agyContextEndNames() string {
	var alt []string
	for _, n := range compat.ContextNames() {
		var b strings.Builder
		for _, r := range n {
			switch {
			case r == 's' || r == 'S':
				b.WriteString(`[sS]`)
			case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
				b.WriteString(`(?i:` + string(r) + `)`)
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		alt = append(alt, b.String())
	}
	return strings.Join(alt, "|")
}

var agyTrimSpace = lazyre.New(`^` + agySpace + `+|` + agySpace + `+$`)

func agyUserText(text string) string {
	if m := agyUserRequest.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	for {
		clean := agyContextEnd.ReplaceAllString(agySnapshot.ReplaceAllString(text, ""), "")
		if clean == text {
			return agyTrimSpace.ReplaceAllString(text, "")
		}
		text = clean
	}
}

func agyToolOutput(text string) string {
	if strings.HasPrefix(text, "Created At:") {
		if _, output, ok := strings.Cut(text, "\nOutput:\n"); ok {
			return output
		}
	}
	return text
}

func mapAgySteps(steps []agyStep, fallback time.Time) agyHistory {
	var history agyHistory
	var pending []string
	if fallback.IsZero() {
		fallback = time.Now()
	}
	fallback = fallback.UTC()
	for _, step := range steps {
		history.seq = step.seq
		at, err := time.Parse(time.RFC3339Nano, step.CreatedAt)
		if err != nil || at.IsZero() {
			at = fallback
		}
		add := func(suffix, role, kind, text string) *proto.Item {
			history.items = append(history.items, proto.Item{ID: step.key + "#" + suffix, Seq: step.seq,
				Role: role, Kind: kind, TS: at, Text: text})
			return &history.items[len(history.items)-1]
		}
		// A call's result follows its planner step. One that never came (interrupted, dropped) must not stay queued
		// to take the next result, which would shift every later result onto the wrong call.
		switch step.Type {
		case "PLANNER_RESPONSE", "USER_INPUT", "ERROR_MESSAGE":
			pending = pending[:0]
		}
		switch step.Type {
		case "USER_INPUT":
			if step.Source == "" || step.Source == "USER_EXPLICIT" {
				if text := agyUserText(step.Content); text != "" {
					add("0", "user", proto.KindText, text)
				}
			}
		case "PLANNER_RESPONSE":
			for _, part := range []struct{ kind, text string }{{proto.KindThinking, step.Thinking}, {proto.KindText, step.Content}} {
				if text := strings.TrimSpace(part.text); text != "" {
					add(part.kind, "assistant", part.kind, text)
				}
			}
			for k, call := range step.ToolCalls {
				if call == nil {
					continue
				}
				name, input := agyTool(call.Name, call.Args)
				callID := fmt.Sprintf("call_%s_%d", step.key, k)
				it := add("tool"+strconv.Itoa(k), "assistant", proto.KindToolUse, "")
				it.ToolName, it.ToolInput, it.ToolUseID = name, input, callID
				pending = append(pending, callID)
			}
		case "GENERIC":
			callID := "call_" + step.key + "_0"
			if len(pending) != 0 {
				callID, pending = pending[0], pending[1:]
			}
			text := agyToolOutput(step.Content)
			if step.Error != "" {
				text = step.Error
			}
			it := add("0", "user", proto.KindToolResult, text)
			it.ToolUseID, it.IsError = callID, step.Error != "" || step.Status == "ERROR"
			for _, field := range step.TruncatedFields {
				if field == "content" && step.Error == "" {
					if history.outputs == nil {
						history.outputs = make(map[string]int)
					}
					history.outputs[it.ID] = step.seq - 1
					break
				}
			}
			var entries []json.RawMessage
			if json.Unmarshal(step.Media, &entries) == nil {
				for index, entry := range entries {
					var media agyMedia
					if json.Unmarshal(entry, &media) != nil || !media.isImage() {
						continue
					}
					image := add("media"+strconv.Itoa(index), "assistant", proto.KindImage, "")
					image.ToolUseID, image.Image = callID, &proto.Image{Mime: media.Mime, Lazy: true}
					if history.media == nil {
						history.media = make(map[string]agyMedia)
					}
					history.media[image.ID] = media
				}
			}
		case "ERROR_MESSAGE":
			text := step.Error
			if text == "" {
				text = step.Content
			}
			if text == "" {
				text = "Error"
			}
			if !strings.HasPrefix(text, "API Error:") {
				text = "API Error: " + text
			}
			add("0", "assistant", proto.KindText, text)
		}
	}
	return history
}

var agyToolNames = map[string]string{
	"run_command": "Bash", "view_file": "Read", "read_file": "Read", "write_to_file": "Write", "write_file": "Write",
	"replace_file_content": "Edit", "edit_file": "Edit", "grep_search": "Grep", "find_by_name": "Glob",
	"search_web": "WebSearch", "read_url_content": "WebFetch", "ask_question": "AskUserQuestion", "invoke_subagent": "Agent",
	"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "multi_edit": "MultiEdit", "ls": "LS", "grep": "Grep", "glob": "Glob",
	"web_fetch": "WebFetch", "web_search": "WebSearch", "todo_write": "TodoWrite",
}

func agyTool(name string, raw json.RawMessage) (string, json.RawMessage) {
	if name == "" {
		name = "tool"
	}
	appName := agyToolNames[name]
	if appName == "" {
		appName = name
	}
	if raw = bytes.TrimSpace(raw); len(raw) > 0 && raw[0] == '[' {
		// Kept as an array, as the companion does; one too long to send is bounded like any other input, under "args".
		wrapped := append(append([]byte(`{"args":`), raw...), '}')
		if clipped := transcript.ClipToolInput(wrapped); len(clipped) < len(wrapped) {
			return appName, clipped
		}
		return appName, raw
	}
	var args map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&args) != nil || args == nil {
		return appName, json.RawMessage("{}")
	}
	first := func(keys ...string) any {
		for _, key := range keys {
			if v := args[key]; v != nil {
				return v
			}
		}
		return ""
	}
	switch name {
	case "run_command":
		args = map[string]any{"command": first("CommandLine", "command"), "description": first("toolSummary", "Cwd")}
	case "view_file", "read_file":
		args = map[string]any{"file_path": first("AbsolutePath", "Path", "file_path")}
	case "write_to_file", "write_file":
		args = map[string]any{"file_path": first("TargetFile", "Path", "file_path"), "content": first("CodeContent", "Content", "content")}
	case "replace_file_content", "edit_file":
		args = map[string]any{"file_path": first("TargetFile", "Path", "file_path"), "old_string": first("TargetContent", "Target", "old_string"),
			"new_string": first("ReplacementContent", "Replacement", "new_string")}
	case "grep_search":
		args = map[string]any{"pattern": first("Query", "Pattern", "pattern"), "path": first("SearchPath", "Path", "path")}
	case "find_by_name":
		args = map[string]any{"pattern": first("Pattern", "pattern"), "path": first("SearchDirectory", "Path", "path")}
	case "search_web":
		args = map[string]any{"query": first("query", "Query")}
	case "read_url_content":
		args = map[string]any{"url": first("Url", "url")}
	case "invoke_subagent":
		description := args["toolSummary"]
		if description == nil {
			description = "subagent"
		}
		// An absent Subagents field renders as an empty prompt. The companion JSON-encodes its empty fallback.
		prompt := first("Subagents")
		if _, ok := prompt.(string); !ok {
			encoded, _ := json.Marshal(prompt)
			prompt = string(encoded)
		}
		args = map[string]any{"description": description, "prompt": prompt}
	}
	encoded, _ := json.Marshal(args)
	return appName, transcript.ClipToolInput(encoded)
}
