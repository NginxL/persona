package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

const newestMessageThread = "example-org/example-repo#12"

func newestMessageFixture(t *testing.T) (*fakeNotifications, *Connector) {
	t.Helper()
	f := notificationsFixture()
	c := f.server(t)
	return f, c
}

func replaceNewestMessageData(t *testing.T, f *fakeNotifications, c *Connector, mutate func()) {
	t.Helper()
	f.mu.Lock()
	mutate()
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll refreshed fixture: %v", err)
	}
}

func newestMessageThing(t *testing.T, c *Connector) (got map[string]connectorwire.InboxThing) {
	t.Helper()
	return things(t, c)
}

func TestNewestDistinctCommentsInSameSecond(t *testing.T) {
	f, c := newestMessageFixture(t)
	at := fixtureAt(9*time.Hour + 5*time.Minute)
	f.mu.Lock()
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[
		{"id":101,"user":{"login":"alex"},"body":"First synthetic comment.","created_at":"` + at + `"}]`
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := newestMessageThing(t, c)[newestMessageThread]
	if before.Last == "" || before.Preview != "First synthetic comment." {
		t.Fatalf("initial comment was not observed: %+v", before)
	}

	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[
			{"id":101,"user":{"login":"alex"},"body":"First synthetic comment.","created_at":"` + at + `"},
			{"id":102,"user":{"login":"blair"},"body":"Second synthetic comment.","created_at":"` + at + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, t2) + "]"
	})
	after := newestMessageThing(t, c)[newestMessageThread]
	if after.Last == before.Last {
		t.Fatalf("same-second distinct comment did not advance Last: %q", before.Last)
	}
	if after.Preview != "Second synthetic comment." {
		t.Fatalf("same-second comment id 102 did not win Preview: %+v", after)
	}
}

func TestNewestDistinctSubmittedReviewsInSameSecond(t *testing.T) {
	f, c := newestMessageFixture(t)
	at := fixtureAt(9*time.Hour + 6*time.Minute)
	f.mu.Lock()
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[]`
	f.comments["/repos/example-org/example-repo/pulls/12/reviews"] = `[
		{"id":201,"user":{"login":"alex"},"state":"COMMENTED","body":"First synthetic review.","submitted_at":"` + at + `"}]`
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := newestMessageThing(t, c)[newestMessageThread]
	if before.Last == "" || before.Preview != "\u8bc4\u5ba1\u4e86 First synthetic review." {
		t.Fatalf("initial review was not observed: %+v", before)
	}

	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/pulls/12/reviews"] = `[
			{"id":201,"user":{"login":"alex"},"state":"COMMENTED","body":"First synthetic review.","submitted_at":"` + at + `"},
			{"id":202,"user":{"login":"blair"},"state":"COMMENTED","body":"Second synthetic review.","submitted_at":"` + at + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, t2) + "]"
	})
	after := newestMessageThing(t, c)[newestMessageThread]
	if after.Last == before.Last {
		t.Fatalf("same-second distinct review did not advance Last: %q", before.Last)
	}
	if after.Preview != "\u8bc4\u5ba1\u4e86 Second synthetic review." {
		t.Fatalf("same-second review id 202 did not win Preview: %+v", after)
	}
}

func TestDeletingNewestCommentPreservesLast(t *testing.T) {
	f, c := newestMessageFixture(t)
	older, newest := fixtureAt(9*time.Hour+2*time.Minute), fixtureAt(9*time.Hour+3*time.Minute)
	f.mu.Lock()
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[
		{"id":301,"user":{"login":"alex"},"body":"Older synthetic comment.","created_at":"` + older + `"},
		{"id":302,"user":{"login":"blair"},"body":"Newest synthetic comment.","created_at":"` + newest + `"}]`
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := newestMessageThing(t, c)[newestMessageThread]
	if before.Last == "" || before.Preview != "Newest synthetic comment." {
		t.Fatalf("initial newest comment was not observed: %+v", before)
	}

	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[{"id":301,"user":{"login":"alex"},"body":"Older synthetic comment.","created_at":"` + older + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, fixtureAt(9*time.Hour+4*time.Minute)) + "]"
	})
	after := newestMessageThing(t, c)[newestMessageThread]
	if after.Last != before.Last {
		t.Fatalf("deleting newest comment changed Last: %q \u2192 %q", before.Last, after.Last)
	}
}

