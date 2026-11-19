package timeline

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/jsonsubset"
	"github.com/nwverse/persona/server/internal/proto"
)

// The shared to-do rounds cases (spec/fixtures/todo-rounds, #10465): the timeline answers every one exactly, compared
// as JSON values, and what it writes holds to schema/wire/todo-view.schema.json.

const fixtures = "../../../../../spec/fixtures/"

// caseInput is a case's input: the to-do as GET /api/v1/todos/{id} lists it, and its stores' rows.
type caseInput struct {
	Todo      json.RawMessage   `json:"todo"`
	Feed      []FeedItem        `json:"feed"`
	Decisions []Decision        `json:"decisions"`
	Sessions  []proto.Session   `json:"sessions"`
	Machines  map[string]string `json:"machines"`
	Loc       string            `json:"loc"`
}

type fixtureCase struct {
	Name     string    `json:"name"`
	Input    caseInput `json:"input"`
	Expected struct {
		Timeline json.RawMessage `json:"timeline"`
		Results  json.RawMessage `json:"results"`
		Recap    json.RawMessage `json:"recap"`
		Latest   []string        `json:"latest"`
	} `json:"expected"`
}

func readCases(t *testing.T) []fixtureCase {
	t.Helper()
	data, err := os.ReadFile(fixtures + "todo-rounds/todo_rounds.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int           `json:"version"`
		Cases   []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != 1 || len(doc.Cases) == 0 {
		t.Fatalf("todo_rounds.json: %v (version %d, %d cases)", err, doc.Version, len(doc.Cases))
	}
	return doc.Cases
}

// input is a case's Input: its to-do decoded as the manager's row decodes, refusing nothing (a row carries many fields
// the timeline does not read), in the case's zone.
func input(t *testing.T, c caseInput) Input {
	t.Helper()
	in := Input{Feed: c.Feed, Decisions: c.Decisions, Sessions: c.Sessions, Machines: c.Machines}
	if err := json.Unmarshal(c.Todo, &in.Todo); err != nil {
		t.Fatal(err)
	}
	if c.Loc != "" {
		loc, err := time.LoadLocation(c.Loc)
		if err != nil {
			t.Fatal(err)
		}
		in.Loc = loc
	}
	return in
}

// written is what a case derives, as the cases write it: the timeline, the results (absent when none) and the recap
// (absent when none).
func written(t *testing.T, d Derived) (timeline, results, recap []byte) {
	t.Helper()
	var err error
	if timeline, err = json.Marshal(assistantwire.TodoTimeline{Rounds: d.Rounds}); err != nil {
		t.Fatal(err)
	}
	if len(d.Results) > 0 {
		if results, err = json.Marshal(d.Results); err != nil {
			t.Fatal(err)
		}
	}
	if d.Recap != nil {
		if recap, err = json.Marshal(d.Recap); err != nil {
			t.Fatal(err)
		}
	}
	return timeline, results, recap
}

// sameJSON fails unless got is the same JSON value as want (both absent counts as the same).
func sameJSON(t *testing.T, what string, got []byte, want json.RawMessage) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Errorf("%s:\n got  %s\n want %s", what, got, want)
		}
		return
	}
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got  %s\n want %s", what, got, compact(t, want))
	}
}

func compact(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return out
}

// viewSchema is schema/wire/todo-view.schema.json, compiled once.
var viewSchema = sync.OnceValues(func() (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "schema", "wire", "todo-view.schema.json"))
	if err != nil {
		return nil, err
	}
	return jsonsubset.Compile(data)
})

// holdsTo says what in raw does not hold to the schema's definition def, "" when it all does.
func holdsTo(t *testing.T, def string, raw []byte) string {
	t.Helper()
	root, err := viewSchema()
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonsubset.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return jsonsubset.ValidateAt(root, root["$defs"].(map[string]any)[def].(map[string]any), v, "$", 0)
}

