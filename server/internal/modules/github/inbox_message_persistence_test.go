package github

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeletedNewestMessageStaysConsumedAfterRestart(t *testing.T) {
	f, c := newestMessageFixture(t)
	store := &memSecrets{}
	c.UseSecrets(store)
	at := fixtureAt(9 * time.Hour)
	later := fixtureAt(9*time.Hour + time.Minute)
	comment := `{"id":101,"user":{"login":"alex"},"body":"Earlier synthetic comment.","created_at":"` + at + `"}`
	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[` + comment +
			`,{"id":102,"user":{"login":"blair"},"body":"Deleted synthetic comment.","created_at":"` + later + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, t1) + "]"
	})
	before := things(t, c)[newestMessageThread]
	restarted := f.server(t)
	restarted.UseSecrets(store)
	restarted.Reload()
	restarted.check(context.Background())
	replaceNewestMessageData(t, f, restarted, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[` + comment + `]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, t2) + "]"
	})
	if after := things(t, restarted)[newestMessageThread]; after.Last != before.Last {
		t.Fatalf("restart replayed a deleted message: %q -> %q", before.Last, after.Last)
	}
}

type messageSaveFailure struct {
	memSecrets
	fail bool
}

func (s *messageSaveFailure) Save(ctx context.Context, raw []byte) error {
	if s.fail {
		return errors.New("synthetic cursor save failure")
	}
	return s.memSecrets.Save(ctx, raw)
}

func TestMessageCursorSaveFailureRetriesBeforePublishing(t *testing.T) {
	f, c := newestMessageFixture(t)
	store := &messageSaveFailure{fail: true}
	c.UseSecrets(store)
	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] =
			`[{"id":101,"user":{"login":"alex"},"body":"Synthetic comment.","created_at":"` + fixtureAt(9*time.Hour) + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, t1) + "]"
	})
	if got := things(t, c); len(got) != 0 {
		t.Fatalf("unpersisted message was published: %+v", got)
	}
	store.fail = false
	replaceNewestMessageData(t, f, c, func() {})
	if got := things(t, c); got[newestMessageThread].Last == "" {
		t.Fatalf("notification was not retried after storage recovered: %+v", got)
	}
}
