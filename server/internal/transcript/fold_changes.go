package transcript

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// A line diff past these sizes counts as everything removed and everything added: the apps' line diff gives up there
// and draws it so (web/src/lib/compare.ts diffLines).
const (
	changeDiffLimit    = 60_000 // lines on both sides
	changeDiffMaxEdits = 6_000
)

// clippedNote matches the exact and approximate byte counts at the end of clipped tool input.
var clippedNote = lazyre.New(regexp.QuoteMeta(clipNotePrefix) + `≈?\d+` + regexp.QuoteMeta(clipNoteSuffix) + "$")

var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true}

// foldChanges is what the run's successful edits changed (Edit, Write, MultiEdit; a Codex file change is an Edit with
// its unified diff), by path in the order first edited: the lines each edit adds and removes as the apps count them
// (web/src/views/chat/ActionRow.tsx editDiffs), so a turn's changed-files card says the same whether its work came
// folded or not. A call counts when its result is in the run and is not an error. Never nil: an empty list says the
// run edited nothing (spec/fixtures/fold-changes holds the cases the web checks too).
func foldChanges(run []proto.Item) []proto.FoldChange {
	ok := map[string]bool{}
	for _, it := range run {
		if it.Kind == proto.KindToolResult && it.ToolUseID != "" {
			ok[it.ToolUseID] = !it.IsError
		}
	}
	out := []proto.FoldChange{}
	at := map[string]int{}
	for _, it := range run {
		if it.Kind != proto.KindToolUse || !editTools[it.ToolName] || !ok[it.ToolUseID] {
			continue
		}
		var in map[string]any
		if json.Unmarshal(it.ToolInput, &in) != nil {
			continue
		}
		file := editPath(in)
		if file == "" {
			continue
		}
		add, del := editCounts(it.ToolName, in)
		exact := !clipped(in)
		if i, seen := at[file]; seen {
			out[i].Add += add
			out[i].Del += del
			out[i].Exact = out[i].Exact && exact
			continue
		}
		at[file] = len(out)
		out = append(out, proto.FoldChange{Path: file, Add: add, Del: del, Exact: exact})
	}
	return out
}

// editPath is the file an edit names, as the apps read it (web/src/lib/files.ts toolFile), cleaned (path.Clean: no
// "./", "../" or doubled slashes), so one file is one entry however its edits wrote it.
func editPath(in map[string]any) string {
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if s, _ := in[k].(string); strings.TrimSpace(s) != "" {
			return path.Clean(strings.TrimSpace(s))
		}
	}
	return ""
}

// editCounts is the lines an edit adds and removes.
func editCounts(tool string, in map[string]any) (add, del int) {
	switch tool {
	case "Edit":
		if d, isDiff := in["diff"].(string); isDiff {
			change, _ := in["change"].(string)
			return patchLines(d, change)
		}
		o, oldOK := in["old_string"].(string)
		n, newOK := in["new_string"].(string)
		if oldOK || newOK {
			return lineChanges(o, n)
		}
	case "Write":
		if c, isText := in["content"].(string); isText {
			return lineChanges("", c)
		}
	case "MultiEdit":
		edits, _ := in["edits"].([]any)
		for _, e := range edits {
			m, _ := e.(map[string]any)
			o, _ := m["old_string"].(string)
			n, _ := m["new_string"].(string)
			a, d := lineChanges(o, n)
			add, del = add+a, del+d
		}
	}
	return add, del
}

// editLines is an edit's two texts as lines. A newline at a text's end ends its last line, not starts another, on a
// side whose other side is empty or ends in a newline too: a Write of "a\nb\n" is two lines, and "foo" becoming
// "foo\n" is still a change (web/src/lib/turn-changes.ts editLines).
func editLines(old, new string) (a, b []string) {
	oldEnds, newEnds := strings.HasSuffix(old, "\n"), strings.HasSuffix(new, "\n")
	split := func(s string, strip bool) []string {
		if s == "" {
			return nil
		}
		if strip {
			s = s[:len(s)-1]
		}
		return strings.Split(s, "\n")
	}
	return split(old, oldEnds && (new == "" || newEnds)), split(new, newEnds && (old == "" || oldEnds))
}

// lineChanges is the lines added and removed between two texts by Myers' line diff (the one git uses), or all of
// both when that is too big to work out.
func lineChanges(old, new string) (add, del int) {
	a, b := editLines(old, new)
	d, found := editDistance(a, b)
	if !found {
		return len(b), len(a)
	}
	return (len(b) - len(a) + d) / 2, (len(a) - len(b) + d) / 2
}

// editDistance is the fewest lines to add and remove to make a into b (Myers' forward pass), and false past
// changeDiffLimit lines or changeDiffMaxEdits edits.
func editDistance(a, b []string) (int, bool) {
	n, m := len(a), len(b)
	if n+m > changeDiffLimit {
		return 0, false
	}
	most := min(n+m, changeDiffMaxEdits)
	off := most + 1
	v := make([]int, 2*most+3)
	for d := 0; d <= most; d++ {
		for k := -d; k <= d; k += 2 {
			x := v[off+k-1] + 1
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return d, true
			}
		}
	}
	return 0, false
}

// patchLines is the lines a Codex file change's unified diff adds and removes (web/src/lib/files.ts parseDiff); a
// patch without hunks is the whole file, added, or removed when the change deletes it.
func patchLines(patch, change string) (add, del int) {
	ls := strings.Split(patch, "\n")
	hunks := false
	for _, l := range ls {
		if strings.HasPrefix(l, "@@") {
			hunks = true
			break
		}
	}
	if !hunks {
		if patch == "" {
			return 0, 0
		}
		n := len(strings.Split(strings.TrimSuffix(patch, "\n"), "\n"))
		if change == "delete" {
			return 0, n
		}
		return n, 0
	}
	in := false
	for _, l := range ls {
		switch {
		case strings.HasPrefix(l, "diff --git"):
			in = false
		case !in && (strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ")):
		case strings.HasPrefix(l, "@@"):
			in = true
		case in && strings.HasPrefix(l, "+"):
			add++
		case in && strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
}

// clipped says whether any string in a tool input ends in the note clipJSON leaves where it cut.
func clipped(v any) bool {
	switch v := v.(type) {
	case string:
		return clippedNote.MatchString(v)
	case []any:
		for _, e := range v {
			if clipped(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range v {
			if clipped(e) {
				return true
			}
		}
	}
	return false
}