func TestTimelineFollowsTheCases(t *testing.T) {
	for _, c := range readCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			d := Derive(input(t, c.Input))
			timeline, results, recap := written(t, d)
			sameJSON(t, "timeline", timeline, c.Expected.Timeline)
			sameJSON(t, "results", results, c.Expected.Results)
			sameJSON(t, "recap", recap, c.Expected.Recap)
			var latest []string
			for _, rd := range Latest(d.Rounds, PaperRounds) {
				latest = append(latest, rd.ID)
			}
			if !slices.Equal(latest, c.Expected.Latest) {
				t.Errorf("the paper's rounds %v, want %v", latest, c.Expected.Latest)
			}
			if p := holdsTo(t, "TodoTimeline", timeline); p != "" {
				t.Errorf("the timeline does not hold to the schema: %s", p)
			}
			if recap != nil {
				if p := holdsTo(t, "TodoRecap", recap); p != "" {
					t.Errorf("the recap does not hold to the schema: %s", p)
				}
			}
			var each []json.RawMessage
			if results != nil {
				if err := json.Unmarshal(results, &each); err != nil {
					t.Fatal(err)
				}
			}
			if len(each) > maxResults {
				t.Errorf("%d results, at most %d", len(each), maxResults)
			}
			for _, r := range each {
				if p := holdsTo(t, "TodoResult", r); p != "" {
					t.Errorf("result %s does not hold to the schema: %s", r, p)
				}
			}
		})
	}
}

// The stores' rows come in no particular order: read in another one, every case derives the same bytes.
func TestTimelineReadsRowsInAnyOrder(t *testing.T) {
	for _, c := range readCases(t) {
		in := input(t, c.Input)
		a, b, d := written(t, Derive(in))
		in.Feed, in.Decisions = slices.Clone(in.Feed), slices.Clone(in.Decisions)
		in.Sessions, in.Todo.Agents = slices.Clone(in.Sessions), slices.Clone(in.Todo.Agents)
		slices.Reverse(in.Feed)
		slices.Reverse(in.Decisions)
		slices.Reverse(in.Sessions)
		slices.Reverse(in.Todo.Agents)
		if v := in.Todo.Supervision; v != nil {
			v.Reviews = slices.Clone(v.Reviews)
			slices.Reverse(v.Reviews)
		}
		x, y, z := written(t, Derive(in))
		if string(a) != string(x) || string(b) != string(y) || string(d) != string(z) {
			t.Errorf("%s reads differently with its rows reversed:\n %s %s %s\n %s %s %s", c.Name, a, b, d, x, y, z)
		}
	}
}

// Together the cases show every entry type and round kind, and only values of the schema's enums: a client drawn
// from them meets everything the hub sends.
func TestTimelineCasesCoverTheSchema(t *testing.T) {
	types, kinds, actors := map[string]bool{}, map[string]bool{}, map[string]bool{}
	check := func(where string, e assistantwire.TodoTimelineEntry) {
		types[e.Type], actors[e.Actor.Kind] = true, true
		if e.Via != "" && !slices.Contains(assistantwire.TodoEntryVias, e.Via) {
			t.Errorf("%s: via %s", where, e.Via)
		}
		if e.State != "" && e.Type != assistantwire.TodoEntryTypeInstruction {
			t.Errorf("%s: a state on a %s", where, e.Type)
		}
	}
	for _, c := range readCases(t) {
		var tl assistantwire.TodoTimeline
		if err := json.Unmarshal(c.Expected.Timeline, &tl); err != nil {
			t.Fatal(err)
		}
		for _, rd := range tl.Rounds {
			kinds[rd.Kind] = true
			if rd.Header != nil {
				check(c.Name+"/"+rd.ID, *rd.Header)
			}
			for _, e := range rd.Entries {
				check(c.Name+"/"+e.ID, e)
			}
		}
	}
	for _, typ := range assistantwire.TodoEntryTypes {
		if !types[typ] {
			t.Errorf("no case shows an entry of type %s", typ)
		}
		delete(types, typ)
	}
	for _, kind := range assistantwire.TodoRoundKinds {
		if !kinds[kind] {
			t.Errorf("no case shows a %s round", kind)
		}
		delete(kinds, kind)
	}
	for kind := range actors {
		if !slices.Contains(assistantwire.TodoActorKinds, kind) {
			t.Errorf("actor %s is no TodoActorKind", kind)
		}
	}
	if len(types) > 0 || len(kinds) > 0 {
		t.Errorf("values the schema does not list: %v %v", types, kinds)
	}
}

