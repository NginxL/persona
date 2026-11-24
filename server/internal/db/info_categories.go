package db

import (
	"context"
	"time"
)

// The information categories what the assistant keeps is tagged with (#13246, migrations/manager/
// 20261122185553_assistant_info_categories.sql). The manager checks the categories; the store keeps what it is given.

// InfoCategoryConversation is the kind of a conversation's tags: ref is its session id.
const InfoCategoryConversation = "conversation"

// InfoCategoryByUser is the tagger only a category the user took away is added back by.
const InfoCategoryByUser = "user"

// InfoCategory is one category of one tagged object.
type InfoCategory struct {
	Ref      string
	Category string
	By       string
	Model    string
	// RemovedByUser: the user took it away; it is kept so nothing but the user adds it back.
	RemovedByUser bool
	TaggedAt      time.Time
}

// AddInfoCategories tags kind's ref with cats, by a tagger (model names the model that did, when one did). A category
// the user took away is added back only when the user adds it; one already there is kept as it was tagged.
func (d *DB) AddInfoCategories(ctx context.Context, kind, ref string, cats []string, by, model string) error {
	if len(cats) == 0 {
		return nil
	}
	q := `INSERT INTO assistant_info_categories (kind, ref, category, by, model)
		SELECT $1, $2, c, $4, $5 FROM unnest($3::text[]) AS c
		ON CONFLICT (kind, ref, category) DO NOTHING`
	if by == InfoCategoryByUser {
		q = `INSERT INTO assistant_info_categories (kind, ref, category, by, model)
			SELECT $1, $2, c, $4, $5 FROM unnest($3::text[]) AS c
			ON CONFLICT (kind, ref, category) DO UPDATE SET by = EXCLUDED.by, model = EXCLUDED.model,
				removed_by_user = false, tagged_at = now()
				WHERE assistant_info_categories.removed_by_user`
	}
	_, err := d.pool.Exec(ctx, q, kind, ref, cats, by, model)
	return err
}

// RemoveInfoCategories are the user taking cats away from kind's ref: each stays as a row the user removed.
func (d *DB) RemoveInfoCategories(ctx context.Context, kind, ref string, cats []string) error {
	if len(cats) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO assistant_info_categories (kind, ref, category, by, removed_by_user)
		SELECT $1, $2, c, $4, true FROM unnest($3::text[]) AS c
		ON CONFLICT (kind, ref, category) DO UPDATE SET by = EXCLUDED.by, model = '', removed_by_user = true,
			tagged_at = now()`, kind, ref, cats, InfoCategoryByUser)
	return err
}

// InfoCategories are the categories of kind's refs, by ref, removed ones included, each ref's by category name.
func (d *DB) InfoCategories(ctx context.Context, kind string, refs []string) (map[string][]InfoCategory, error) {
	out := map[string][]InfoCategory{}
	if len(refs) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT ref, category, by, model, removed_by_user, tagged_at FROM assistant_info_categories
		WHERE kind = $1 AND ref = ANY($2) ORDER BY ref, category`, kind, refs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c InfoCategory
		if err := rows.Scan(&c.Ref, &c.Category, &c.By, &c.Model, &c.RemovedByUser, &c.TaggedAt); err != nil {
			return nil, err
		}
		out[c.Ref] = append(out[c.Ref], c)
	}
	return out, rows.Err()
}

// InfoCategoryCapture is the kind of a quick capture's tags: ref is its id (assistant_captures).
const InfoCategoryCapture = "capture"
