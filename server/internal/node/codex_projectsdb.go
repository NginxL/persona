package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Projects move independently of thread assignments from the desktop state file into Codex's database. Both
// catalogs are needed during that migration, including projects with no chats. A mapped native row replaces its
// legacy row; native positions lead, followed by the still-unmigrated projects in their saved sidebar order.
func codexDesktopProjects(home string) (map[string]codexAppProject, []string, error) {
	snapshot, err := codexDesktopProjectSnapshot(home)
	return snapshot.Projects, snapshot.Order, err
}

type codexProjectSnapshot struct {
	Projects         map[string]codexAppProject
	Order            []string
	ThreadProjects   map[string]string // active native thread id -> canonical project id; empty means confirmed projectless
	NativeProjectIDs map[string]string // native project id -> canonical project id
	Stamp            string            // source stamp of the last complete successful refresh
	Revision         uint64            // also changes when a previously unavailable reader becomes usable
}

// Nothing writes to either source. A failed refresh preserves the entire last successful snapshot and its stamp.
func codexDesktopProjectSnapshot(home string) (codexProjectSnapshot, error) {
	if !filepath.IsAbs(home) {
		return codexProjectSnapshot{}, nil
	}
	codexProjectCatalogs.Lock()
	c := codexProjectCatalogs.byHome[home]
	if c == nil {
		c = &codexProjectCatalog{}
		codexProjectCatalogs.byHome[home] = c
	}
	codexProjectCatalogs.Unlock()
	c.Lock()
	defer c.Unlock()

	state := filepath.Join(home, ".codex-global-state.json")
	db := codexAppDBPath(home)
	stamp := fileStamp(state) + db + fileStamp(db) + fileStamp(db+"-wal")
	if stamp == c.stamp && time.Since(c.at) < codexAppDBRefresh {
		return c.snapshot, c.err
	}
	c.at, c.stamp = time.Now(), stamp
	snapshot, err := readCodexDesktopProjectSnapshot(home, state, db)
	c.err = err
	if err == nil {
		snapshot.Stamp = stamp
		snapshot.Revision = c.snapshot.Revision + 1
		c.snapshot = snapshot
	}
	return c.snapshot, c.err
}

type codexProjectCatalog struct {
	sync.Mutex
	at       time.Time
	stamp    string
	snapshot codexProjectSnapshot
	err      error
}

var codexProjectCatalogs = struct {
	sync.Mutex
	byHome map[string]*codexProjectCatalog
}{byHome: map[string]*codexProjectCatalog{}}

type codexLegacyProjectState struct {
	Projects    map[string]codexAppProject   `json:"local-projects"`
	Order       []string                     `json:"project-order"`
	Mapped      map[string]map[string]string `json:"app-server-project-id-by-legacy-project-id-by-host"`
	Migration   map[string]json.RawMessage   `json:"app-server-projects-migration-by-host"`
	Assignments map[string]json.RawMessage   `json:"thread-project-assignments"`
	Projectless []string                     `json:"projectless-thread-ids"`
}

type codexProjectMigration struct {
	ThreadAssignmentsMigrated     bool     `json:"threadAssignmentsMigrated"`
	ThreadAssignmentsReadMigrated bool     `json:"threadAssignmentsReadMigrated"`
	PendingThreadAssignmentIDs    []string `json:"pendingThreadAssignmentIds"`
}

func codexProjectHomeMatches(host, home string) bool {
	mappedHome, ok := strings.CutPrefix(host, "local:")
	return ok && filepath.IsAbs(mappedHome) && folderKey(filepath.Clean(mappedHome)) == folderKey(filepath.Clean(home))
}

