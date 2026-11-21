package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hub and the built-in manager are the same database when they name the
// same database on any common server: pgx tries every host of a multi-host
// URL, so a shared fallback is enough to reach the same one.
func TestSameDatabaseComparesEveryHost(t *testing.T) {
	for _, c := range []struct {
		hub, manager string
		same         bool
	}{
		{"postgres://u@a,b/persona", "postgres://u@a,c/persona", true},
		{"postgres://u@b,a/persona", "postgres://u@c,a/persona", true},
		{"postgres://u@a:5432,b:5433/persona", "postgres://u@c:5432,B:5433/persona", true},
		{"postgres://u@a/persona", "postgres://u@a:5432/persona", true},
		{"postgres://u@/persona?host=/run/pg", "postgres://u@/persona?host=/tmp/other,/run/pg", true},
		{"postgres://u@a,b/persona", "postgres://u@c,d/persona", false},
		{"postgres://u@a:5432,b:5432/persona", "postgres://u@a:5433,b:5433/persona", false},
		{"postgres://u@a,b/persona", "postgres://u@a,b/persona_manager", false},
	} {
		hub := Target{URL: c.hub, Database: "persona"}
		manager := Target{URL: c.manager, Database: "persona"}
		if strings.HasSuffix(c.manager, "persona_manager") {
			manager.Database = "persona_manager"
		}
		if got := sameDatabase(hub, manager); got != c.same {
			errorf(t, "sameDatabase(%s, %s) = %v, want %v", c.hub, c.manager, got, c.same)
		}
	}
}

func TestDiscoverRefusesOverlappingMultiHostDatabases(t *testing.T) {
	home := filepath.Join(t.TempDir(), "runtime")
	writeFixtureFile(t, filepath.Join(home, "hub.json"), []byte(`{"database_url":"postgres://u@a,b/persona"}`))
	writeFixtureFile(t, filepath.Join(home, "managers", "assistant", "manager.json"), []byte(`{"database_url":"postgres://u@c,a/persona"}`))
	if _, _, err := Discover(Options{Home: home}); err == nil || !strings.Contains(err.Error(), "separate databases") {
		fatalf(t, "overlapping multi-host discovery = %v", err)
	}
}

// A development snapshot supports a built-in manager whose tables are still in
// the hub database: shared says only the hub target needs checking. Once moved,
// a named database on the hub's server takes precedence over a saved URL, as
// webdev.py's inventory has it.
func TestDiscoverDevelopmentSupportsSharedAndMovedManagerDatabases(t *testing.T) {
	home := filepath.Join(t.TempDir(), "runtime")
	writeFixtureFile(t, filepath.Join(home, "hub.json"), []byte(`{"database_url":"postgres://u:p@db.local:5432/persona"}`))
	managerFile := filepath.Join(home, "managers", "assistant", "manager.json")

	// Not moved (and no manager.json at all): the Hub database alone.
	for name, content := range map[string]string{
		"no manager.json": "",
		"not moved":       `{"database_url":"postgres://u:p@db.local:5432/persona_manager"}`,
		"moved to a name": `{"moved_to_own_database":{"at":"2026-01-02T03:04:05Z"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if content == "" {
				_ = os.Remove(managerFile)
			} else {
				writeFixtureFile(t, managerFile, []byte(content))
			}
			hub, manager, shared, err := DiscoverDevelopment(Options{Home: home})
			if err != nil {
				fatal(t, err)
			}
			if !shared || hub.Database != "persona" || manager.Database != "" {
				fatalf(t, "hub=%+v manager=%+v shared=%v", hub, manager, shared)
			}
		})
	}

	// Moved with a named database: the hub's server with that name wins over
	// the saved URL.
	writeFixtureFile(t, managerFile, []byte(`{"database_url":"postgres://u:p@other.local:5432/assistant_saved",
		"database_name":"assistant_named","moved_to_own_database":{"at":"2026-01-02T03:04:05Z"}}`))
	_, manager, shared, err := DiscoverDevelopment(Options{Home: home})
	if err != nil {
		fatal(t, err)
	}
	if shared || manager.Database != "assistant_named" || !strings.Contains(manager.URL, "db.local") {
		fatalf(t, "named database: %+v shared=%v", manager, shared)
	}
	// Moved without a name: its saved URL.
	writeFixtureFile(t, managerFile, []byte(`{"database_url":"postgres://u:p@other.local:5432/assistant_saved",
		"moved_to_own_database":{"at":"2026-01-02T03:04:05Z"}}`))
	_, manager, shared, err = DiscoverDevelopment(Options{Home: home})
	if err != nil {
		fatal(t, err)
	}
	if shared || manager.Database != "assistant_saved" || !strings.Contains(manager.URL, "other.local") {
		fatalf(t, "saved database: %+v shared=%v", manager, shared)
	}
	// A URL that names the hub database is still one database.
	writeFixtureFile(t, managerFile, []byte(`{"database_url":"postgres://u:p@db.local:5432/persona",
		"moved_to_own_database":{"at":"2026-01-02T03:04:05Z"}}`))
	if _, _, shared, err = DiscoverDevelopment(Options{Home: home}); err != nil || !shared {
		fatalf(t, "same-name URL: shared=%v err=%v", shared, err)
	}
}
