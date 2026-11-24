package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/mailwire"
)

// brokenMail is slowMail whose accounts in errs fail with their own error. Once a read of it failed, an account in
// refuse shows in the accounts' list as refusing its login (Status "error", as the mail service's health says: the
// way a password changed elsewhere first shows up in a round), and one in gone is no longer listed (deleted while the
// round read it).
type brokenMail struct {
	slowMail
	errs   map[string]error
	refuse map[string]bool
	gone   map[string]bool
	mu     sync.Mutex
	tried  map[string]bool
}

func (m *brokenMail) ListAccounts() ([]mailwire.MailAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mailwire.MailAccount, 0, len(m.accounts))
	for _, a := range m.accounts {
		a.Status = "ok"
		switch {
		case m.gone[a.ID] && m.tried[a.ID]:
			continue
		case m.refuse[a.ID] && m.tried[a.ID]:
			a.Status, a.Error = "error", "the server refused the login"
		}
		out = append(out, a)
	}
	return out, nil
}

func (m *brokenMail) ListMessages(ctx context.Context, opts mailwire.ListOptions) (*mailwire.ListResult, error) {
	if err := m.errs[opts.AccountID]; err != nil {
		m.mu.Lock()
		if m.tried == nil {
			m.tried = map[string]bool{}
		}
		m.tried[opts.AccountID] = true
		m.mu.Unlock()
		return nil, err
	}
	return m.slowMail.ListMessages(ctx, opts)
}

// mailRoundWire is a round's mail as the wire carries it to the assistant.
type mailRoundWire struct {
	Mail *struct {
		Accounts []struct {
			ID string `json:"id"`
		} `json:"accounts"`
		Failed []struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			ReadError string `json:"read_error"`
		} `json:"failed"`
	} `json:"mail"`
}

func newBrokenMailHub(t *testing.T, m Mail) *Manager {
	t.Helper()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.mail = m
	if err := h.addConnector(mailConnector{h: h}); err != nil {
		t.Fatal(err)
	}
	return h
}

// A mail account a full round could not read is named in the round's mail with a short code, as a connector's
// failed Collect is (#1849): it used to be only logged and left out, so the inbox showed its last mail as current and
// nothing said it was behind. A timeout (the round's, the network's, the mail service's own) is "timeout"; an account
// the mail settings show as refusing its login is "login_refused"; anything else "failed"; one deleted meanwhile is
// not named. The error's words never go on the wire. The account stays out of accounts, so an assistant from before
// this reads the round as it always did.
func TestMailRoundSaysWhichAccountCouldNotBeRead(t *testing.T) {
	m := &brokenMail{slowMail: slowMail{accounts: []mailwire.MailAccount{
		{ID: "a", Email: "a@example.com"}, {ID: "b", Email: "b@example.com"}, {ID: "c", Email: "c@example.com"},
		{ID: "d", Email: "d@example.com"}, {ID: "e", Email: "e@example.com"}, {ID: "f", Email: "f@example.com"},
		{ID: "g", Email: "g@example.com"},
	}}, errs: map[string]error{
		"b": context.DeadlineExceeded,
		"c": &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded},
		"d": errors.New("邮箱服务器拒绝了登录：user me@example.com, password hunter2"),
		"e": errors.New("select INBOX: imaps://me:hunter2@imap.example.com: broken pipe"),
		"f": mailwire.ErrTimedOut,
		"g": unknownMailAccount("g"),
	}, refuse: map[string]bool{"d": true}, gone: map[string]bool{"g": true}}
	h := newBrokenMailHub(t, m)

	raw, err := json.Marshal(h.readInbox(context.Background(), roundFull))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("an error's words went on the wire: %s", raw)
	}
	var r mailRoundWire
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Mail == nil {
		t.Fatalf("no mail in a full round: %s", raw)
	}
	var read []string
	for _, a := range r.Mail.Accounts {
		read = append(read, a.ID)
	}
	if join(read) != "a" {
		t.Fatalf("accounts = %s, want only the one read (an older assistant reads these as before)", join(read))
	}
	want := map[string]string{"b": assistantwire.ReadTimeout, "c": assistantwire.ReadTimeout, "d": assistantwire.ReadLoginRefused,
		"e": assistantwire.ReadFailed, "f": assistantwire.ReadTimeout}
	got := map[string]string{}
	for _, f := range r.Mail.Failed {
		if f.Email != f.ID+"@example.com" {
			t.Fatalf("failed account %s has the address %q", f.ID, f.Email)
		}
		got[f.ID] = f.ReadError
	}
	if len(got) != len(want) {
		t.Fatalf("failed accounts = %v, want %v (round: %s)", got, want, raw)
	}
	for id, code := range want {
		if got[id] != code {
			t.Fatalf("account %s: read_error %q, want %q (round: %s)", id, got[id], code, raw)
		}
	}
}

