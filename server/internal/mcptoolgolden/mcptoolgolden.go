// Package mcptoolgolden keeps the hub's MCP tool lists as goldens of one tool a file (#11252). The lists used to be
// one golden each, and the assistant's variants (GitHub signed in, iMessage handed over) repeated its whole list, so a
// change to one tool's description rewrote three files of about 190 KB and every PR that touched a tool conflicted in
// them. Now a change to one tool changes that tool's file, and a new tool adds one file and one line of an index.
//
// A list lives under a directory (Dir, relative to internal/hub) as
//
//	<list>.index.golden   the tool names in the list's order, one a line
//	<list>/<tool>.golden  each tool as the hub sends it, indented for reading
//
// and a variant of a list as one <variant>.diff.golden: the tools it adds, leaves out, moves or offers changed,
// against its base list (each new or changed tool in full).
//
// Only tests import this package. TestGoldenMCPTools (internal/hub) checks the files against the live lists and,
// with UPDATE_GOLDEN set, rewrites them; the tests that read a whole list (the prompt size ratchet, the supervision
// behaviour cases) rebuild it with Load. docs/assistant/prompts.md, "Golden tests".
package mcptoolgolden

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Dir is where the hub's tool goldens live, relative to the hub package (internal/hub).
const Dir = "testdata/prompts/mcp_tools"

const (
	indexSuffix = ".index.golden"
	diffSuffix  = ".diff.golden"
	toolSuffix  = ".golden"
)

// toolName is a tool or list name that is safe as a file name on every platform the tests run on.
var toolName = lazyre.New(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// update is the repository's switch for rewriting goldens (docs/assistant/prompts.md).
func update() bool { return os.Getenv("UPDATE_GOLDEN") != "" }

// entry is one tool of a list: its name and its golden, the wire JSON indented with a final newline.
type entry struct {
	name, text string
}

func entryOf(raw []byte) (entry, error) {
	b, err := json.MarshalIndent(json.RawMessage(raw), "", "  ") // compacts, then indents: the same bytes as the wire
	if err != nil {
		return entry{}, err
	}
	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return entry{}, err
	}
	if !toolName.MatchString(head.Name) {
		return entry{}, fmt.Errorf("tool name %q is not a safe file name", head.Name)
	}
	return entry{name: head.Name, text: string(b) + "\n"}, nil
}

func entriesOf(tools []proto.Tool) ([]entry, error) {
	out := make([]entry, 0, len(tools))
	folded := map[string]string{}
	for _, tool := range tools {
		b, err := json.Marshal(tool)
		if err != nil {
			return nil, err
		}
		e, err := entryOf(b)
		if err != nil {
			return nil, err
		}
		// One file a tool: a name twice (or twice but for case, on a case-insensitive file system) has no file of its own.
		if other, dup := folded[strings.ToLower(e.name)]; dup {
			return nil, fmt.Errorf("tools %q and %q share a golden file", other, e.name)
		}
		folded[strings.ToLower(e.name)] = e.name
		out = append(out, e)
	}
	return out, nil
}

// render is a list as one JSON array, indented, with a final newline: what the list's single golden once held.
func render(entries []entry) []byte {
	raws := make([]json.RawMessage, len(entries))
	for i, e := range entries {
		raws[i] = json.RawMessage(e.text)
	}
	b, err := json.MarshalIndent(raws, "", "  ")
	if err != nil {
		panic(err) // every entry came out of entryOf, so it is valid JSON
	}
	return append(b, '\n')
}

func renderIndex(entries []entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.name)
		b.WriteByte('\n')
	}
	return b.String()
}

// Load is the list as one JSON array, indented, with a final newline, rebuilt from its goldens: a base list from its
// index and tool files, a variant from its base and its difference. It is byte for byte what the list's single golden
// held before #11252.
func Load(dir, list string) ([]byte, error) {
	entries, err := load(dir, list)
	if err != nil {
		return nil, err
	}
	return render(entries), nil
}

func load(dir, list string) ([]entry, error) {
	if !toolName.MatchString(list) {
		return nil, fmt.Errorf("%q is not a list name", list)
	}
	raw, err := os.ReadFile(filepath.Join(dir, list+diffSuffix))
	if err == nil {
		var d diff
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("%s%s: %w", list, diffSuffix, err)
		}
		if _, err := os.Stat(filepath.Join(dir, d.Base+diffSuffix)); err == nil {
			return nil, fmt.Errorf("%s: its base %s is a variant itself", list, d.Base)
		}
		base, err := load(dir, d.Base)
		if err != nil {
			return nil, err
		}
		out, err := apply(base, d)
		if err != nil {
			return nil, fmt.Errorf("%s%s: %w", list, diffSuffix, err)
		}
		return out, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	index, err := os.ReadFile(filepath.Join(dir, list+indexSuffix))
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, name := range strings.Split(strings.TrimSuffix(string(index), "\n"), "\n") {
		if name == "" {
			continue // an empty list
		}
		if !toolName.MatchString(name) {
			return nil, fmt.Errorf("%s%s: %q is not a tool name", list, indexSuffix, name)
		}
		b, err := os.ReadFile(filepath.Join(dir, list, name+toolSuffix))
		if err != nil {
			return nil, err
		}
		e, err := entryOf(b)
		if err != nil {
			return nil, fmt.Errorf("%s/%s%s: %w", list, name, toolSuffix, err)
		}
		if e.name != name {
			return nil, fmt.Errorf("%s/%s%s holds the tool %q", list, name, toolSuffix, e.name)
		}
		out = append(out, e)
	}
	return out, nil
}

