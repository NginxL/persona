package db

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PushSent is one alert push the hub delivered (hub/push_grading.go): the record behind the daily push counts. Rows are only
// appended and swept (#8257); the JSON names are those of the hub.push_sent setting it replaces, which
// AdoptPushSentSetting reads and the hub still writes for a build from before the table.
type PushSent struct {
	// ID names the logical push, chosen once before its first write and kept across retries: the table's UNIQUE
	// push_id, so writing the same push again adds nothing. Not in the old setting, whose entries are matched to rows
	// by what they hold and get a new one when added (MergePushSentSetting).
	ID         string    `json:"-"`
	At         time.Time `json:"at"`
	Level      string    `json:"level"`
	RelatedKey string    `json:"related_key,omitempty"`
	Manager    string    `json:"manager"`
}

// AddPushSent appends entries in their order, in one statement however many there are. An entry whose ID is already
// in the table is skipped, so a batch retried after a write that landed but reported an error (a timeout, a dropped
// connection) is not counted twice. Every entry needs an ID.
func (d *DB) AddPushSent(ctx context.Context, entries []PushSent) error {
	_, err := addPushSent(ctx, d.pool, entries)
	return err
}

// addPushSent returns how many rows it added: the entries whose ID was not there yet.
func addPushSent(ctx context.Context, q execer, entries []PushSent) (int64, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	id := make([]string, len(entries))
	at := make([]time.Time, len(entries))
	level := make([]string, len(entries))
	key := make([]string, len(entries))
	manager := make([]string, len(entries))
	for i, e := range entries {
		if e.ID == "" {
			return 0, fmt.Errorf("push sent entry %d has no id", i)
		}
		id[i], at[i], level[i], key[i], manager[i] = e.ID, e.At, e.Level, e.RelatedKey, e.Manager
	}
	tag, err := q.Exec(ctx, `INSERT INTO hub_push_sent (push_id, at, level, related_key, manager)
		SELECT push_id, at, level, related_key, manager
		FROM unnest($1::text[], $2::timestamptz[], $3::text[], $4::text[], $5::text[])
			WITH ORDINALITY AS e(push_id, at, level, related_key, manager, n)
		ORDER BY n
		ON CONFLICT (push_id) DO NOTHING`, id, at, level, key, manager)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// pushSentContent is what one delivered push holds, without its ID: how a row and an entry of the hub.push_sent
// setting, which has no IDs, are told to be the same push. The time is in microseconds, the table's precision.
type pushSentContent struct {
	at                      int64
	level, related, manager string
}

func contentOf(e PushSent) pushSentContent {
	return pushSentContent{e.At.UnixMicro(), e.Level, e.RelatedKey, e.Manager}
}

// MergePushSentSetting compares the hub.push_sent setting's entries with the table's rows (in the order they were
// added) by what they hold, each row standing for one entry (two equal entries are two pushes): missing are the
// entries the table lacks, in the setting's order, each with a new ID; behind says the table has rows later than
// after that the setting lacks. Entries the sweep would take are not missing: the ones at or before after, and, with
// the table at its keep rows, the ones earlier than the oldest of those (rows the table had and swept, still in a
// setting written before that).
func MergePushSentSetting(setting, table []PushSent, after time.Time, keep int) (missing []PushSent, behind bool) {
	have := make(map[pushSentContent]int, len(table))
	for _, r := range table {
		if r.At.After(after) {
			have[contentOf(r)]++
		}
	}
	var oldestKept time.Time
	if keep > 0 && len(table) >= keep {
		oldestKept = table[len(table)-keep].At
	}
	for _, e := range setting {
		if !e.At.After(after) || e.At.Before(oldestKept) {
			continue
		}
		if k := contentOf(e); have[k] > 0 {
			have[k]--
			continue
		}
		e.ID = "setting:" + rand.Text()
		missing = append(missing, e)
	}
	for _, n := range have {
		if n > 0 {
			return missing, true
		}
	}
	return missing, false
}

// PushSentAfter returns the newest limit rows later than after, oldest first (the order they were added in).
func (d *DB) PushSentAfter(ctx context.Context, after time.Time, limit int) ([]PushSent, error) {
	rows, err := d.pool.Query(ctx, `SELECT push_id, at, level, related_key, manager FROM (
			SELECT id, push_id, at, level, related_key, manager FROM hub_push_sent WHERE at > $1 ORDER BY id DESC LIMIT $2
		) newest ORDER BY id`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSent{}
	for rows.Next() {
		var e PushSent
		if err := rows.Scan(&e.ID, &e.At, &e.Level, &e.RelatedKey, &e.Manager); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PrunePushSent deletes the rows at or before before, and all but the newest keep; it returns how many went.
func (d *DB) PrunePushSent(ctx context.Context, before time.Time, keep int) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM hub_push_sent WHERE at <= $1
		OR id <= (SELECT id FROM hub_push_sent ORDER BY id DESC OFFSET $2 LIMIT 1)`, before, keep)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// AdoptPushSentSetting adds to the table the entries of the settings row key (the whole-list record before
// hub_push_sent) that it lacks, after its rows, in one transaction, and leaves the row as it is: a build from before
// the table reads only that row, so the hub keeps it for a switch back (hub/push_grading.go mirrorSent). It returns how many
// rows it added, and whether the table holds pushes later than after that the row lacks (no row: every one), which
// is when the row is due a write. It runs at every start of the hub: the first one after the upgrade adds the whole
// list; a later one adds what a build from before, switched back to, delivered meanwhile. An entry is the same push
// as a row by what it holds (MergePushSentSetting), so the entries this build wrote into the row, and ones added
// before, are not added again; after and keep are the sweep's limits, whose entries are left out.
func (d *DB) AdoptPushSentSetting(ctx context.Context, key string, after time.Time, keep int) (added int, behind bool, err error) {
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var entries []PushSent
		var raw []byte
		// Locked: two starts at once would each find the same entries missing.
		err := tx.QueryRow(ctx, `SELECT value FROM `+d.settingsTable()+` WHERE key = $1 FOR UPDATE`, key).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return fmt.Errorf("adopt %s: %w", key, err)
			}
		}
		rows, err := tx.Query(ctx, `SELECT at, level, related_key, manager FROM hub_push_sent ORDER BY id`)
		if err != nil {
			return err
		}
		table, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (e PushSent, err error) {
			err = row.Scan(&e.At, &e.Level, &e.RelatedKey, &e.Manager)
			return e, err
		})
		if err != nil {
			return err
		}
		var missing []PushSent
		missing, behind = MergePushSentSetting(entries, table, after, keep)
		n, err := addPushSent(ctx, tx, missing)
		added = int(n)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	return added, behind, nil
}