func readCodexDesktopProjectSnapshot(home, state, db string) (codexProjectSnapshot, error) {
	var raw codexLegacyProjectState
	found := false
	if b, err := os.ReadFile(state); err == nil {
		if err := json.Unmarshal(b, &raw); err != nil {
			return codexProjectSnapshot{}, fmt.Errorf("read Codex project state: %w", err)
		}
		found = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return codexProjectSnapshot{}, fmt.Errorf("read Codex project state: %w", err)
	}
	projects := maps.Clone(raw.Projects)
	if projects == nil {
		projects = map[string]codexAppProject{}
	}
	ctx := context.Background()
	native, nativeOrder, supported, err := readCodexNativeProjects(ctx, db)
	if err != nil {
		return codexProjectSnapshot{}, err
	}
	if !found && !supported {
		return codexProjectSnapshot{}, nil
	}
	var threads map[string]string
	if supported {
		threads, err = readCodexNativeProjectThreads(ctx, db)
		if err != nil {
			return codexProjectSnapshot{}, err
		}
	}

	// Host identities include the Codex home. Never borrow another local profile's or a remote host's mapping.
	legacyByNative := map[string]string{}
	for host, mapping := range raw.Mapped {
		if !codexProjectHomeMatches(host, home) {
			continue
		}
		for _, legacy := range slices.Sorted(maps.Keys(mapping)) {
			id := mapping[legacy]
			if id != "" && (legacyByNative[id] == "" || legacy < legacyByNative[id]) {
				legacyByNative[id] = legacy
			}
		}
	}
	order := make([]string, 0, len(projects)+len(native))
	seen := map[string]bool{}
	appendID := func(id string) {
		if _, ok := projects[id]; id != "" && ok && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	nativeIDs := map[string]string{}
	for _, id := range nativeOrder {
		key := id
		if legacy := legacyByNative[id]; legacy != "" {
			key = legacy
		}
		projects[key] = native[id]
		nativeIDs[id] = key
		// The app may have cached the native id already as well as its legacy id.
		if key != id {
			delete(projects, id)
		}
		appendID(key)
	}
	for _, id := range raw.Order {
		appendID(id)
	}
	for _, id := range slices.Sorted(maps.Keys(projects)) {
		appendID(id)
	}
	// A null native project predates migration as well as representing explicit removal. Only completed local
	// membership migration can override the legacy record, and pending or previously unknown chats stay untouched.
	authoritative, foundMigration := false, false
	pending := map[string]bool{}
	for host, marker := range raw.Migration {
		if !codexProjectHomeMatches(host, home) {
			continue
		}
		// Older clients used numeric markers, and unrelated hosts may still do so.
		// Only a complete, understood marker for this home can remove a legacy assignment.
		var migration codexProjectMigration
		complete := json.Unmarshal(marker, &migration) == nil && migration.ThreadAssignmentsMigrated && migration.ThreadAssignmentsReadMigrated
		if !foundMigration {
			authoritative = complete
			foundMigration = true
		} else {
			authoritative = authoritative && complete
		}
		for _, thread := range migration.PendingThreadAssignmentIDs {
			pending[thread] = true
		}
	}
	known := map[string]bool{}
	for thread := range raw.Assignments {
		known[thread] = true
	}
	for _, thread := range raw.Projectless {
		known[thread] = true
	}
	assigned := map[string]string{}
	for thread, id := range threads {
		if id == "" {
			if authoritative && known[thread] && !pending[thread] {
				assigned[thread] = ""
			}
		} else if key, ok := nativeIDs[id]; ok {
			assigned[thread] = key
		}
	}
	return codexProjectSnapshot{Projects: projects, Order: order, ThreadProjects: assigned, NativeProjectIDs: nativeIDs}, nil
}

// readCodexNativeProjects recognizes an older Codex database with no project tables as an absent source. A schema
// error in existing tables, a locked database, or malformed data remains an error, not an empty successful refresh.
func readCodexNativeProjects(ctx context.Context, db string) (map[string]codexAppProject, []string, bool, error) {
	if db == "" {
		return nil, nil, false, nil
	}
	sqlite, err := codexSQLiteProgram()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	// Both root and path have shipped as the root column, and older catalogs have
	// no saved positions. Inspect capabilities before choosing the read query so
	// a missing source is distinct from a broken or temporarily unreadable one.
	out, err := codexProjectQuery(ctx, sqlite, db,
		`SELECT 'projects' AS tableName, name FROM pragma_table_info('projects') UNION ALL `+
			`SELECT 'project_roots' AS tableName, name FROM pragma_table_info('project_roots')`)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read Codex project schema: %w", err)
	}
	var columns []struct {
		TableName string `json:"tableName"`
		Name      string `json:"name"`
	}
	if len(bytes.TrimSpace(out)) != 0 {
		if err := json.Unmarshal(out, &columns); err != nil {
			return nil, nil, false, fmt.Errorf("decode Codex project schema: %w", err)
		}
	}
	projectColumns, rootColumns := map[string]bool{}, map[string]bool{}
	for _, column := range columns {
		switch column.TableName {
		case "projects":
			projectColumns[column.Name] = true
		case "project_roots":
			rootColumns[column.Name] = true
		}
	}
	if len(projectColumns) == 0 {
		return nil, nil, false, nil
	}
	rootColumn := "path"
	if !rootColumns[rootColumn] {
		rootColumn = "root"
	}
	if !projectColumns["id"] || !projectColumns["name"] || !rootColumns["project_id"] || !rootColumns[rootColumn] {
		return nil, nil, false, errors.New("read Codex projects: unsupported project catalog schema")
	}
	orderBy := []string{}
	if projectColumns["position"] {
		orderBy = append(orderBy, "p.position")
	}
	orderBy = append(orderBy, "p.id")
	if rootColumns["position"] {
		orderBy = append(orderBy, "r.position")
	}
	orderBy = append(orderBy, "r."+rootColumn)
	q := `SELECT p.id, p.name, r.` + rootColumn + ` AS path FROM projects p LEFT JOIN project_roots r ON r.project_id = p.id ` +
		`ORDER BY ` + strings.Join(orderBy, ", ")
	out, err = codexProjectQuery(ctx, sqlite, db, q)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read Codex projects: %w", err)
	}
	var rows []struct {
		ID   string  `json:"id"`
		Name string  `json:"name"`
		Path *string `json:"path"`
	}
	if len(bytes.TrimSpace(out)) != 0 {
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, nil, false, fmt.Errorf("decode Codex projects: %w", err)
		}
	}
	projects := map[string]codexAppProject{}
	var order []string
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		p, known := projects[row.ID]
		if !known {
			p.Name = row.Name
			order = append(order, row.ID)
		}
		if row.Path != nil && *row.Path != "" && !slices.Contains(p.RootPaths, *row.Path) {
			p.RootPaths = append(p.RootPaths, *row.Path)
		}
		projects[row.ID] = p
	}
	return projects, order, true, nil
}