// Every line key the timeline writes is in spec/fixtures/todo-rounds/README.md's "Line keys" table and every key there
// is one it writes; the cases use no other.
func TestTimelineLineKeysAreDocumented(t *testing.T) {
	keys, documented := sourceLineKeys(t), readmeLineKeys(t)
	slices.Sort(keys)
	slices.Sort(documented)
	if !slices.Equal(keys, documented) {
		t.Errorf("line keys written %v, documented in spec/fixtures/todo-rounds/README.md %v", keys, documented)
	}
	for _, c := range readCases(t) {
		var tl assistantwire.TodoTimeline
		if err := json.Unmarshal(c.Expected.Timeline, &tl); err != nil {
			t.Fatal(err)
		}
		for _, rd := range tl.Rounds {
			for _, e := range append(slices.Clone(rd.Entries), headers(rd)...) {
				if e.Line != nil && !slices.Contains(keys, e.Line.Key) {
					t.Errorf("%s/%s: line key %s", c.Name, e.ID, e.Line.Key)
				}
			}
		}
	}
}

func headers(rd assistantwire.TodoRound) []assistantwire.TodoTimelineEntry {
	if rd.Header == nil {
		return nil
	}
	return []assistantwire.TodoTimelineEntry{*rd.Header}
}

// sourceLineKeys are the values of the package's line* constants: every line key the timeline writes.
func sourceLineKeys(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i >= len(vs.Values) || !strings.HasPrefix(n.Name, "line") {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
						v, _ := strconv.Unquote(lit.Value)
						out = append(out, v)
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no line* constants found")
	}
	return out
}

// readmeLineKeys are the keys of the "### Line keys" table of spec/fixtures/todo-rounds/README.md.
func readmeLineKeys(t *testing.T) []string {
	t.Helper()
	readme, err := os.ReadFile(fixtures + "todo-rounds/README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(readme), "\n### Line keys\n")
	if !ok {
		t.Fatal(`spec/fixtures/todo-rounds/README.md has no "### Line keys"`)
	}
	if end := strings.Index(section, "\n#"); end >= 0 {
		section = section[:end]
	}
	var out []string
	for l := range strings.Lines(section) {
		if cell, ok := strings.CutPrefix(l, "| `"); ok {
			if key, _, ok := strings.Cut(cell, "`"); ok {
				out = append(out, key)
			}
		}
	}
	return out
}

// The timeline stays pure (docs/design/proposals/todos-redesign.md 7.1), as the projection does: no IO and no clock,
// and nothing of the manager, so the same rows always give the same timeline. The standard library it may use is
// listed, so a package that reads the disk, the network or randomness never gets in unnoticed.
func TestTimelineStaysPure(t *testing.T) {
	allowed := []string{"cmp", "encoding/json", "maps", "slices", "strconv", "strings", "time",
		"github.com/nwverse/persona/server/internal/proto", "github.com/nwverse/persona/server/internal/assistantwire"}
	clock := []string{"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker", "LoadLocation"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); !slices.Contains(allowed, path) {
				t.Errorf("%s imports %s: the timeline reads only its rows (the listed standard library, proto and "+
					"assistantwire)", name, path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && slices.Contains(clock, sel.Sel.Name) {
					t.Errorf("%s: time.%s: the timeline has no clock", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}
