package assistantwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/jsonsubset"
)

// mergedPlaces is a TodoPlaces as the hub answers it after merging several managers' places: the only writer of
// TodoPlace.manager, which no fixture carries (they hold one manager's answer).
const mergedPlaces = `{"places":[{"id":"all","kind":"all","title":"","needs_you":1},` +
	`{"id":"today","kind":"today","title":"","needs_you":0},{"id":"upcoming","kind":"upcoming","title":"","needs_you":0},` +
	`{"id":"project:garden","kind":"project","title":"Garden","project":"garden","needs_you":1,"manager":"work"}],` +
	`"counts":{"open":2,"needs_you":1,"in_progress":0,"sections":{"needs_you":1,"can_wait":0,"working":0,` +
	`"waiting":0,"todo":1,"just_finished":0,"someday":0,"done":0}}}`

// The views, counts and places the shared to-do cases expect (spec/fixtures/todo-view and todo-counts, #10226), and
// the timelines, results and recaps (spec/fixtures/todo-rounds, #10465), read into the generated types
// (todo_view_gen.go) and write back as the same JSON: no field lost, renamed or added, and no time written another
// way. What Go writes also holds to the schema (checkTodoView), so a required list or map is never null, which a nil
// slice or map would be.
func TestTodoViewSamplesRoundTrip(t *testing.T) {
	root := todoViewSchema(t)
	samples := append(todoViewSamples(t),
		todoViewSample{def: "TodoPlaces", where: "merged places", raw: json.RawMessage(mergedPlaces)})
	seen := map[string]bool{}
	for _, s := range samples {
		v := todoViewDefs[s.def]()
		d := json.NewDecoder(bytes.NewReader(s.raw))
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			t.Fatalf("%s: %v", s.where, err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", s.where, err)
		}
		var want, got any
		if err := json.Unmarshal(s.raw, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: written back differently\n want %s\n got  %s", s.where, s.raw, out)
		}
		if p := checkTodoView(t, root, s.def, out); p != "" {
			t.Errorf("%s: %s", s.where, p)
		}
		seen[s.def] = true
	}
	for _, def := range []string{"TodoSummary", "TodoCounts", "TodoPlaces", "TodoTimeline", "TodoResult", "TodoRecap"} {
		if !seen[def] {
			t.Errorf("no %s sample", def)
		}
	}
}

// A nil slice or map writes null, which the schema refuses for the required places and sections: whoever builds one
// (the projection, the hub's merge) allocates them, and checks what it writes the same way.
func TestTodoViewNilListsAreCaught(t *testing.T) {
	root := todoViewSchema(t)
	sections := map[string]int{}
	for _, s := range TodoListSections {
		sections[s] = 0
	}
	for _, c := range []struct {
		def  string
		v    any
		want string
	}{
		{"TodoCounts", TodoCounts{}, "$.sections: should be object, is null"},
		{"TodoPlaces", TodoPlaces{Counts: TodoCounts{Sections: sections}}, "$.places: should be array, is null"},
		{"TodoCounts", TodoCounts{Sections: map[string]int{"needs_you": 0}}, `$.sections: missing required "can_wait"`},
	} {
		out, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if p := checkTodoView(t, root, c.def, out); p != c.want {
			t.Errorf("%s %s: %q, want %q", c.def, out, p, c.want)
		}
	}
}

// checkTodoView says what in raw does not hold to the schema's definition def, "" when it all does. The projection's
// and the hub's tests can make the same check with internal/jsonsubset.
func checkTodoView(t *testing.T, root map[string]any, def string, raw []byte) string {
	t.Helper()
	v, err := jsonsubset.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := root["$defs"].(map[string]any)[def].(map[string]any)
	if schema == nil {
		t.Fatalf("no definition %s", def)
	}
	return jsonsubset.ValidateAt(root, schema, v, "$", 0)
}

type todoViewSample struct {
	def, where string
	raw        json.RawMessage
}

// todoViewSamples are the expected values of every todo-view, todo-counts and todo-rounds case, with the definition
// each holds to.
func todoViewSamples(t *testing.T) []todoViewSample {
	t.Helper()
	var out []todoViewSample
	for _, domain := range []string{"todo-view", "todo-counts", "todo-rounds"} {
		files, err := filepath.Glob(filepath.Join("..", "..", "..", "spec", "fixtures", domain, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("spec/fixtures/%s: %v", domain, err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Cases []struct {
					Name     string
					Expected struct {
						View     json.RawMessage   `json:"view"`
						Counts   json.RawMessage   `json:"counts"`
						Places   json.RawMessage   `json:"places"`
						Timeline json.RawMessage   `json:"timeline"`
						Results  []json.RawMessage `json:"results"`
						Recap    json.RawMessage   `json:"recap"`
					}
				}
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			for _, c := range doc.Cases {
				where := fmt.Sprintf("%s/%s %s", domain, filepath.Base(f), c.Name)
				for def, raw := range map[string]json.RawMessage{"TodoSummary": c.Expected.View,
					"TodoCounts": c.Expected.Counts, "TodoPlaces": c.Expected.Places,
					"TodoTimeline": c.Expected.Timeline, "TodoRecap": c.Expected.Recap} {
					if raw != nil {
						out = append(out, todoViewSample{def: def, where: where + " " + def, raw: raw})
					}
				}
				for i, raw := range c.Expected.Results {
					out = append(out, todoViewSample{def: "TodoResult", where: fmt.Sprintf("%s TodoResult %d", where, i),
						raw: raw})
				}
			}
		}
	}
	return out
}

// todoViewSchema is schema/wire/todo-view.schema.json, compiled for jsonsubset.
func todoViewSchema(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "wire", "todo-view.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := jsonsubset.Compile(data)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
