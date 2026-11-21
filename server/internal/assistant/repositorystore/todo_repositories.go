// Package repositorystore keeps to-do repository associations in the assistant manager's database.
package repositorystore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

// Store adds repository persistence to the manager's ordinary database store.
// Embedding preserves every unrelated assistant store capability on the same connection pool.
type Store struct{ *db.DB }

// New wraps the assistant manager's database without opening another pool.
func New(database *db.DB) *Store { return &Store{DB: database} }

func decodeTodoRepositories(id string, revision int64, raw []byte) (repository.TaskSet, error) {
	out := repository.TaskSet{SchemaVersion: 1, TodoID: id, Revision: revision, Repositories: []repository.TaskRepository{}}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Repositories); err != nil {
			return out, err
		}
		if out.Repositories == nil {
			return out, repository.Problem("store_unavailable")
		}
	}
	return out, nil
}

func (d *Store) TodoRepositories(ctx context.Context, id string) (repository.TaskSet, error) {
	var revision int64
	var raw []byte
	err := d.QueryRow(ctx, `SELECT COALESCE(r.revision,0), r.repositories FROM todos t LEFT JOIN todo_repository_sets r ON r.todo_id=t.id WHERE t.id=$1`, id).Scan(&revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.TaskSet{}, db.ErrTodoNotFound
	}
	if err != nil {
		return repository.TaskSet{}, err
	}
	return decodeTodoRepositories(id, revision, raw)
}

// ReadTodoRepositoriesTx locks the Todo, including an empty association set. Preparation
// consumers can read this value and persist their own selected snapshot in the same
// transaction. It never seals business associations or changes legacy execution paths.
// Transactions needing both locks take the Todo before the catalog; ordinary EditTodo
// takes only the Todo lock, and catalog CAS updates never lock a Todo.
func ReadTodoRepositoriesTx(ctx context.Context, tx pgx.Tx, id string, expected *int64) (repository.TaskSet, error) {
	var todo string
	if err := tx.QueryRow(ctx, `SELECT id FROM todos WHERE id=$1 FOR UPDATE`, id).Scan(&todo); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repository.TaskSet{}, db.ErrTodoNotFound
		}
		return repository.TaskSet{}, err
	}
	var revision int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,repositories FROM todo_repository_sets WHERE todo_id=$1`, id).Scan(&revision, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return repository.TaskSet{}, err
	}
	if expected != nil && revision != *expected {
		return repository.TaskSet{}, repository.Problem("revision_conflict")
	}
	return decodeTodoRepositories(id, revision, raw)
}

// ReadRepositoryCatalogTx holds a shared lock through association validation and commit.
// Existing catalog CAS updates take the conflicting row lock, so identity cannot change
// between validation and save. No network call occurs while holding these locks.
func ReadRepositoryCatalogTx(ctx context.Context, tx pgx.Tx) (repository.Catalog, error) {
	var raw []byte
	var catalog repository.Catalog
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, repository.CatalogKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog, nil
	}
	if err != nil {
		return catalog, err
	}
	err = json.Unmarshal(raw, &catalog)
	return catalog, err
}

func writeTodoRepositoriesTx(ctx context.Context, tx pgx.Tx, set repository.TaskSet) error {
	raw, err := json.Marshal(set.Repositories)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO todo_repository_sets(todo_id,revision,repositories) VALUES($1,$2,$3) ON CONFLICT(todo_id) DO UPDATE SET revision=$2,repositories=$3`, set.TodoID, set.Revision, raw)
	return err
}

func (d *Store) CreateTodoWithRepositories(ctx context.Context, t db.TodoRow, in repository.CreateBindings) error {
	if !repository.ValidRevision(in.CatalogRevision) || in.Repositories == nil {
		return repository.Problem("invalid")
	}
	return d.Transaction(ctx, func(tx pgx.Tx) error {
		if err := db.CreateTodoTx(ctx, tx, t); err != nil {
			return err
		}
		catalog, err := ReadRepositoryCatalogTx(ctx, tx)
		if err != nil {
			return err
		}
		if catalog.Revision != *in.CatalogRevision {
			return repository.Problem("catalog_conflict")
		}
		rows, err := repository.Bind(nil, in.Repositories, catalog)
		if err != nil {
			return err
		}
		return writeTodoRepositoriesTx(ctx, tx, repository.TaskSet{TodoID: t.ID, Revision: 1, Repositories: rows})
	})
}

func (d *Store) SetTodoRepositories(ctx context.Context, id string, in repository.SetInput) (repository.TaskSet, error) {
	var out repository.TaskSet
	if !repository.ValidRevision(in.ExpectedRevision) || !repository.ValidRevision(in.ExpectedCatalogRevision) || in.Repositories == nil {
		return out, repository.Problem("invalid")
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, in.ExpectedRevision)
		if err != nil {
			return err
		}
		catalog, err := ReadRepositoryCatalogTx(ctx, tx)
		if err != nil {
			return err
		}
		if catalog.Revision != *in.ExpectedCatalogRevision {
			return repository.Problem("catalog_conflict")
		}
		if err := rememberTodoRepositoryLinksTx(ctx, tx, id, repositoryURLs(cur.Repositories), false); err != nil {
			return err
		}
		rows, err := repository.Bind(cur.Repositories, in.Repositories, catalog)
		if err != nil {
			return err
		}
		cur.Repositories = rows
		cur.Revision++
		out = cur
		return writeTodoRepositoriesTx(ctx, tx, cur)
	})
	return out, err
}