// file compares got with the golden at path, or writes it with UPDATE_GOLDEN set.
func file(t testing.TB, path, got string) bool {
	t.Helper()
	if update() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return true
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: %v (UPDATE_GOLDEN=1 writes it)", path, err)
		return false
	}
	if got != string(want) {
		t.Errorf("%s differs (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
		return false
	}
	return true
}

// absent reports a golden that should not be there (a removed tool's, or one from the other layout of a list: an
// index where the list is a variant now, a difference where it is a base list), or deletes it with UPDATE_GOLDEN set.
func absent(t testing.TB, path string) bool {
	t.Helper()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if update() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		return true
	}
	t.Errorf("%s is left over (UPDATE_GOLDEN=1 deletes it)", path)
	return false
}

// pruneTools reports the tool goldens in dir/list that keep is not, or deletes them with UPDATE_GOLDEN set. It looks
// at the .golden files of that one directory and nothing else.
func pruneTools(t testing.TB, dir, list string, keep map[string]bool) bool {
	t.Helper()
	files, err := os.ReadDir(filepath.Join(dir, list))
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	ok := true
	for _, f := range files {
		name, isGolden := strings.CutSuffix(f.Name(), toolSuffix)
		if f.IsDir() || !isGolden || keep[name] {
			continue
		}
		ok = absent(t, filepath.Join(dir, list, f.Name())) && ok
	}
	return ok
}

// loads checks that Load rebuilds the list, which is how the tests that read a whole list see it.
func loads(t testing.TB, dir, list string, entries []entry) {
	t.Helper()
	got, err := Load(dir, list)
	if err != nil {
		t.Errorf("Load(%s): %v", list, err)
		return
	}
	if !bytes.Equal(got, render(entries)) {
		t.Errorf("Load(%s) does not rebuild the list the hub offers", list)
	}
}

// CheckList compares a list with its goldens: its index and one file a tool. With UPDATE_GOLDEN set it writes them
// instead, and deletes the goldens of tools the list no longer has (in dir/<list> only).
func CheckList(t testing.TB, dir, list string, tools []proto.Tool) {
	t.Helper()
	entries, err := entriesOf(tools)
	if err != nil {
		t.Fatalf("%s: %v", list, err)
	}
	ok := absent(t, filepath.Join(dir, list+diffSuffix))
	ok = file(t, filepath.Join(dir, list+indexSuffix), renderIndex(entries)) && ok
	keep := map[string]bool{}
	for _, e := range entries {
		keep[e.name] = true
		ok = file(t, filepath.Join(dir, list, e.name+toolSuffix), e.text) && ok
	}
	ok = pruneTools(t, dir, list, keep) && ok
	if ok {
		loads(t, dir, list, entries)
	}
}

// CheckVariant compares a variant of the list base with its golden difference from it (<variant>.diff.golden), or
// writes it with UPDATE_GOLDEN set. baseTools are the base list as CheckList checked it.
func CheckVariant(t testing.TB, dir, variant, base string, baseTools, tools []proto.Tool) {
	t.Helper()
	baseEntries, err := entriesOf(baseTools)
	if err != nil {
		t.Fatalf("%s: %v", base, err)
	}
	entries, err := entriesOf(tools)
	if err != nil {
		t.Fatalf("%s: %v", variant, err)
	}
	d := compute(base, baseEntries, entries)
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt, err := apply(baseEntries, d); err != nil || !bytes.Equal(render(rebuilt), render(entries)) {
		t.Fatalf("%s: its difference from %s does not rebuild it (%v)", variant, base, err)
	}
	ok := absent(t, filepath.Join(dir, variant+indexSuffix))
	ok = pruneTools(t, dir, variant, nil) && ok
	ok = file(t, filepath.Join(dir, variant+diffSuffix), string(b)+"\n") && ok
	if ok {
		loads(t, dir, variant, entries)
	}
}

// diff is a variant's difference from its base list. Applied in this order: drop Removed and the tools Added moves,
// replace Changed where they are, then place Added one by one in the variant's order.
type diff struct {
	Base    string            `json:"base"`
	Removed []string          `json:"removed,omitempty"` // base tools the variant leaves out
	Changed []json.RawMessage `json:"changed,omitempty"` // base tools the variant offers in the same place, but different, in full
	Added   []added           `json:"added,omitempty"`   // tools the base has nowhere or elsewhere, in the variant's order
}

