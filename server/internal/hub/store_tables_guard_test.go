package hub

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// The migrations are a hub set and a manager set (docs/server/server.md "Migrations"): a hub's database has only the hub's
// tables, and the default manager is to get a database of its own. So what the hub asks of its store must only touch
// the hub's tables, and what the managers ask of theirs only the manager's. These read every interface a side
// declares, take the methods *db.DB serves of them, and look at the SQL those methods (and the db functions they
// call) run.

var createTableRE = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*)`)

// extractCreateTables returns sorted, unique lowercase table names declared by CREATE TABLE statements in sql.
func extractCreateTables(sql string) []string {
	var out []string
	for _, m := range createTableRE.FindAllStringSubmatch(sql, -1) {
		if tb := strings.ToLower(m[1]); !slices.Contains(out, tb) {
			out = append(out, tb)
		}
	}
	sort.Strings(out)
	return out
}

// setTables are the tables migrations/<part> makes.
func setTables(t *testing.T, part string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "db", "migrations", part, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations/%s: %v", part, err)
	}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range extractCreateTables(string(b)) {
			if !slices.Contains(out, tb) {
				out = append(out, tb)
			}
		}
	}
	sort.Strings(out)
	return out
}

// dbFuncs are internal/db's functions and *DB's methods by name, each with the string literals in its body, the
// names it calls and the names it reads; and the package's constants as "const." and their name, each with the
// string literals of its value and the names it reads. So SQL kept in a constant (insertMetricSQL) counts for the
// functions that use it.
type dbFunc struct {
	strs  []string
	calls []string
	names []string
}

func dbFuncs(t *testing.T) map[string]dbFunc {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "db", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	scan := func(node ast.Node) dbFunc {
		var fn dbFunc
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil {
						fn.strs = append(fn.strs, s)
					}
				}
			case *ast.Ident:
				fn.names = append(fn.names, n.Name)
			case *ast.CallExpr:
				switch c := n.Fun.(type) {
				case *ast.Ident:
					fn.calls = append(fn.calls, c.Name)
				case *ast.SelectorExpr:
					fn.calls = append(fn.calls, c.Sel.Name)
				}
			}
			return true
		})
		return fn
	}
	out := map[string]dbFunc{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Body == nil {
					continue
				}
				key := decl.Name.Name
				if decl.Recv != nil {
					key = "DB." + key
				}
				out[key] = scan(decl.Body)
			case *ast.GenDecl:
				if decl.Tok != token.CONST {
					continue
				}
				for _, spec := range decl.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, id := range vs.Names {
						if i < len(vs.Values) {
							out["const."+id.Name] = scan(vs.Values[i])
						}
					}
				}
			}
		}
	}
	return out
}

// tablesTouched are the tables of all the SQL method m of *DB runs, the db functions it calls and the constants they
// read included.
func tablesTouched(funcs map[string]dbFunc, m string, all []string) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(key string)
	walk = func(key string) {
		// The side's own settings table, by how the database was opened; and the copy of the hub's keys kept for a hub
		// build from before hub_settings on a database shared with the default manager (db.go keepForOlderHub).
		if seen[key] || key == "DB.settingsTable" || key == "DB.keepForOlderHub" || key == "DB.sharedWithOlderHub" {
			return
		}
		seen[key] = true
		fn, ok := funcs[key]
		if !ok {
			return
		}
		for _, s := range fn.strs {
			for _, tb := range all {
				if regexp.MustCompile(`\b`+tb+`\b`).MatchString(s) && !slices.Contains(out, tb) {
					out = append(out, tb)
				}
			}
		}
		for _, c := range fn.calls {
			walk("DB." + c)
			walk(c)
		}
		for _, n := range fn.names {
			walk("const." + n)
		}
	}
	walk("DB." + m)
	sort.Strings(out)
	return out
}

// storeMethods are the methods of *db.DB that the interfaces declared in the non-test files of dir ask for.
func storeMethods(t *testing.T, dir string) []string {
	t.Helper()
	served := map[string]bool{}
	typ := reflect.TypeFor[*db.DB]()
	for i := range typ.NumMethod() {
		served[typ.Method(i).Name] = true
	}
	delete(served, "Close")
	delete(served, "CloseContext")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			it, ok := n.(*ast.InterfaceType)
			if !ok {
				return true
			}
			for _, field := range it.Methods.List {
				for _, id := range field.Names {
					if served[id.Name] && !slices.Contains(out, id.Name) {
						out = append(out, id.Name)
					}
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// Regression test for #12846 / #12833: table names with digits (such as a2a_tasks) must be parsed intact rather than
// truncated at the first digit into single-letter names that cause false positives on SQL aliases or English words.
func TestCreateTableREParsesDigitTableNames(t *testing.T) {
	sql := `
		CREATE TABLE a2a_tasks (id text PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS v2_items_3 (id int);
		create table OAuth2_Tokens (id text);
		CREATE TABLE a2a_tasks (id text);
	`
	got := extractCreateTables(sql)
	want := []string{"a2a_tasks", "oauth2_tokens", "v2_items_3"}
	if !slices.Equal(got, want) {
		t.Fatalf("extractCreateTables() = %v, want %v", got, want)
	}

	managerTables := setTables(t, "manager")
	if !slices.Contains(managerTables, "a2a_tasks") {
		t.Fatalf("manager tables %v do not contain a2a_tasks", managerTables)
	}
	if slices.Contains(managerTables, "a") {
		t.Fatalf("manager tables %v contain truncated single-letter table name \"a\"", managerTables)
	}

	funcs := map[string]dbFunc{
		"DB.HubQueryWithAliasA": {
			strs: []string{
				"SELECT 1 FROM share_machine_authorities a WHERE a.id = $1",
				"phone write arguments must be a JSON object of at most 64 KiB",
			},
		},
		"DB.ManagerQueryA2ATasks": {
			strs: []string{
				"SELECT id FROM a2a_tasks WHERE state = $1",
			},
		},
	}
	all := append([]string{"share_machine_authorities"}, got...)
	if touched := tablesTouched(funcs, "HubQueryWithAliasA", all); !slices.Equal(touched, []string{"share_machine_authorities"}) {
		t.Fatalf("HubQueryWithAliasA touched %v, want [share_machine_authorities]", touched)
	}
	if touched := tablesTouched(funcs, "ManagerQueryA2ATasks", all); !slices.Equal(touched, []string{"a2a_tasks"}) {
		t.Fatalf("ManagerQueryA2ATasks touched %v, want [a2a_tasks]", touched)
	}
}

// The hub's store touches only the hub's tables; the managers' stores only the manager's.
func TestStoresTouchTheirOwnTables(t *testing.T) {
	hubTables, managerTables := setTables(t, "hub"), setTables(t, "manager")
	all := append(slices.Clone(hubTables), managerTables...)
	funcs := dbFuncs(t)
	// InsertMetric's SQL is a constant (insertMetricSQL): a scan that misses constants sees no table at all.
	if got := tablesTouched(funcs, "InsertMetric", all); !slices.Equal(got, []string{"machine_metrics"}) {
		t.Fatalf("InsertMetric touches %v, want [machine_metrics]: the scan misses SQL kept in a constant", got)
	}
	for _, side := range []struct {
		dirs   []string
		tables []string
		set    string
	}{
		{[]string{"."}, hubTables, "hub"},
		{[]string{"../assistant", "../devices", "../extmgr", "../extension", "../mail", "../agents"}, managerTables, "manager"},
	} {
		for _, dir := range side.dirs {
			for _, m := range storeMethods(t, dir) {
				for _, tb := range tablesTouched(funcs, m, all) {
					if !slices.Contains(side.tables, tb) {
						t.Errorf("internal/%s asks its store for %s, which touches %s: not a table of the %s set "+
							"(internal/db/migrations/%s). Get it over the bus from the side that owns it, or move the "+
							"table; docs/server/server.md \"Migrations\"", strings.TrimPrefix(filepath.Clean(filepath.Join("hub", dir)), "hub/"),
							m, tb, side.set, side.set)
					}
				}
			}
		}
	}
}

// settingKeys are the keys the non-test files of dir pass to Setting / PutSetting / TakeSetting / CompareSetting as a
// string literal or a constant of the package (keys built at run time are not seen).
func settingKeys(t *testing.T, dir string) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	consts := map[string]string{}
	var parsed []*ast.File
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[id.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
	}
	out := map[string]string{}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !slices.Contains([]string{"Setting", "PutSetting", "TakeSetting", "CompareSetting"}, sel.Sel.Name) {
				return true
			}
			pos := fset.Position(call.Pos())
			where := filepath.Base(pos.Filename) + ":" + strconv.Itoa(pos.Line)
			switch k := call.Args[1].(type) {
			case *ast.BasicLit:
				if v, err := strconv.Unquote(k.Value); err == nil {
					out[v] = where
				}
			case *ast.Ident:
				if v, ok := consts[k.Name]; ok {
					out[v] = where
				}
			}
			return true
		})
	}
	return out
}

// Each side keeps its own settings (hub_settings, a manager's settings): the hub uses no key a manager does, so it
// never reads what a manager wrote (it gets that over the bus), and the other way round.
func TestSettingsKeysAreOneSidesOwn(t *testing.T) {
	hub := settingKeys(t, ".")
	if len(hub) < 10 {
		t.Fatalf("found only %d of the hub's setting keys: the scan is broken", len(hub))
	}
	for _, dir := range []string{"../assistant", "../devices", "../extmgr", "../extension", "../mail", "../agents"} {
		for k, where := range settingKeys(t, dir) {
			if at, ok := hub[k]; ok {
				t.Errorf("setting %q is used by the hub (%s) and by internal/%s (%s): a key is one side's; hand the value "+
					"over the bus instead; docs/server/server.md \"Migrations\"", k, at, filepath.Base(dir), where)
			}
		}
	}
}
