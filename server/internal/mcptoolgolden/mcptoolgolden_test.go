package mcptoolgolden

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func tool(name, description string) proto.Tool {
	return proto.Tool{Name: name, Description: description, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
}

func tools(names ...string) []proto.Tool {
	out := make([]proto.Tool, len(names))
	for i, n := range names {
		out[i] = tool(n, "Does "+n+".")
	}
	return out
}

func mustEntries(t *testing.T, list []proto.Tool) []entry {
	t.Helper()
	e, err := entriesOf(list)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// roundTrip computes variant's difference from base, sends it through JSON as the golden does and applies it.
func roundTrip(t *testing.T, base, variant []proto.Tool) (diff, []entry) {
	t.Helper()
	d := compute("base", mustEntries(t, base), mustEntries(t, variant))
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var back diff
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	got, err := apply(mustEntries(t, base), back)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, b)
	}
	return d, got
}

func names(entries []entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.name
	}
	return out
}

// Any variant comes back from its base and its difference: tools added, removed, moved and changed, anywhere.
func TestDifferenceRebuildsEveryVariant(t *testing.T) {
	r := rand.New(rand.NewSource(11252))
	for run := range 500 {
		base := tools()
		for i := range r.Intn(12) {
			base = append(base, tool(fmt.Sprintf("t%d", i), "Base."))
		}
		variant := slices.Clone(base)
		for range r.Intn(6) {
			switch op := r.Intn(4); {
			case op == 0 && len(variant) > 0: // remove
				i := r.Intn(len(variant))
				variant = slices.Delete(variant, i, i+1)
			case op == 1: // add a new one anywhere
				variant = slices.Insert(variant, r.Intn(len(variant)+1), tool(fmt.Sprintf("n%d", r.Intn(1000)), "New."))
			case op == 2 && len(variant) > 0: // move
				i := r.Intn(len(variant))
				moved := variant[i]
				variant = slices.Delete(variant, i, i+1)
				variant = slices.Insert(variant, r.Intn(len(variant)+1), moved)
			case op == 3 && len(variant) > 0: // change
				i := r.Intn(len(variant))
				variant[i].Description += " Changed."
			}
		}
		if _, err := entriesOf(variant); err != nil {
			continue // a new name drawn twice
		}
		_, got := roundTrip(t, base, variant)
		if string(render(got)) != string(render(mustEntries(t, variant))) {
			t.Fatalf("run %d: base %v, variant %v, rebuilt %v", run, names(mustEntries(t, base)), names(mustEntries(t, variant)), names(got))
		}
	}
}

// A variant that appends tools keeps the same difference when its base gains a tool: the appended run stays "at end".
func TestDifferenceOfAppendedToolsIgnoresNewBaseTools(t *testing.T) {
	d1, _ := roundTrip(t, tools("a", "b"), tools("a", "b", "x", "y"))
	d2, _ := roundTrip(t, tools("a", "b", "c"), tools("a", "b", "c", "x", "y"))
	b1, _ := json.Marshal(d1)
	b2, _ := json.Marshal(d2)
	if string(b1) != string(b2) {
		t.Fatalf("difference changed with the base:\n%s\n%s", b1, b2)
	}
	if len(d1.Added) != 2 || d1.Added[0].At != "end" || d1.Added[1].At != "end" || d1.Removed != nil || d1.Changed != nil {
		t.Fatalf("difference = %s", b1)
	}
}

// A tool changed in place is listed in full under changed; a tool moved and changed is added in full where it went.
func TestDifferenceListsChangedAndMovedTools(t *testing.T) {
	base := tools("a", "b", "c")
	inPlace := []proto.Tool{base[0], tool("b", "Other."), base[2]}
	if d, _ := roundTrip(t, base, inPlace); len(d.Changed) != 1 || len(d.Added) != 0 || d.Removed != nil {
		b, _ := json.Marshal(d)
		t.Fatalf("difference = %s", b)
	}
	variant := []proto.Tool{base[1], tool("a", "Other."), base[2]}
	d, got := roundTrip(t, base, variant)
	if len(d.Changed) != 0 || len(d.Added) != 1 || len(d.Added[0].Tool) == 0 || d.Added[0].After != "b" {
		b, _ := json.Marshal(d)
		t.Fatalf("difference = %s", b)
	}
	if strings.Join(names(got), ",") != "b,a,c" {
		t.Fatalf("rebuilt %v", names(got))
	}
}