func TestDeletingAllCommentsPreservesLast(t *testing.T) {
	f, c := newestMessageFixture(t)
	commentAt := fixtureAt(9*time.Hour + 7*time.Minute)
	f.mu.Lock()
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[{"id":401,"user":{"login":"alex"},"body":"Synthetic comment to remove.","created_at":"` + commentAt + `"}]`
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := newestMessageThing(t, c)[newestMessageThread]
	if before.Last == "" {
		t.Fatalf("initial comment was not observed: %+v", before)
	}
	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, fixtureAt(9*time.Hour+8*time.Minute)) + "]"
	})
	after := newestMessageThing(t, c)[newestMessageThread]
	if after.Last != before.Last {
		t.Fatalf("deleting all comments changed Last: %q \u2192 %q", before.Last, after.Last)
	}
}

func TestDismissingLatestReviewPreservesLast(t *testing.T) {
	f, c := newestMessageFixture(t)
	older, latest := fixtureAt(9*time.Hour+9*time.Minute), fixtureAt(9*time.Hour+10*time.Minute)
	f.mu.Lock()
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[{"id":501,"user":{"login":"alex"},"body":"Earlier synthetic comment.","created_at":"` + older + `"}]`
	f.comments["/repos/example-org/example-repo/pulls/12/reviews"] = `[{"id":502,"user":{"login":"blair"},"state":"CHANGES_REQUESTED","body":"Synthetic review to dismiss.","submitted_at":"` + latest + `"}]`
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := newestMessageThing(t, c)[newestMessageThread]
	if before.Last == "" || !strings.Contains(before.Preview, "Synthetic review to dismiss.") {
		t.Fatalf("initial latest review was not observed: %+v", before)
	}
	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/pulls/12/reviews"] = `[{"id":502,"user":{"login":"blair"},"state":"DISMISSED","body":"Synthetic review to dismiss.","submitted_at":"` + latest + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, fixtureAt(9*time.Hour+11*time.Minute)) + "]"
	})
	after := newestMessageThing(t, c)[newestMessageThread]
	if after.Last != before.Last {
		t.Fatalf("dismissing latest review changed Last: %q \u2192 %q", before.Last, after.Last)
	}
}

func TestCommentInSameSecondAsReviewAdvancesLastAndPreview(t *testing.T) {
	f, c := newestMessageFixture(t)
	at := fixtureAt(9*time.Hour + 12*time.Minute)
	f.mu.Lock()
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[]`
	f.comments["/repos/example-org/example-repo/pulls/12/reviews"] = `[{"id":601,"user":{"login":"alex"},"state":"COMMENTED","body":"Synthetic review in shared second.","submitted_at":"` + at + `"}]`
	f.mu.Unlock()
	c.inbox.fullAt = time.Time{}
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := newestMessageThing(t, c)[newestMessageThread]
	if before.Last == "" {
		t.Fatalf("initial review was not observed: %+v", before)
	}
	replaceNewestMessageData(t, f, c, func() {
		f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[{"id":602,"user":{"login":"blair"},"body":"New synthetic comment in same second.","created_at":"` + at + `"}]`
		f.notifications = "[" + note("1", "author", "PullRequest", "https://api.github.com"+api12, fixtureAt(9*time.Hour+13*time.Minute)) + "]"
	})
	after := newestMessageThing(t, c)[newestMessageThread]
	if after.Last == before.Last {
		t.Fatalf("same-second new comment did not advance Last: %q", after.Last)
	}
	if after.Preview != "New synthetic comment in same second." {
		t.Fatalf("same-second comment did not win Preview: %+v", after)
	}
}
