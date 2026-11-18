package assistant

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A transient read gets background recovery rounds before the apps show a notice.
// Only mail rounds count; one good read starts a fresh episode for that account.
func TestInboxTransientMailNoticeRecovery(t *testing.T) {
	var b inboxBook
	names := map[string]string{"mail": "Mail"}
	failed := func(code string) assistantwire.InboxRound {
		return assistantwire.InboxRound{Names: names, Mail: &assistantwire.InboxMailRead{Failed: []assistantwire.InboxMailFailed{
			{ID: "work", Email: "work@example.com", ReadError: code},
		}}}
	}
	for i, code := range []string{assistantwire.ReadTimeout, assistantwire.ReadFailed, assistantwire.ReadRateLimited} {
		changed := b.recordReads(failed(code))
		want := 0
		if i == 2 {
			want = 1
		}
		if got := len(b.mailReadErrs); got != want || changed != (i == 2) {
			t.Fatalf("failure %d: notices=%d changed=%v", i+1, got, changed)
		}
		if b.recordReads(assistantwire.InboxRound{Names: names}) {
			t.Fatal("a non-mail round changed the mail notice")
		}
	}
	if b.mailReadErrs["work"].ReadError != assistantwire.ReadRateLimited {
		t.Fatal("the notice does not carry the latest failure")
	}
	if !b.recordReads(assistantwire.InboxRound{Names: names, Mail: &assistantwire.InboxMailRead{}}) || len(b.mailReadErrs) != 0 {
		t.Fatal("recovery did not immediately clear the notice")
	}
	if b.recordReads(failed(assistantwire.ReadTimeout)) || len(b.mailReadErrs) != 0 {
		t.Fatal("a fresh episode immediately showed a notice")
	}
	if !b.recordReads(failed(assistantwire.ReadLoginRefused)) || b.mailReadErrs["work"].ReadError != assistantwire.ReadLoginRefused {
		t.Fatal("a refused login was not shown immediately")
	}
	b.recordReads(failed(assistantwire.ReadTimeout))
	if len(b.mailReadErrs) != 1 {
		t.Fatal("another failed read cleared an already visible notice")
	}
}

func TestInboxTransientMailNoticeAccountsAndRemoval(t *testing.T) {
	var b inboxBook
	r := assistantwire.InboxRound{Names: map[string]string{"mail": "Mail"}, Mail: &assistantwire.InboxMailRead{Failed: []assistantwire.InboxMailFailed{
		{ID: "first", ReadError: assistantwire.ReadTimeout},
	}}}
	b.recordReads(r)
	b.recordReads(r)
	r.Mail.Failed = append(r.Mail.Failed, assistantwire.InboxMailFailed{ID: "second", ReadError: assistantwire.ReadTimeout})
	b.recordReads(r)
	if len(b.mailReadErrs) != 1 || b.mailReadErrs["first"].ID != "first" {
		t.Fatalf("failure counts leaked between accounts: %+v", b.mailReadErrs)
	}
	// Removing an account from a complete mail round resets its pending failures.
	r.Mail.Failed = r.Mail.Failed[:1]
	b.recordReads(r)
	r.Mail.Failed = r.Mail.Failed[0:0]
	r.Mail.Failed = append(r.Mail.Failed, assistantwire.InboxMailFailed{ID: "second", ReadError: assistantwire.ReadTimeout})
	b.recordReads(r)
	if len(b.mailReadErrs) != 0 {
		t.Fatal("a removed account retained its pending failures")
	}
	b.recordReads(r)
	b.recordReads(assistantwire.InboxRound{Names: map[string]string{}})
	if b.recordReads(r) || len(b.mailReadErrs) != 0 {
		t.Fatal("removing mail did not reset its pending failures")
	}
}

// The real collection/API path suppresses the first two notices, tells clients at
// the threshold without new mail, and clears immediately when the account recovers.
func TestInboxTransientMailNoticeCollection(t *testing.T) {
	h := newInboxHub(t)
	failed := `{"epoch":"current","seq":1,"round":"full","sources":[],"names":{"mail":"Mail"},` +
		`"mail":{"accounts":[],"failed":[{"id":"work","email":"work@example.com","read_error":"timeout"}]}}`
	for range 3 {
		collectedJSON(t, h, failed) // Replayed rounds must not advance the failure count.
	}
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 0 {
		t.Fatal("a single failed round, replayed, showed a notice")
	}
	collectedJSON(t, h, `{"epoch":"current","seq":2,"round":"full","sources":[],"names":{"mail":"Mail"},`+
		`"mail":{"accounts":[],"failed":[{"id":"work","email":"work@example.com","read_error":"timeout"}]}}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 0 {
		t.Fatal("the second failed round showed a notice")
	}
	h.in.mu.Lock()
	before := len(h.in.changed)
	h.in.mu.Unlock()
	collectedJSON(t, h, `{"epoch":"current","seq":3,"round":"full","sources":[],"names":{"mail":"Mail"},`+
		`"mail":{"accounts":[],"failed":[{"id":"work","email":"work@example.com","read_error":"timeout"}]}}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 1 || tr.Sources[0].ReadError != assistantwire.ReadTimeout {
		t.Fatalf("the third failed round did not show its notice: %+v", tr)
	}
	h.in.mu.Lock()
	after := len(h.in.changed)
	h.in.mu.Unlock()
	if after == before {
		t.Fatal("clients were not told the notice appeared")
	}
	collectedJSON(t, h, `{"epoch":"current","seq":4,"round":"full","sources":[],"names":{"mail":"Mail"},`+
		`"mail":{"accounts":[{"id":"work","email":"work@example.com","messages":[]}]}}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 0 {
		t.Fatalf("a successful read did not clear the API notice: %+v", tr)
	}
	h.in.mu.Lock()
	cleared := len(h.in.changed)
	h.in.mu.Unlock()
	if cleared == after {
		t.Fatal("clients were not told the notice cleared")
	}
}
