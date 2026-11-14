package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A mail the owner replied to from a mail client carries IMAP \Answered: its item is filed as replied. No Postgres.

// markMail sets the flags of UID 1 on the fake account's page.
func (h *inboxHub) markMail(seen, answered bool) {
	h.in.mu.Lock()
	defer h.in.mu.Unlock()
	msgs := h.in.mail.accounts[0].Messages
	for i := range msgs {
		if msgs[i].UID == 1 {
			msgs[i].Seen, msgs[i].Answered = seen, answered
		}
	}
}

func (h *inboxHub) chainKinds(it *InboxItem) []string {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	var kinds []string
	for _, e := range it.Chain {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func hasKind(kinds []string, k string) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func TestInboxMailAnsweredElsewhereIsFiledAsReplied(t *testing.T) {
	h, it := offPageHub(t, TierAction, time.Now().Add(-time.Hour))
	h.markMail(true, true)
	h.collectInbox(context.Background(), roundFull)
	if open, read, by := h.itemState(it); open || !read || by != "replied" {
		t.Fatalf("answered elsewhere: open %v, read %v, done_by %q; want filed as replied", open, read, by)
	}
	if kinds := h.chainKinds(it); !hasKind(kinds, ChainSelfReplied) {
		t.Fatalf("chain %v has no self_replied entry", kinds)
	}
}

func TestInboxMailSeenOnlyKeepsReadElsewhere(t *testing.T) {
	h, it := offPageHub(t, TierAction, time.Now().Add(-time.Hour))
	h.markMail(true, false)
	h.collectInbox(context.Background(), roundFull)
	if open, read, by := h.itemState(it); !open || !read || by != "" {
		t.Fatalf("Needs you, seen only: open %v, read %v, done_by %q; want open and read", open, read, by)
	}
	h2, it2 := offPageHub(t, TierFYI, time.Now().Add(-time.Hour))
	h2.markMail(true, false)
	h2.collectInbox(context.Background(), roundFull)
	if open, _, by := h2.itemState(it2); open || by != "read" {
		t.Fatalf("FYI, seen only: open %v, done_by %q; want filed as read", open, by)
	}
}

func TestInboxMailAnsweredButHandledStays(t *testing.T) {
	h, it := offPageHub(t, TierAction, time.Now().Add(-time.Hour))
	h.inbox.mu.Lock()
	it.Handling = &InboxHandling{State: HandlingWaiting}
	h.inbox.mu.Unlock()
	h.markMail(true, true)
	h.collectInbox(context.Background(), roundFull)
	if open, _, by := h.itemState(it); !open || by != "" {
		t.Fatalf("handled: open %v, done_by %q; want left open", open, by)
	}
}

// A newer mail of the thread, still unread, came after the one the owner answered: the item stays open for it.
func TestInboxMailAnsweredThenNewMailStays(t *testing.T) {
	h, it := offPageHub(t, TierAction, time.Now().Add(-time.Hour))
	h.markMail(true, true)
	h.in.mu.Lock()
	acc := &h.in.mail.accounts[0]
	acc.Messages = append([]assistantwire.InboxMailSummary{{Mailbox: "INBOX", UID: 4, Subject: "Re: Weekly digest",
		From: MailAddress{Name: "News", Email: "news@example.com"}, Date: time.Now().Format(time.RFC3339)}}, acc.Messages...)
	h.in.mu.Unlock()
	h.collectInbox(context.Background(), roundFull)
	if open, _, by := h.itemState(it); !open || by != "" {
		t.Fatalf("answered, then new mail: open %v, done_by %q; want left open", open, by)
	}
}

// Reopened by hand after being filed on an answer, the item is not filed again on the same answer.
func TestInboxMailAnsweredReopenedStays(t *testing.T) {
	h, it := offPageHub(t, TierAction, time.Now().Add(-time.Hour))
	h.markMail(true, true)
	h.collectInbox(context.Background(), roundFull)
	if open, _, by := h.itemState(it); open || by != "replied" {
		t.Fatalf("not filed: open %v, done_by %q", open, by)
	}
	h.inbox.mu.Lock()
	it.DoneAt, it.DoneBy = nil, ""
	h.inbox.mu.Unlock()
	h.collectInbox(context.Background(), roundFull)
	if open, _, by := h.itemState(it); !open || by != "" {
		t.Fatalf("reopened, filed again: open %v, done_by %q", open, by)
	}
}