// added is one tool where the variant has it: right after the tool before it, at the start, or at the end (a run of
// them that ends the list stays at the end, so tools the base gains before it leave the difference alone).
type added struct {
	At    string          `json:"at,omitempty"`    // "start" or "end"
	After string          `json:"after,omitempty"` // or the tool it follows
	Name  string          `json:"name,omitempty"`  // a base tool moved here as it is
	Tool  json.RawMessage `json:"tool,omitempty"`  // or the tool in full: new, or a base tool moved and changed
}

// compute is variant's difference from base. The tools both have stay in place along the longest run that keeps the
// base's order; any other tool the variant has is added, so the difference is as small as the order allows.
func compute(baseName string, base, variant []entry) diff {
	d := diff{Base: baseName}
	pos := map[string]int{}
	for i, e := range base {
		pos[e.name] = i
	}
	has := map[string]bool{}
	for _, e := range variant {
		has[e.name] = true
	}
	for _, e := range base {
		if !has[e.name] {
			d.Removed = append(d.Removed, e.name)
		}
	}
	keep := inPlace(variant, pos)
	end := len(variant) // the run of added tools that ends the list
	for end > 0 && !keep[end-1] {
		end--
	}
	for i, e := range variant {
		if keep[i] {
			if e.text != base[pos[e.name]].text {
				d.Changed = append(d.Changed, json.RawMessage(e.text))
			}
			continue
		}
		a := added{}
		switch {
		case i >= end:
			a.At = "end"
		case i == 0:
			a.At = "start"
		default:
			a.After = variant[i-1].name
		}
		if p, ok := pos[e.name]; ok && base[p].text == e.text {
			a.Name = e.name
		} else {
			a.Tool = json.RawMessage(e.text)
		}
		d.Added = append(d.Added, a)
	}
	return d
}

// inPlace marks the variant's tools that stay where the base has them: the longest subsequence of the tools both
// lists have whose base positions rise (the first such run found, so the result is the same every time).
func inPlace(variant []entry, pos map[string]int) []bool {
	n := len(variant)
	length := make([]int, n) // the longest rising run ending at i
	prev := make([]int, n)
	best := -1
	for i, e := range variant {
		prev[i] = -1
		p, ok := pos[e.name]
		if !ok {
			continue
		}
		length[i] = 1
		for j := 0; j < i; j++ {
			if q, ok := pos[variant[j].name]; ok && q < p && length[j]+1 > length[i] {
				length[i], prev[i] = length[j]+1, j
			}
		}
		if best < 0 || length[i] > length[best] {
			best = i
		}
	}
	keep := make([]bool, n)
	for i := best; i >= 0; i = prev[i] {
		keep[i] = true
	}
	return keep
}

// apply rebuilds a variant from its base and its difference.
func apply(base []entry, d diff) ([]entry, error) {
	byName := map[string]entry{}
	for _, e := range base {
		byName[e.name] = e
	}
	drop := map[string]bool{}
	for _, name := range d.Removed {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("removes %q, which %s does not have", name, d.Base)
		}
		drop[name] = true
	}
	adds := make([]entry, len(d.Added))
	for i, a := range d.Added {
		var e entry
		switch {
		case a.Name != "" && len(a.Tool) == 0:
			var ok bool
			if e, ok = byName[a.Name]; !ok {
				return nil, fmt.Errorf("moves %q, which %s does not have", a.Name, d.Base)
			}
		case a.Name == "" && len(a.Tool) > 0:
			var err error
			if e, err = entryOf(a.Tool); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("added tool %d needs a name or a tool, not both", i)
		}
		if _, moved := byName[e.name]; moved {
			drop[e.name] = true
		}
		adds[i] = e
	}
	var out []entry
	for _, e := range base {
		if !drop[e.name] {
			out = append(out, e)
		}
	}
	at := func(name string) int {
		for i, e := range out {
			if e.name == name {
				return i
			}
		}
		return -1
	}
	for _, raw := range d.Changed {
		e, err := entryOf(raw)
		if err != nil {
			return nil, err
		}
		i := at(e.name)
		if i < 0 {
			return nil, fmt.Errorf("changes %q, which is not in place in %s", e.name, d.Base)
		}
		out[i] = e
	}
	for i, a := range d.Added {
		e := adds[i]
		if at(e.name) >= 0 {
			return nil, fmt.Errorf("adds %q twice", e.name)
		}
		switch {
		case a.At == "start" && a.After == "":
			out = slices.Insert(out, 0, e)
		case a.At == "end" && a.After == "":
			out = append(out, e)
		case a.At == "" && a.After != "":
			j := at(a.After)
			if j < 0 {
				return nil, fmt.Errorf("adds %q after %q, which is not in the list", e.name, a.After)
			}
			out = slices.Insert(out, j+1, e)
		default:
			return nil, fmt.Errorf("adds %q at %q after %q: say one of start, end or the tool before it", e.name, a.At, a.After)
		}
	}
	return out, nil
}