// The threads table predates native projects; a database without project_id simply has no native assignments.
// Check that capability first so real errors in a supported assignment schema do not look like an empty catalog.
func readCodexNativeProjectThreads(ctx context.Context, db string) (map[string]string, error) {
	sqlite, err := codexSQLiteProgram()
	if err != nil {
		return nil, err
	}
	out, err := codexProjectQuery(ctx, sqlite, db, `SELECT name FROM pragma_table_info('threads') WHERE name = 'project_id'`)
	if err != nil {
		return nil, fmt.Errorf("read Codex project assignment schema: %w", err)
	}
	var columns []struct {
		Name string `json:"name"`
	}
	if len(bytes.TrimSpace(out)) != 0 {
		if err := json.Unmarshal(out, &columns); err != nil {
			return nil, fmt.Errorf("decode Codex project assignment schema: %w", err)
		}
	}
	if len(columns) == 0 {
		return nil, nil
	}
	q := `SELECT id, COALESCE(project_id, '') AS project_id FROM threads WHERE archived = 0 ORDER BY id`
	out, err = codexProjectQuery(ctx, sqlite, db, q)
	if err != nil {
		return nil, fmt.Errorf("read Codex project assignments: %w", err)
	}
	var rows []struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	}
	if len(bytes.TrimSpace(out)) != 0 {
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, fmt.Errorf("decode Codex project assignments: %w", err)
		}
	}
	assigned := map[string]string{}
	for _, row := range rows {
		if row.ID != "" {
			assigned[row.ID] = row.ProjectID
		}
	}
	return assigned, nil
}

func codexProjectQuery(ctx context.Context, sqlite, db, query string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, sqlite, "-readonly", "-bail", "-json", "-cmd", ".timeout 2000", codexDBTarget(db), query)
	hideCodexSQLiteWindow(cmd)
	return cmd.Output()
}