func (d *Store) SetTodoRepositoryLinks(ctx context.Context, id, repoID string, in repository.LinksInput) (repository.TaskSet, error) {
	var out repository.TaskSet
	if !repository.ValidRevision(in.ExpectedRevision) || in.Links == nil {
		return out, repository.Problem("invalid")
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, in.ExpectedRevision)
		if err != nil {
			return err
		}
		if err := rememberTodoRepositoryLinksTx(ctx, tx, id, repositoryURLs(cur.Repositories), false); err != nil {
			return err
		}
		for index, row := range cur.Repositories {
			if row.RepoID == repoID {
				links, err := repository.NormalizeLinks(row.SourceSnapshot.Identity, in.Links)
				if err != nil {
					return err
				}
				repository.PreserveLinkTitles(row.Links, links)
				cur.Repositories[index].Links = links
				cur.Revision++
				out = cur
				return writeTodoRepositoriesTx(ctx, tx, cur)
			}
		}
		return repository.Problem("not_found")
	})
	return out, err
}

// SetTodoRepositoryLinkRoles persists a role rewrite the manager classified (e.g. a proven-foreign PR downgraded to
// reference), serializing with interactive edits through the same revision and Todo lock.
func (d *Store) SetTodoRepositoryLinkRoles(ctx context.Context, id string, rows []repository.TaskRepository) (repository.TaskSet, error) {
	var out repository.TaskSet
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, nil)
		if err != nil {
			return err
		}
		roles := map[string]string{}
		for _, row := range rows {
			for _, l := range row.Links {
				role, perr := repository.NormalizeLinkRole(l.Role)
				if perr != "" {
					return perr
				}
				roles[l.CanonicalURL] = role
			}
		}
		changed := false
		for ri := range cur.Repositories {
			for li := range cur.Repositories[ri].Links {
				if role, ok := roles[cur.Repositories[ri].Links[li].CanonicalURL]; ok && cur.Repositories[ri].Links[li].Role != role {
					cur.Repositories[ri].Links[li].Role = role
					changed = true
				}
			}
		}
		if !changed {
			out = cur
			return nil
		}
		if cur.Revision >= repository.MaxRevision {
			return repository.Problem("limit")
		}
		cur.Revision++
		out = cur
		return writeTodoRepositoriesTx(ctx, tx, cur)
	})
	return out, err
}

func repositoryURLs(rows []repository.TaskRepository) []string {
	urls := []string{}
	for _, row := range rows {
		for _, link := range row.Links {
			urls = append(urls, link.CanonicalURL)
		}
	}
	return urls
}
func rememberTodoRepositoryLinksTx(ctx context.Context, tx pgx.Tx, id string, urls []string, legacy bool) error {
	if urls == nil {
		urls = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO todo_repository_link_receipts(todo_id,seen_urls,legacy_imported) VALUES($1,$2,$3)
 ON CONFLICT(todo_id) DO UPDATE SET seen_urls=ARRAY(SELECT DISTINCT value FROM unnest(todo_repository_link_receipts.seen_urls || EXCLUDED.seen_urls) AS value), legacy_imported=todo_repository_link_receipts.legacy_imported OR EXCLUDED.legacy_imported`, id, urls, legacy)
	return err
}

// ImportTodoRepositoryLinks is additive and serializes with interactive edits on the Todo.
// Historical imports run once; repeated reports cannot resurrect removed references.
// An explicit agent/user request can deliberately add a previously removed reference.
func (d *Store) ImportTodoRepositoryLinks(ctx context.Context, id string, urls []string, source string) (repository.TaskSet, bool, error) {
	input := make([]repository.LinkInput, 0, len(urls))
	for _, raw := range urls {
		input = append(input, repository.LinkInput{URL: raw})
	}
	return d.ImportTodoRepositoryLinkDetails(ctx, id, input, source)
}

// ImportTodoRepositoryLinkDetails uses the same transaction for references and titles.
func (d *Store) ImportTodoRepositoryLinkDetails(ctx context.Context, id string, input []repository.LinkInput, source string) (repository.TaskSet, bool, error) {
	var out repository.TaskSet
	var changed bool
	if source != "legacy" && source != "report" && source != "explicit" {
		return out, false, repository.Problem("invalid")
	}
	if len(input) > repository.MaxTaskRepositories*repository.MaxRepositoryLinks {
		return out, false, repository.Problem("limit")
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, nil)
		if err != nil {
			return err
		}
		out = cur
		var seen []string
		var legacy bool
		err = tx.QueryRow(ctx, `SELECT seen_urls,legacy_imported FROM todo_repository_link_receipts WHERE todo_id=$1`, id).Scan(&seen, &legacy)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if source == "legacy" && legacy {
			return nil
		}
		pending := []repository.LinkInput{}
		canonical := []string{}
		for _, in := range input {
			_, link, err := repository.ParseTaskLinkInput(in)
			if err != nil {
				return err
			}
			canonical = append(canonical, link.CanonicalURL)
			if source == "explicit" || !slices.Contains(seen, link.CanonicalURL) {
				pending = append(pending, repository.LinkInput{URL: link.CanonicalURL, Title: link.Title, Role: in.Role, Resolved: in.Resolved})
			}
		}
		if len(pending) == 0 {
			if source == "legacy" {
				return rememberTodoRepositoryLinksTx(ctx, tx, id, canonical, true)
			}
			return nil
		}
		catalog, err := ReadRepositoryCatalogTx(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := repository.MergeTaskLinkDetails(cur.Repositories, pending, catalog)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(rows, cur.Repositories) {
			if cur.Revision >= repository.MaxRevision {
				return repository.Problem("limit")
			}
			cur.Repositories = rows
			cur.Revision++
			changed = true
			if err := writeTodoRepositoriesTx(ctx, tx, cur); err != nil {
				return err
			}
		}
		out = cur
		return rememberTodoRepositoryLinksTx(ctx, tx, id, canonical, source == "legacy")
	})
	return out, changed && err == nil, err
}
