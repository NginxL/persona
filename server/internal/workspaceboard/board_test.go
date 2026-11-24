package workspaceboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func text(s string) *string { return &s }
func flag(b bool) *bool     { return &b }
func fixture() State {
	s := Default()
	s.Revision = 7
	for i, col := range []string{"todo", "todo", "doing", "done"} {
		s.Cards = append(s.Cards, Card{ID: fmt.Sprint(i), MachineID: "machine-a", WorkspaceID: fmt.Sprint("space-", i), Title: fmt.Sprint("Project ", i), Cwd: fmt.Sprint("/projects/", i), Source: "directory", ColumnID: col})
	}
	return s
}
func apply(t *testing.T, s State, m Mutation) State {
	t.Helper()
	next, err := Apply(s, m)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func cardIDs(s State) []string {
	ids := []string{}
	for _, c := range s.Cards {
		ids = append(ids, c.ID)
	}
	return ids
}
func columnIDs(s State) []string {
	ids := []string{}
	for _, c := range s.Columns {
		ids = append(ids, c.ID)
	}
	return ids
}
func TestDefaultIndependent(t *testing.T) {
	a, b := Default(), Default()
	a.Columns[0].Title = "changed"
	if b.Columns[0].Title != "待办" || b.Revision != 0 {
		t.Fatalf("shared default: %+v", b)
	}
	data, _ := json.Marshal(b)
	if !strings.Contains(string(data), `"cards":[]`) {
		t.Fatalf("cards must be array: %s", data)
	}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryBindingIdentityUsesRemotePlatform(t *testing.T) {
	for _, tc := range []struct {
		first, second string
		duplicate     bool
	}{
		{`C:\Projects\Example`, `c:/projects/example`, true},
		{`\\Server\Share\Example`, `//server/share/EXAMPLE`, true},
		{"/projects/Example", "/projects/example", false},
		{"/:Project", "/:project", false},
	} {
		s := Default()
		s.Cards = []Card{{ID: "existing", MachineID: "computer", WorkspaceID: "existing", Title: "Example", Cwd: tc.first, Source: "directory", ColumnID: "todo"}}
		_, err := Apply(s, Mutation{Action: "add_card", ID: "new", MachineID: "computer", WorkspaceID: "new", Title: text("Example"), Cwd: tc.second, Source: "directory", ColumnID: "todo"})
		if (err != nil) != tc.duplicate {
			t.Fatalf("binding %q and %q: %v", tc.first, tc.second, err)
		}
		both := Default()
		both.Cards = []Card{
			{ID: "existing", MachineID: "computer", WorkspaceID: "existing", Title: "Example", Cwd: tc.first, Source: "directory", ColumnID: "todo"},
			{ID: "new", MachineID: "computer", WorkspaceID: "new", Title: "Example", Cwd: tc.second, Source: "directory", ColumnID: "todo"},
		}
		if err := Validate(both); errors.Is(err, ErrInvalid) != tc.duplicate || (err != nil && !tc.duplicate) {
			t.Fatalf("Validate(%q, %q) = %v, duplicate=%v", tc.first, tc.second, err, tc.duplicate)
		}
	}
}
func TestMoveOrderAndInput(t *testing.T) {
	original := fixture()
	before, _ := json.Marshal(original)
	next := apply(t, original, Mutation{Action: "move_card", ID: "2", ColumnID: "todo", BeforeID: "1"})
	if !reflect.DeepEqual(cardIDs(next), []string{"0", "2", "1", "3"}) || next.Cards[1].ColumnID != "todo" || next.Revision != 7 {
		t.Fatalf("move: %+v", next)
	}
	next = apply(t, next, Mutation{Action: "move_card", ID: "0", ColumnID: "done"})
	if !reflect.DeepEqual(cardIDs(next), []string{"2", "1", "3", "0"}) {
		t.Fatalf("append: %v", cardIDs(next))
	}
	next = apply(t, next, Mutation{Action: "move_column", ID: "done", BeforeID: "todo"})
	if !reflect.DeepEqual(columnIDs(next), []string{"done", "todo", "doing", "review"}) {
		t.Fatalf("columns: %v", columnIDs(next))
	}
	next.Columns[0].Title = "changed"
	next.Cards[0].Title = "changed"
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("caller mutated")
	}
	same := apply(t, original, Mutation{Action: "move_card", ID: "0", ColumnID: "todo", BeforeID: "0"})
	same.Cards[0].Title = "other"
	if original.Cards[0].Title == "other" {
		t.Fatal("no-op retained caller slice")
	}
	next = apply(t, original, Mutation{Action: "move_column", ID: "todo"})
	if !reflect.DeepEqual(columnIDs(next), []string{"doing", "review", "done", "todo"}) {
		t.Fatal(columnIDs(next))
	}
}
func TestArchiveRestoreAndPinMetadata(t *testing.T) {
	original := fixture()
	next := apply(t, original, Mutation{Action: "update_card", ID: "1", Title: text(" Renamed "), Pinned: flag(true), Archived: flag(true), MachineID: "ignored", Cwd: "/ignored", ColumnID: "done"})
	want := original.Cards[1]
	want.Title = "Renamed"
	want.Pinned = true
	want.Archived = true
	if !reflect.DeepEqual(next.Cards[1], want) || !reflect.DeepEqual(cardIDs(next), cardIDs(original)) {
		t.Fatalf("update: %+v", next)
	}
	next = apply(t, next, Mutation{Action: "update_card", ID: "1", Pinned: flag(false), Archived: flag(false)})
	if next.Cards[1].Pinned || next.Cards[1].Archived || next.Cards[1].Title != "Renamed" || original.Cards[1].Archived {
		t.Fatalf("restore or alias: %+v", next)
	}
}
func TestDeleteColumnNeverLosesCards(t *testing.T) {
	original := fixture()
	original.Cards[0].Archived = true
	if _, err := Apply(original, Mutation{Action: "delete_column", ID: "todo"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonempty: %v", err)
	}
	next := apply(t, original, Mutation{Action: "delete_column", ID: "todo", MoveTo: "review"})
	if len(next.Cards) != len(original.Cards) || !reflect.DeepEqual(cardIDs(next), cardIDs(original)) || next.Cards[0].ColumnID != "review" || next.Cards[1].ColumnID != "review" || !next.Cards[0].Archived || original.Cards[0].ColumnID != "todo" {
		t.Fatalf("lost, reordered or aliased: %+v", next)
	}
	next = apply(t, next, Mutation{Action: "delete_column", ID: "done", MoveTo: "doing"})
	next = apply(t, next, Mutation{Action: "delete_column", ID: "review", MoveTo: "doing"})
	if _, err := Apply(next, Mutation{Action: "delete_column", ID: "doing"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("last column: %v", err)
	}
}
func TestAddRenameAndDuplicateBinding(t *testing.T) {
	s := apply(t, Default(), Mutation{Action: "add_column", ID: "blocked", Title: text(" 阻塞 ")})
	s = apply(t, s, Mutation{Action: "update_column", ID: "blocked", Title: text("暂停")})
	m := Mutation{Action: "add_card", ID: "new", Title: text("Project"), ColumnID: "blocked", MachineID: "machine-a", WorkspaceID: "space-a", Cwd: "/projects/one/", Source: "directory", SourceID: "space-a", Branch: "main"}
	s = apply(t, s, m)
	c := s.Cards[0]
	if c.Cwd != "/projects/one" || c.SourceID != "space-a" || c.Branch != "main" || s.Columns[4].Title != "暂停" {
		t.Fatalf("add: %+v", s)
	}
	s.Cards[0].Archived = true
	m.ID = "another"
	m.WorkspaceID = "other"
	m.Cwd = "/projects/two/../one"
	if _, err := Apply(s, m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate: %v", err)
	}
	m.MachineID = "machine-b"
	if _, err := Apply(s, m); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidMutationsPreserveInput(t *testing.T) {
	for _, m := range []Mutation{{Action: "move_card", ID: "0", ColumnID: "todo", BeforeID: "2"}, {Action: "move_card", ID: "0", ColumnID: "missing"}, {Action: "move_card", ID: "missing", ColumnID: "todo"}, {Action: "move_card", ID: "0", ColumnID: "todo", BeforeID: "missing"}, {Action: "move_column", ID: "todo", BeforeID: "missing"}, {Action: "delete_column", ID: "todo", MoveTo: "todo"}, {Action: "delete_column", ID: "todo", MoveTo: "missing"}, {Action: "update_card", ID: "0", Title: text(" ")}, {Action: "update_column", ID: "todo", Title: text(strings.Repeat("界", MaxTitleRunes+1))}, {Action: "add_column", ID: "todo", Title: text("Duplicate")}, {Action: "add_column", ID: "bad\n", Title: text("Invalid")}, {Action: "update_column", ID: "todo"}, {Action: "anything"}} {
		t.Run(m.Action+"-"+m.ID+"-"+m.BeforeID+"-"+m.MoveTo, func(t *testing.T) {
			s := fixture()
			before, _ := json.Marshal(s)
			if _, err := Apply(s, m); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want invalid: %v", err)
			}
			after, _ := json.Marshal(s)
			if string(before) != string(after) {
				t.Fatal("failed operation mutated input")
			}
		})
	}
}
func TestValidateBoundsAndCorruption(t *testing.T) {
	cases := map[string]func(*State){"revision": func(s *State) { s.Revision = -1 }, "no columns": func(s *State) { s.Columns = nil }, "missing column": func(s *State) { s.Cards[0].ColumnID = "missing" }, "duplicate card": func(s *State) { s.Cards[1].ID = s.Cards[0].ID }, "duplicate cwd": func(s *State) { s.Cards[1].Cwd = s.Cards[0].Cwd + "/" }, "relative cwd": func(s *State) { s.Cards[0].Cwd = "relative" }, "nul cwd": func(s *State) { s.Cards[0].Cwd = "/a\x00b" }, "long cwd": func(s *State) { s.Cards[0].Cwd = "/" + strings.Repeat("a", MaxPathBytes) }, "long title": func(s *State) { s.Cards[0].Title = strings.Repeat("界", MaxTitleRunes+1) }, "too many columns": func(s *State) {
		for i := len(s.Columns); i <= MaxColumns; i++ {
			s.Columns = append(s.Columns, Column{fmt.Sprint(i), "Column"})
		}
	}, "too many cards": func(s *State) {
		for i := len(s.Cards); i <= MaxCards; i++ {
			c := s.Cards[0]
			c.ID = fmt.Sprint(i)
			c.Cwd = fmt.Sprint("/many/", i)
			s.Cards = append(s.Cards, c)
		}
	}}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			corrupt(&s)
			if err := Validate(s); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
}
func TestRemoteWindowsAndUnixPaths(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{`C:\repo\one\..\two\`, `c:/repo/two`}, {`c:/repo/two`, `c:/repo/two`}, {`\\Server\Share\repo\..\two`, `//server/share/two`}, {`/unix/back\slash`, `/unix/back\slash`}, {`C:\`, `c:/`}} {
		got, err := CanonicalCwd(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("%q = %q, %v want %q", tc.in, got, err, tc.want)
		}
	}
	for _, p := range []string{`C:repo`, `C:`, `\repo`, `\\server`, `relative`} {
		if _, err := CanonicalCwd(p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted relative %q: %v", p, err)
		}
	}
	s := fixture()
	s.Cards[0].Cwd = `C:\repo`
	s.Cards[1].Cwd = `c:/repo`
	if err := Validate(s); !errors.Is(err, ErrInvalid) {
		t.Fatalf("windows duplicate: %v", err)
	}
	s.Cards[1].MachineID = "machine-b"
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
}
