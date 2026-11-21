package repositorystore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repository"
)

// followupColumns is the shared column list for reading one subscription row.
const followupColumns = `followup_id, todo_id, pr_url, supervisor, origin, head_oid, state, review,
	changes_requested_at, last_observed_at, created_at, notified_conflict_head, notified_review_head`

// scanPRFollowup scans one subscription from any pgx row source.
func scanPRFollowup(row pgx.Row) (repository.PRFollowup, error) {
	var f repository.PRFollowup
	err := row.Scan(&f.ID, &f.TodoID, &f.PRURL, &f.Supervisor, &f.Origin, &f.HeadOID, &f.State, &f.Review,
		&f.ChangesRequestedAt, &f.LastObservedAt, &f.CreatedAt, &f.NotifiedConflictHead, &f.NotifiedReviewHead)
	return f, err
}

// prFollowupID derives a stable subscription id from its to-do and PR so re-acceptance is idempotent.
func prFollowupID(todoID, prURL string) string {
	sum := sha256.Sum256([]byte(todoID + "|" + prURL))
	return "fup_" + hex.EncodeToString(sum[:12])
}

// UpsertPRFollowups records a follow-up for each still-open delivery PR of a to-do that was just accepted. An
// existing subscription for the pair is left untouched (its observed facts and notifications continue), so this is
// safe to call on every acceptance.
func (d *Store) UpsertPRFollowups(ctx context.Context, todoID, supervisor, origin string, prURLs []string, now time.Time) error {
	if len(prURLs) == 0 {
		return nil
	}
	return d.Transaction(ctx, func(tx pgx.Tx) error {
		for _, prURL := range prURLs {
			id := prFollowupID(todoID, prURL)
			_, err := tx.Exec(ctx, `INSERT INTO todo_pr_followups
				(followup_id, todo_id, pr_url, supervisor, origin, created_at)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (todo_id, pr_url) DO NOTHING`,
				id, todoID, prURL, supervisor, origin, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ListPRFollowups reads every active post-closure subscription, oldest creation first so a long-idle one is visible
// to the sweep and the daily briefing.
func (d *Store) ListPRFollowups(ctx context.Context) ([]repository.PRFollowup, error) {
	var out []repository.PRFollowup
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+followupColumns+` FROM todo_pr_followups ORDER BY created_at ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanPRFollowup(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// SavePRFollowupObservation persists one sweep's observed facts and notification dedup heads, refusing to overwrite
// with an older observation. It returns whether a row was updated.
func (d *Store) SavePRFollowupObservation(ctx context.Context, f repository.PRFollowup) (bool, error) {
	if f.LastObservedAt == nil {
		return false, repository.Problem("invalid")
	}
	var updated bool
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE todo_pr_followups SET
			head_oid=$2, state=$3, review=$4, changes_requested_at=$5, last_observed_at=$6,
			notified_conflict_head=$7, notified_review_head=$8
			WHERE followup_id=$1 AND (last_observed_at IS NULL OR last_observed_at <= $6)`,
			f.ID, f.HeadOID, f.State, f.Review, f.ChangesRequestedAt, f.LastObservedAt,
			f.NotifiedConflictHead, f.NotifiedReviewHead)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() > 0
		return nil
	})
	return updated, err
}

// DeletePRFollowup removes a subscription once its PR reaches a terminal state, or when its to-do is gone.
func (d *Store) DeletePRFollowup(ctx context.Context, id string) error {
	return d.Transaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM todo_pr_followups WHERE followup_id=$1`, id)
		return err
	})
}

// StalePRFollowups returns subscriptions whose PR has shown no terminal change and whose last observation (or
// creation, when never polled) is older than before, for the daily briefing. A forge that cannot read the PR is
// excluded by the caller, not here.
func (d *Store) StalePRFollowups(ctx context.Context, before time.Time) ([]repository.PRFollowup, error) {
	var out []repository.PRFollowup
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+followupColumns+`
			FROM todo_pr_followups
			WHERE COALESCE(last_observed_at, created_at) < $1 ORDER BY created_at ASC`, before)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanPRFollowup(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}