// Load gives byte for byte what one json.MarshalIndent of the whole list gave, the single golden of before.
func TestLoadIsTheSingleGolden(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATE_GOLDEN", "1")
	list := []proto.Tool{tool("a", "Uses <tags> & \"quotes\", naïve café and \u2028."), tool("b", ""), {Name: "c", Description: "Deferred.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"n": {"type": "integer", "minimum": 1.50}}}`), DeferLoading: true}}
	CheckList(t, dir, "base", list)
	CheckVariant(t, dir, "variant", "base", list, append(slices.Clone(list[1:]), tool("d", "New.")))
	for name, want := range map[string][]proto.Tool{"base": list, "variant": append(slices.Clone(list[1:]), tool("d", "New."))} {
		b, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got, err := Load(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(b)+"\n" {
			t.Fatalf("Load(%s):\n%s\nwant:\n%s", name, got, b)
		}
	}
}

// recorder keeps the errors a check reports instead of failing the test with them.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func check(t *testing.T, run func(testing.TB)) []string {
	t.Helper()
	t.Setenv("UPDATE_GOLDEN", "")
	r := &recorder{TB: t}
	run(r)
	return r.errs
}

// The update writes one file a tool and the index, deletes removed tools' files in the list's directory only, and
// the check then fails on any difference: a tool, the order, a variant's difference, a stale file.
func TestCheckListUpdatesAndFailsOnEveryDifference(t *testing.T) {
	dir := t.TempDir()
	keepOutside := []string{filepath.Join(dir, "other", "b.golden"), filepath.Join(dir, "base", "notes.txt")}
	for _, p := range keepOutside {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("UPDATE_GOLDEN", "1")
	CheckList(t, dir, "base", tools("a", "b", "c"))
	CheckList(t, dir, "base", tools("a", "c"))
	if _, err := os.Stat(filepath.Join(dir, "base", "b.golden")); !os.IsNotExist(err) {
		t.Fatalf("b.golden not deleted: %v", err)
	}
	for _, p := range keepOutside {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if index, _ := os.ReadFile(filepath.Join(dir, "base.index.golden")); string(index) != "a\nc\n" {
		t.Fatalf("index = %q", index)
	}
	CheckVariant(t, dir, "variant", "base", tools("a", "c"), tools("a", "c", "z"))

	if errs := check(t, func(tb testing.TB) { CheckList(tb, dir, "base", tools("a", "c")) }); len(errs) != 0 {
		t.Fatalf("unchanged list: %v", errs)
	}
	changed := tools("a", "c")
	changed[1].Description = "Changed."
	if errs := check(t, func(tb testing.TB) { CheckList(tb, dir, "base", changed) }); len(errs) != 1 ||
		!strings.Contains(errs[0], filepath.Join("base", "c.golden")) {
		t.Fatalf("changed tool: %v", errs)
	}
	if errs := check(t, func(tb testing.TB) { CheckList(tb, dir, "base", tools("c", "a")) }); len(errs) != 1 ||
		!strings.Contains(errs[0], "base.index.golden") {
		t.Fatalf("reordered list: %v", errs)
	}
	if errs := check(t, func(tb testing.TB) { CheckList(tb, dir, "base", tools("a")) }); len(errs) != 2 {
		t.Fatalf("removed tool (index and stale file): %v", errs)
	}
	if errs := check(t, func(tb testing.TB) {
		CheckVariant(tb, dir, "variant", "base", tools("a", "c"), tools("a", "z", "c"))
	}); len(errs) != 1 || !strings.Contains(errs[0], "variant.diff.golden") {
		t.Fatalf("variant order: %v", errs)
	}
	if errs := check(t, func(tb testing.TB) {
		CheckVariant(tb, dir, "variant", "base", tools("a", "c"), tools("a", "c", "z"))
	}); len(errs) != 0 {
		t.Fatalf("unchanged variant: %v", errs)
	}
}

// A tool name that is no safe file name, or two names one case-insensitive file system would mix up, stop the test.
func TestToolNamesMustBeFileNames(t *testing.T) {
	for _, list := range [][]proto.Tool{tools("../x"), tools(".hidden"), tools(""), tools("a", "A"), tools("a", "a")} {
		if _, err := entriesOf(list); err == nil {
			t.Errorf("%v: accepted", list)
		}
	}
}
