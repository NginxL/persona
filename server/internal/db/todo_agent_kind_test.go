package db

import (
	"context"
	"testing"
	"time"
)

// Relinking does not erase a known kind with an empty update, while a newer known kind replaces the old snapshot.
func TestTodoAgentRelinkKindPreservationAndReplacement(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "kind-relink", Title: "Keep the Agent kind", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	link := func(agent string, at time.Time) {
		t.Helper()
		if _, err := d.LinkTodoAgentWithSource(ctx, "kind-relink", "box", "s1", "worker", "/code/app", agent, "", "worker", "parent", at); err != nil {
			t.Fatal(err)
		}
		rows, err := d.ListAllTodoAgents(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("agent rows = %+v, %v", rows, err)
		}
		if rows[0].Agent != agent && agent != "" {
			t.Fatalf("stored agent = %q, want replacement %q", rows[0].Agent, agent)
		}
		if agent == "" && rows[0].Agent != "codex" {
			t.Fatalf("empty relink replaced the stored kind with %q, want codex preserved", rows[0].Agent)
		}
	}
	link("codex", now)
	link("", now.Add(time.Minute))
	link("gemini", now.Add(2*time.Minute))
}

// reviewerKindStore is the store API needed to persist the independent reviewer's start answer. Asserting the narrow
// interface keeps this a behavioral failure on a DB whose reviewer insert still hard-codes an empty kind.
type reviewerKindStore interface {
	LinkTodoReviewer(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, via string, at time.Time) error
}

func TestTodoReviewerLinkRecordsAgentKind(t *testing.T) {
	d, ctx := open(t), context.Background()
	store, ok := any(d).(reviewerKindStore)
	if !ok {
		t.Fatal("LinkTodoReviewer has no agent parameter, so the reviewer's kind cannot be persisted")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "review-kind", Title: "Review the change", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkTodoReviewer(ctx, "review-kind", "box", "reviewer", "review", "/wt/review", "codex", "rv_1", now); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListAllTodoAgents(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("agent rows = %+v, %v", rows, err)
	}
	row := rows[0]
	if row.Source != "review" || row.Via != "rv_1" || row.Agent != "codex" || row.ReleasedAt == nil {
		t.Fatalf("reviewer row = %+v, want source review, via rv_1, agent codex, released", row)
	}
}
