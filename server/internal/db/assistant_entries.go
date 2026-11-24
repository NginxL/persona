package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/proto"
)

var ErrEntryNotFound = errors.New("ledger entry not found")
var ErrEntrySuperseded = errors.New("ledger entry already has a successor")

// AppendAssistantEntry serializes appends so at is an increasing, server-assigned
// timestamp. A reader cannot miss a backdated append behind its time cursor.
func (d *DB) AppendAssistantEntry(ctx context.Context, e proto.AssistantEntry) (proto.AssistantEntry, error) {
	NormalizeAssistantEntry(&e)
	if err := ValidateAssistantEntry(e); err != nil {
		return e, err
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('assistant_entries'))`); err != nil {
			return err
		}
		// Duplicate internal source IDs replay without appending another entry.
		var existing []byte
		err := tx.QueryRow(ctx, `SELECT body FROM assistant_entries WHERE id=$1`, e.ID).Scan(&existing)
		if err == nil {
			var previous proto.AssistantEntry
			if err := json.Unmarshal(existing, &previous); err != nil {
				return err
			}
			e.At = previous.At
			if !reflect.DeepEqual(e, previous) {
				return errors.New("entry id conflicts with immutable content")
			}
			e = previous
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if e.Supersedes != "" {
			var personal bool
			err := tx.QueryRow(ctx, `SELECT (body->'scope'->>'personal')::boolean FROM assistant_entries WHERE id=$1`, e.Supersedes).Scan(&personal)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEntryNotFound
			}
			if err != nil {
				return err
			}
			// A successor must retain the personal-data access boundary.
			if personal && !e.Scope.Personal {
				return errors.New("a personal entry requires a personal successor")
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_entries WHERE supersedes=$1)`, e.Supersedes).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return ErrEntrySuperseded
			}
		}
		if err := tx.QueryRow(ctx, `SELECT GREATEST(clock_timestamp(),COALESCE(MAX(at)+interval '1 microsecond', '-infinity'::timestamptz)) FROM assistant_entries`).Scan(&e.At); err != nil {
			return err
		}
		e.At = e.At.UTC()
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO assistant_entries(id,at,supersedes,body) VALUES($1,$2,NULLIF($3,''),$4)`, e.ID, e.At, e.Supersedes, b)
		return err
	})
	return e, err
}

func (d *DB) AssistantEntry(ctx context.Context, id string) (proto.AssistantEntry, error) {
	var b []byte
	var e proto.AssistantEntry
	err := d.pool.QueryRow(ctx, `SELECT body FROM assistant_entries WHERE id=$1`, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrEntryNotFound
	}
	if err != nil {
		return e, err
	}
	err = json.Unmarshal(b, &e)
	return e, err
}

// ListAssistantEntries filters in SQL before limiting, including the caller's
// personal-data boundary. Status is derived; the original bytes stay intact.
func (d *DB) ListAssistantEntries(ctx context.Context, p proto.AssistantEntriesParams, personal bool) (proto.AssistantEntriesResult, error) {
	return d.listAssistantEntries(ctx, p, personal, false)
}

// NewestAssistantEntries is ListAssistantEntries newest first: each page is the
// next older one, and After continues past its oldest entry. The memory
// assembler reads it so that a large ledger never hides the newest entries.
func (d *DB) NewestAssistantEntries(ctx context.Context, p proto.AssistantEntriesParams, personal bool) (proto.AssistantEntriesResult, error) {
	return d.listAssistantEntries(ctx, p, personal, true)
}

func (d *DB) listAssistantEntries(ctx context.Context, p proto.AssistantEntriesParams, personal, newest bool) (proto.AssistantEntriesResult, error) {
	out := proto.AssistantEntriesResult{Entries: []proto.AssistantEntry{}, After: p.After}
	if err := ValidateAssistantEntriesParams(&p); err != nil {
		return out, err
	}
	c, _ := decodeAssistantEntriesCursor(p.After)
	filter := proto.AssistantEntryScopeFilter{}
	if p.Scope != nil {
		filter = *p.Scope
	}
	machines, _ := json.Marshal(filter.Machine)
	projects, _ := json.Marshal(filter.Project)
	// Use arrays, not JSON null, for the overlap predicates below.
	if filter.Machine == nil {
		machines = []byte(`[]`)
	}
	if filter.Project == nil {
		projects = []byte(`[]`)
	}
	now := time.Now().UTC()
	after, order := ">", "at,id"
	if newest {
		after, order = "<", "at DESC,id DESC"
	}
	rows, err := d.pool.Query(ctx, `WITH effective AS (
 SELECT e.*, CASE
 WHEN n.body->>'status'='wrong' THEN 'wrong'
 WHEN n.id IS NOT NULL THEN 'superseded'
 WHEN e.body->>'status'='wrong' THEN 'wrong'
 WHEN e.body->>'status'='superseded' THEN 'superseded'
 WHEN NULLIF(e.body->>'expires_at','')::timestamptz <= $1 THEN 'expired'
 ELSE 'active' END AS status
 FROM assistant_entries e LEFT JOIN assistant_entries n ON n.supersedes=e.id
 ) SELECT body, status FROM effective e WHERE
 ($2 OR NOT (body->'scope'->>'personal')::boolean)
 AND ($3='' OR body->>'by'=$3) AND ($4='' OR body->>'trust'=$4)
 AND (($5='' AND status<>'expired') OR $5='all' OR status=$5)
 AND ($6='' OR body->'tag' @> jsonb_build_array($6::text))
 AND ($7='' OR body->>'type'=$7)
 AND ($8='' OR body->'scope'->>'executor' IN ('any',$8))
 AND ($9::jsonb='[]' OR body->'scope'->'machine'='[]' OR EXISTS(
 SELECT 1 FROM jsonb_array_elements_text($9::jsonb) v WHERE body->'scope'->'machine' @> jsonb_build_array(v.value)))
 AND ($10::jsonb='[]' OR body->'scope'->'project'='[]' OR EXISTS(
 SELECT 1 FROM jsonb_array_elements_text($10::jsonb) v WHERE body->'scope'->'project' @> jsonb_build_array(v.value)))
 AND ($11::boolean IS NULL OR (body->'scope'->>'personal')::boolean=$11)
 AND ($12::timestamptz IS NULL OR (at,id)`+after+`($12,$13::text))
 ORDER BY `+order+` LIMIT $14`, now, personal, p.By, p.Trust, p.Status, p.Tag, p.Type, filter.Executor, machines, projects, filter.Personal, cursorTime(c), c.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var status string
		var e proto.AssistantEntry
		if err := rows.Scan(&b, &status); err != nil {
			return out, err
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return out, err
		}
		e.Status = status
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Entries) > p.Limit {
		out.HasMore = true
		out.Entries = out.Entries[:p.Limit]
	}
	if len(out.Entries) > 0 {
		out.After = encodeAssistantEntriesCursor(out.Entries[len(out.Entries)-1])
	}
	return out, nil
}
func cursorTime(c assistantEntriesCursor) *time.Time {
	if c.At.IsZero() {
		return nil
	}
	return &c.At
}
