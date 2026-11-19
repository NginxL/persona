package assistant

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// settingsPrefixStore reads every raw setting under prefix. Model migrations require the complete set, including
// records whose purpose is not in the current extension catalog.
type settingsPrefixStore interface {
	SettingsPrefix(ctx context.Context, prefix string) (map[string]json.RawMessage, error)
}

// Store is where the assistant keeps its state: the hub's Postgres (internal/db), the same settings keys as before
// (assistant.titles, assistant.archived, assistant.relay, assistant.tasks, assistant.runs, tasks.trust) and the same
// tables (task_plans, assistant_feed), so nothing is migrated. Only the assistant reads and writes them.
type Store interface {
	// Setting reads key into out; false when it was never written.
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error

	// The plans the assistant handed the user (task_plan).
	CreateTaskPlan(ctx context.Context, p db.TaskPlanRow) error
	TaskPlan(ctx context.Context, id string) (*db.TaskPlanRow, error) // db.ErrTaskPlanNotFound
	// TaskPlansByID reads the plans with ids in one query; an id with no plan is left out.
	TaskPlansByID(ctx context.Context, ids []string) ([]db.TaskPlanRow, error)
	TaskPlans(ctx context.Context, conversation string) ([]db.TaskPlanRow, error)
	// UpdateTaskPlan moves plan id from state from to to with tasks; false when it was not in from.
	UpdateTaskPlan(ctx context.Context, id, from, to string, tasks json.RawMessage, at time.Time) (bool, error)

	// A conversation's feed: what the assistant put into it besides the transcript.
	AppendFeed(ctx context.Context, f db.FeedRow) error
	Feed(ctx context.Context, conversation string, since time.Time, limit int) ([]db.FeedRow, error)
	// FeedItem is one item by its id; nil when there is none.
	FeedItem(ctx context.Context, id string) (*db.FeedRow, error)
	// MarkFeedSent merges patch into the bodies of the kind / ref items put in by at that do not say sent_at yet; the
	// items it changed.
	MarkFeedSent(ctx context.Context, kind, ref string, at time.Time, patch json.RawMessage) ([]db.FeedRow, error)
	// FeedByRef lists the items of kinds about ref in every conversation, newest first, at most limit: a to-do's
	// history (todo_timeline.go). It refuses message cards (db.ErrFeedByRefMessage).
	FeedByRef(ctx context.Context, ref string, kinds []string, limit int) ([]db.FeedRow, error)
}
