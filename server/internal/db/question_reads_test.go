package db

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// Read marks of a chat's answers (#13021): the first read records when marks began and later reads keep it; marks
// are per session, a mark made twice is kept once, and the newest come first.
func TestQuestionReads(t *testing.T) {
	d, ctx := open(t), context.Background()
	// open does not clear these tables: sessions of this run's own
	main, other := fmt.Sprint("main-", time.Now().UnixNano()), fmt.Sprint("other-", time.Now().UnixNano())
	since, ids, err := d.QuestionReads(ctx, "server", main)
	if err != nil || since.IsZero() || len(ids) != 0 {
		t.Fatalf("first read = %v, %v, %v", since, ids, err)
	}
	again, _, err := d.QuestionReads(ctx, "server", main)
	if err != nil || !again.Equal(since) {
		t.Fatalf("since moved: %v then %v (%v)", since, again, err)
	}
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := d.MarkQuestionsRead(ctx, "server", main, []string{"u1", "u2"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkQuestionsRead(ctx, "server", main, []string{"u3", "u1"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkQuestionsRead(ctx, "server", other, []string{"x1"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkQuestionsRead(ctx, "server", main, nil, t0); err != nil {
		t.Fatal(err)
	}
	_, ids, err = d.QuestionReads(ctx, "server", main)
	if err != nil || !slices.Equal(ids, []string{"u3", "u1", "u2"}) {
		t.Fatalf("main = %v, %v", ids, err)
	}
	if _, ids, err := d.QuestionReads(ctx, "laptop", main); err != nil || len(ids) != 0 {
		t.Fatalf("another computer's session = %v, %v", ids, err)
	}
}