// When every account is read the round's mail is what it was before failed accounts were named (no "failed" key), and
// one whose accounts' status can't be listed again still names the account that failed, as "failed".
func TestMailRoundWithoutFailuresIsUnchanged(t *testing.T) {
	m := &brokenMail{slowMail: slowMail{accounts: []mailwire.MailAccount{{ID: "a", Email: "a@example.com"}}}}
	raw, err := json.Marshal(newBrokenMailHub(t, m).readMail(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"failed"`) {
		t.Fatalf("a round that read every account names failed ones: %s", raw)
	}

	h := newBrokenMailHub(t, &unlistedMail{brokenMail: brokenMail{slowMail: slowMail{accounts: []mailwire.MailAccount{{ID: "b", Email: "b@example.com"}}},
		errs: map[string]error{"b": errors.New("broken pipe")}}})
	r := h.readMail(context.Background())
	if r == nil || len(r.Failed) != 1 || r.Failed[0].ID != "b" || r.Failed[0].ReadError != assistantwire.ReadFailed {
		t.Fatalf("the accounts' status could not be listed again: %+v", r)
	}
}

// unlistedMail lists its accounts once; listing them again fails.
type unlistedMail struct {
	brokenMail
	listed bool
}

func (m *unlistedMail) ListAccounts() ([]mailwire.MailAccount, error) {
	if m.listed {
		return nil, errors.New("the store is gone")
	}
	m.listed = true
	return m.brokenMail.ListAccounts()
}

// A held round (the manager was away) keeps the newest mail round's failed accounts: a mail round over it that could
// not read an account names it, with the mail an earlier round read of it still in accounts; a round without mail
// keeps the held round's; a mail round that read every account clears them.
func TestHeldRoundKeepsWhichMailAccountsFailed(t *testing.T) {
	msg := func(uid uint32) []assistantwire.InboxMailSummary {
		return []assistantwire.InboxMailSummary{{Mailbox: "INBOX", UID: uid}}
	}
	timedOut := []assistantwire.InboxMailFailed{{ID: "b", Email: "b@example.com", ReadError: assistantwire.ReadTimeout}}
	names := map[string]string{"mail": "Mail"}
	old := assistantwire.InboxRound{Round: assistantwire.RoundFull, Names: names, Mail: &assistantwire.InboxMailRead{
		Accounts: []assistantwire.InboxMailAccount{{ID: "a", Messages: msg(1)}, {ID: "b", Messages: msg(1)}}}}
	n := assistantwire.InboxRound{Round: assistantwire.RoundFull, Names: names, Mail: &assistantwire.InboxMailRead{
		Accounts: []assistantwire.InboxMailAccount{{ID: "a", Messages: msg(2)}}, Failed: timedOut}}
	got := mergeRounds(old, n)
	if got.Mail == nil || len(got.Mail.Accounts) != 2 || got.Mail.Accounts[1].ID != "b" || len(got.Mail.Accounts[1].Messages) != 1 ||
		!slices.Equal(got.Mail.Failed, timedOut) {
		t.Fatalf("a mail round that could not read b over a held one: %+v", got.Mail)
	}

	poll := assistantwire.InboxRound{Round: assistantwire.RoundPoll, Names: names}
	if got := mergeRounds(got, poll); got.Mail == nil || !slices.Equal(got.Mail.Failed, timedOut) {
		t.Fatalf("a round without mail over the held one: %+v", got.Mail)
	}

	again := assistantwire.InboxRound{Round: assistantwire.RoundFull, Names: names, Mail: &assistantwire.InboxMailRead{
		Accounts: []assistantwire.InboxMailAccount{{ID: "a", Messages: msg(2)}, {ID: "b", Messages: msg(3)}}}}
	if got := mergeRounds(got, again); got.Mail == nil || len(got.Mail.Failed) != 0 {
		t.Fatalf("a mail round that read every account still names failed ones: %+v", got.Mail)
	}
}
