package mail

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// newServiceWithAccount builds a Service whose only account points at the test
// IMAP server.
func newServiceWithAccount(t *testing.T, srv *testIMAP) *Service {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, newMemRecords())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	a := srv.testAccount()
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()
	if err := s.store.save([]*MailAccount{a}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	return s
}

func TestListMessagesAndPaging(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	subjects := []string{"One", "Two", "Three", "Four", "Five"}
	for _, subj := range subjects {
		srv.appendMessage(t, "INBOX", rawMessage(subj, "张三 <zhang@example.com>", "Body of "+subj+"\r\nsecond line"))
	}

	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Limit: 3})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if got := len(res.Messages); got != 3 {
		t.Fatalf("len = %d, want 3", got)
	}
	if !res.HasMore {
		t.Fatal("has_more should be true when there are older messages")
	}
	// Newest first.
	if res.Messages[0].Subject != "Five" || res.Messages[2].Subject != "Three" {
		t.Fatalf("unexpected order: %v", subjectsOf(res.Messages))
	}
	first := res.Messages[0]
	if first.From.Name != "张三" || first.From.Email != "zhang@example.com" {
		t.Fatalf("from = %+v", first.From)
	}
	if first.AccountID != "acc_test" || first.Mailbox != "INBOX" || first.UID == 0 {
		t.Fatalf("summary header = %+v", first)
	}
	if first.Snippet != "Body of Five second line" {
		t.Fatalf("snippet = %q", first.Snippet)
	}
	if !strings.HasPrefix(first.Date, "2006-01-02T") && first.Date == "" {
		t.Fatalf("date = %q", first.Date)
	}

	// Page two uses before_uid of the oldest row on page one.
	oldest := res.Messages[len(res.Messages)-1].UID
	page2, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Limit: 3, BeforeUID: oldest})
	if err != nil {
		t.Fatalf("ListMessages page 2: %v", err)
	}
	if got := subjectsOf(page2.Messages); len(got) != 2 || got[0] != "Two" || got[1] != "One" {
		t.Fatalf("page 2 = %v, want [Two One]", got)
	}
	if page2.HasMore {
		t.Fatal("page 2 should not have more")
	}
	// The mailbox's UIDVALIDITY comes with every page, so a cursor of UIDs can tell when the mailbox was renumbered.
	if res.UIDValidity == 0 || page2.UIDValidity != res.UIDValidity {
		t.Fatalf("uid_validity = %d, page 2 %d", res.UIDValidity, page2.UIDValidity)
	}
}

func TestListMessagesSearch(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	srv.appendMessage(t, "INBOX", rawMessage("Invoice", "billing@example.com", "Please pay the invoice"))
	srv.appendMessage(t, "INBOX", rawMessage("Lunch", "friend@example.com", "See you at noon"))
	srv.appendMessage(t, "INBOX", rawMessage("Newsletter", "news@example.com", "Weekly digest"))

	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "invoice"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Subject != "Invoice" {
		t.Fatalf("search results = %v", subjectsOf(res.Messages))
	}

	res, err = s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "Lunch"})
	if err != nil {
		t.Fatalf("search subject: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Subject != "Lunch" {
		t.Fatalf("subject search results = %v", subjectsOf(res.Messages))
	}

	res, err = s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "nothing-matches-this"})
	if err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if len(res.Messages) != 0 || res.HasMore {
		t.Fatalf("empty search = %+v", res)
	}
}

// A server that searches gives its own answer: the headers are not matched locally on top of it.
func TestListMessagesSearchUsesServerHits(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	srv.appendMessage(t, "INBOX", rawMessage("Lunch", "friend@example.com", "the invoice is only in the body"))

	res, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Query: "invoice"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := subjectsOf(res.Messages); strings.Join(got, "|") != "Lunch" || res.HeadersOnly {
		t.Fatalf("search = %v, headers only %v; want the server's body hit", got, res.HeadersOnly)
	}
}

// NetEase 163 answers every keyword SEARCH with nothing; other servers refuse some with NO. The query is then matched
// against subjects, senders and recipients: every word, any case, newest first; bodies are not looked at.
func TestListMessagesSearchServerCannotSearch(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		srv := newTestIMAP(t)
		if refuse {
			srv.refuseSearch.Store(true)
		} else {
			srv.blindSearch.Store(true)
		}
		s := newServiceWithAccount(t, srv)
		ctx := context.Background()

		srv.appendMessage(t, "INBOX", rawMessage("Invoice for March", "Billing <billing@example.com>", "Please pay"))
		srv.appendMessage(t, "INBOX", rawMessage(mime.BEncoding.Encode("UTF-8", "新设备登录提醒"), "网易邮箱 <safe@example.com>", "账号安全"))
		srv.appendMessage(t, "INBOX", rawMessage("Lunch", "friend@example.com", "the invoice is only in the body"))

		for _, c := range []struct{ query, want string }{
			{"invoice", "Invoice for March"},                      // any case; the body-only mention is not found
			{"登录", "新设备登录提醒"},                                     // an encoded Chinese subject
			{"网易", "新设备登录提醒"},                                     // the sender's name
			{"FRIEND@example", "Lunch"},                           // the sender's address
			{"march  billing", "Invoice for March"},               // every word, in different headers
			{"march lunch", ""},                                   // the words are in different messages
			{"me@example.com", "Lunch|新设备登录提醒|Invoice for March"}, // the recipient
		} {
			res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: c.query})
			if err != nil {
				t.Fatalf("refuse=%v %q: %v", refuse, c.query, err)
			}
			if got := strings.Join(subjectsOf(res.Messages), "|"); got != c.want || !res.HeadersOnly || res.HasMore {
				t.Errorf("refuse=%v %q = %q (headers only %v, has more %v), want %q from the headers, complete",
					refuse, c.query, got, res.HeadersOnly, res.HasMore, c.want)
			}
		}
	}
}

// A page's time counts from the start of its IMAP work, the server's own (empty) search included: when that search
// takes all of it, the page still looks at one batch and says where to carry on, rather than running past the
// operation's timeout the way it did on NetEase 163, which is slow to search and to fetch old mail.
func TestListMessagesSearchSlowServer(t *testing.T) {
	srv := newTestIMAP(t)
	srv.blindSearch.Store(true)
	srv.searchDelay.Store(int64(50 * time.Millisecond))
	s := newServiceWithAccount(t, srv)
	s.scan = localScan{batch: 2, budget: 100, time: 30 * time.Millisecond}
	var uids []imap.UID
	for i := 1; i <= 6; i++ {
		uids = append(uids, srv.appendMessage(t, "INBOX", rawMessage(fmt.Sprintf("Note %d", i), "a@example.com", "x")))
	}

	res, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Query: "note", Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := strings.Join(subjectsOf(res.Messages), "|"); got != "Note 6|Note 5" || !res.HasMore || res.NextBeforeUID != uint32(uids[4]) {
		t.Fatalf("page = %q (has more %v, next %d), want one batch, Note 6|Note 5, then below %d", got, res.HasMore, res.NextBeforeUID, uids[4])
	}
}

// Connecting counts too: a cold login that takes the page's whole time still leaves it one batch and a place to carry
// on, rather than a full scan started late.
func TestListMessagesSearchSlowConnect(t *testing.T) {
	srv := newTestIMAP(t)
	srv.blindSearch.Store(true)
	s := newServiceWithAccount(t, srv)
	s.scan = localScan{batch: 2, budget: 100, time: 30 * time.Millisecond}
	s.imapDial = func(*MailAccount) (*imapclient.Client, error) {
		time.Sleep(60 * time.Millisecond)
		c, err := imapclient.DialInsecure(fmt.Sprintf("%s:%d", srv.host, srv.port), nil)
		if err != nil {
			return nil, err
		}
		if err := c.Login("me@example.com", "secret").Wait(); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
	var uids []imap.UID
	for i := 1; i <= 6; i++ {
		uids = append(uids, srv.appendMessage(t, "INBOX", rawMessage(fmt.Sprintf("Note %d", i), "a@example.com", "x")))
	}

	res, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Query: "note", Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := strings.Join(subjectsOf(res.Messages), "|"); got != "Note 6|Note 5" || !res.HasMore || res.NextBeforeUID != uint32(uids[4]) {
		t.Fatalf("page = %q (has more %v, next %d), want one batch, Note 6|Note 5, then below %d", got, res.HasMore, res.NextBeforeUID, uids[4])
	}
}

// A page fetches its summaries a batch at a time, newest first; once the operation is short of time it ends early
// and says where to carry on, and paging still returns every message once. (NetEase 163 can take tens of seconds
// for a page of old mail.)
func TestListMessagesPageFetchStopsInTime(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	s.fetch = pageFetch{batch: 2, reserve: networkTimeout} // short of time from the start: one batch per page
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		srv.appendMessage(t, "INBOX", rawMessage(fmt.Sprintf("Msg %d", i), "a@example.com", "x"))
	}

	for _, query := range []string{"", "msg"} {
		var seen []string
		before := uint32(0)
		for range 10 {
			res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: query, Limit: 5, BeforeUID: before})
			if err != nil {
				t.Fatalf("query %q below %d: %v", query, before, err)
			}
			if len(res.Messages) > 2 {
				t.Fatalf("query %q below %d: %d summaries, want one batch of 2 at most", query, before, len(res.Messages))
			}
			seen = append(seen, subjectsOf(res.Messages)...)
			if !res.HasMore {
				break
			}
			if res.NextBeforeUID == 0 || before != 0 && res.NextBeforeUID >= before {
				t.Fatalf("query %q below %d: next before_uid %d does not move back", query, before, res.NextBeforeUID)
			}
			before = res.NextBeforeUID
		}
		if got := strings.Join(seen, ","); got != "Msg 5,Msg 4,Msg 3,Msg 2,Msg 1" {
			t.Fatalf("query %q: pages saw %q, want every message once, newest first", query, got)
		}
	}
}

// Matching locally pages like a server search, and a page that has looked at as many messages as it may says where
// to carry on, even when it found nothing.
func TestListMessagesSearchLocalPaging(t *testing.T) {
	srv := newTestIMAP(t)
	srv.blindSearch.Store(true)
	s := newServiceWithAccount(t, srv)
	s.scan = localScan{batch: 2, budget: 4, time: time.Minute}
	ctx := context.Background()

	// UIDs 1–12, oldest first: reports at 1, 2, 5 and 12; nothing between 6 and 11.
	var uids []imap.UID
	for i := 1; i <= 12; i++ {
		subject := fmt.Sprintf("Note %d", i)
		if i == 1 || i == 2 || i == 5 || i == 12 {
			subject = fmt.Sprintf("Report %d", i)
		}
		uids = append(uids, srv.appendMessage(t, "INBOX", rawMessage(subject, "a@example.com", "x")))
	}

	var pages []string
	before := uint32(0)
	for range 12 {
		res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "report", Limit: 1, BeforeUID: before})
		if err != nil {
			t.Fatalf("page below %d: %v", before, err)
		}
		pages = append(pages, strings.Join(subjectsOf(res.Messages), "|"))
		if !res.HasMore {
			break
		}
		if res.NextBeforeUID == 0 || before != 0 && res.NextBeforeUID >= before {
			t.Fatalf("page below %d: next before_uid %d does not move back", before, res.NextBeforeUID)
		}
		before = res.NextBeforeUID
	}
	// 12 and 11–9 fill the first page's budget of four; 8–5 the second; 4–1 then has two reports for a page of one.
	want := []string{"Report 12", "Report 5", "Report 2", "Report 1"}
	if strings.Join(pages, ",") != strings.Join(want, ",") {
		t.Fatalf("pages = %q, want %q", pages, want)
	}

	// A page whose time is up before it starts still looks at one batch and moves on: no page repeats or skips.
	s.scan = localScan{batch: 3, budget: 100, time: time.Nanosecond}
	var seen []string
	before = 0
	for range 12 {
		res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "a@example.com", Limit: 50, BeforeUID: before})
		if err != nil {
			t.Fatalf("timed-out page below %d: %v", before, err)
		}
		if len(res.Messages) > 3 {
			t.Fatalf("timed-out page below %d returned %d messages, want one batch of 3 at most", before, len(res.Messages))
		}
		seen = append(seen, subjectsOf(res.Messages)...)
		if !res.HasMore {
			break
		}
		if res.NextBeforeUID == 0 || before != 0 && res.NextBeforeUID >= before {
			t.Fatalf("timed-out page below %d: next before_uid %d does not move back", before, res.NextBeforeUID)
		}
		before = res.NextBeforeUID
	}
	if len(seen) != 12 || seen[0] != "Report 12" || seen[11] != "Report 1" {
		t.Fatalf("timed-out pages saw %q, want all 12 newest first", seen)
	}
	s.scan = localScan{batch: 2, budget: 4, time: time.Minute}

	// Nothing among the newest: the page is empty but says where the older messages start.
	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "note 6", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Messages) != 0 || !res.HasMore || res.NextBeforeUID != uint32(uids[8]) {
		t.Fatalf("empty first page = %+v, want has_more and next before_uid %d", res, uids[8])
	}
}

func TestMailboxes(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	srv.appendMessage(t, "INBOX", rawMessage("Unread", "a@example.com", "hi"))
	srv.appendMessage(t, "INBOX", rawMessage("Read", "b@example.com", "hi"))

	boxes, err := s.Mailboxes(ctx, "acc_test")
	if err != nil {
		t.Fatalf("Mailboxes: %v", err)
	}
	byName := map[string]Mailbox{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	inbox, ok := byName["INBOX"]
	if !ok {
		t.Fatalf("INBOX missing from %v", boxes)
	}
	if inbox.Role != RoleInbox {
		t.Errorf("INBOX role = %q", inbox.Role)
	}
	if inbox.Total != 2 || inbox.Unseen != 2 {
		t.Errorf("INBOX counters = %d total / %d unseen, want 2/2", inbox.Total, inbox.Unseen)
	}
	// Special-use is not set by the in-memory server, so the name fallback
	// applies.
	if got := byName["Sent"].Role; got != RoleSent {
		t.Errorf("Sent role = %q", got)
	}
	if got := byName["Junk"].Role; got != RoleJunk {
		t.Errorf("Junk role = %q", got)
	}
}

// QQ Mail's IMAP server advertises none of LIST-EXTENDED, LIST-STATUS and
// SPECIAL-USE and never answers a LIST with RETURN options, so every mailbox
// listing used to hit the timeout.
func TestMailboxesWithoutListExtended(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.ignoreListReturn(t)
	s := newServiceWithAccount(t, srv)
	srv.appendMessage(t, "INBOX", rawMessage("Unread", "a@example.com", "hi"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	boxes, err := s.Mailboxes(ctx, "acc_test")
	if err != nil {
		t.Fatalf("Mailboxes: %v", err)
	}
	byName := map[string]Mailbox{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	if inbox := byName["INBOX"]; inbox.Total != 1 || inbox.Unseen != 1 {
		t.Errorf("INBOX counters = %d total / %d unseen, want 1/1 (from STATUS)", inbox.Total, inbox.Unseen)
	}
	if got := byName["Sent"].Role; got != RoleSent {
		t.Errorf("Sent role = %q", got)
	}
}

func TestListOptions(t *testing.T) {
	status := &imap.StatusOptions{NumMessages: true, NumUnseen: true}
	caps := func(cs ...imap.Cap) imap.CapSet {
		set := imap.CapSet{imap.CapIMAP4rev1: {}}
		for _, c := range cs {
			set[c] = struct{}{}
		}
		return set
	}
	for _, tc := range []struct {
		name       string
		caps       imap.CapSet
		specialUse bool
		withStatus bool
	}{
		{"plain IMAP4rev1 (QQ Mail)", caps(), false, false},
		{"SPECIAL-USE without LIST-EXTENDED", caps(imap.CapSpecialUse), false, false},
		{"LIST-EXTENDED only", caps(imap.CapListExtended), false, false},
		{"LIST-EXTENDED, LIST-STATUS, SPECIAL-USE (Gmail)", caps(imap.CapListExtended, imap.CapListStatus, imap.CapSpecialUse), true, true},
		{"IMAP4rev2", caps(imap.CapIMAP4rev2), true, true},
	} {
		got := listOptions(tc.caps, status)
		if got.ReturnSpecialUse != tc.specialUse || (got.ReturnStatus != nil) != tc.withStatus {
			t.Errorf("%s: ReturnSpecialUse=%v ReturnStatus=%v, want %v %v",
				tc.name, got.ReturnSpecialUse, got.ReturnStatus != nil, tc.specialUse, tc.withStatus)
		}
		if got := listOptions(tc.caps, nil); got.ReturnStatus != nil {
			t.Errorf("%s: STATUS asked for without status options", tc.name)
		}
	}
}

// The copy into Sent after sending looks the mailbox up the same way; on such a
// server every send waited for the timeout and the copy was lost.
func TestSentMailboxWithoutListExtended(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.ignoreListReturn(t)
	s := newServiceWithAccount(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sent, err := s.sentMailbox(ctx, srv.testAccount())
	if err != nil {
		t.Fatalf("sentMailbox: %v", err)
	}
	if sent != "Sent" {
		t.Errorf("sentMailbox = %q, want Sent", sent)
	}
}

func TestGetMessage(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	raw := "From: =?utf-8?q?=E6=9D=8E=E5=9B=9B?= <li@example.com>\r\n" +
		"To: me@example.com, Second <two@example.com>\r\n" +
		"Cc: cc@example.com\r\n" +
		"Subject: =?utf-8?q?=E4=B8=AD=E6=96=87=E4=B8=BB=E9=A2=98?=\r\n" +
		"Date: Tue, 03 Jan 2006 10:00:00 +0800\r\n" +
		"Message-Id: <abc@example.com>\r\n" +
		"In-Reply-To: <parent@example.com>\r\n" +
		"References: <root@example.com> <parent@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: multipart/alternative; boundary=\"ALT\"\r\n" +
		"\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>html <b>body</b></p>\r\n" +
		"--ALT--\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/pdf; name=\"report.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQK\r\n" +
		"--BOUND--\r\n"

	uid := srv.appendMessage(t, "INBOX", raw)

	msg, err := s.GetMessage(ctx, "acc_test", uint32(uid), "INBOX")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Subject != "中文主题" {
		t.Errorf("subject = %q", msg.Subject)
	}
	if msg.From.Name != "李四" || msg.From.Email != "li@example.com" {
		t.Errorf("from = %+v", msg.From)
	}
	if len(msg.To) != 2 || msg.To[1].Email != "two@example.com" || msg.To[1].Name != "Second" {
		t.Errorf("to = %+v", msg.To)
	}
	if len(msg.Cc) != 1 || msg.Cc[0].Email != "cc@example.com" {
		t.Errorf("cc = %+v", msg.Cc)
	}
	if msg.Text != "plain body" {
		t.Errorf("text = %q", msg.Text)
	}
	if !strings.Contains(msg.HTML, "<b>body</b>") {
		t.Errorf("html = %q", msg.HTML)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "report.pdf" {
		t.Fatalf("attachments = %+v", msg.Attachments)
	}
	if msg.Attachments[0].Mime != "application/pdf" {
		t.Errorf("attachment mime = %q", msg.Attachments[0].Mime)
	}
	if msg.Attachments[0].Size != 9 { // base64 "JVBERi0xLjQK" decodes to "%PDF-1.4\n"
		t.Errorf("attachment size = %d, want 9", msg.Attachments[0].Size)
	}
	if !msg.HasAttach {
		t.Error("has_attachments = false")
	}
	if msg.InReplyTo != "parent@example.com" {
		t.Errorf("in_reply_to = %q", msg.InReplyTo)
	}
	if len(msg.References) != 2 || msg.References[0] != "root@example.com" {
		t.Errorf("references = %v", msg.References)
	}
	if msg.Seen {
		t.Error("fetched message should not be marked seen")
	}

	// Reading must not set \Seen on the server (BODY.PEEK).
	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Seen {
		t.Fatalf("message was marked seen by GetMessage: %+v", res.Messages)
	}
}

func TestSetFlags(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	uid := srv.appendMessage(t, "INBOX", rawMessage("Flags", "a@example.com", "hi"))

	yes, no := true, false
	if err := s.SetFlags(ctx, "acc_test", uint32(uid), SetFlagsRequest{Seen: &yes, Flagged: &yes}); err != nil {
		t.Fatalf("SetFlags seen+flagged: %v", err)
	}
	msg, err := s.GetMessage(ctx, "acc_test", uint32(uid), "INBOX")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !msg.Seen || !msg.Flagged {
		t.Fatalf("flags not set: seen=%v flagged=%v", msg.Seen, msg.Flagged)
	}

	if err := s.SetFlags(ctx, "acc_test", uint32(uid), SetFlagsRequest{Flagged: &no}); err != nil {
		t.Fatalf("SetFlags unflag: %v", err)
	}
	msg, err = s.GetMessage(ctx, "acc_test", uint32(uid), "INBOX")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Flagged {
		t.Fatal("flag was not cleared")
	}
	if !msg.Seen {
		t.Fatal("seen flag was cleared by an unrelated update")
	}
}

func TestListMessagesAllAccounts(t *testing.T) {
	srvA := newTestIMAP(t)
	srvB := newTestIMAP(t)
	dir := t.TempDir()
	s, err := New(dir, newMemRecords())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.store.save([]*MailAccount{srvA.testAccount()}); err != nil {
		t.Fatal(err)
	}
	accounts, _ := s.store.load()
	for _, a := range accounts {
		s.accts[a.ID] = a
	}
	b := srvB.testAccount()
	b.ID = "acc_b"
	b.Email = "second@example.com"
	b.Username = "me@example.com"
	s.accts[b.ID] = b

	srvA.appendMessage(t, "INBOX", rawMessage("From A", "a@example.com", "a"))
	srvB.appendMessage(t, "INBOX", rawMessage("From B", "b@example.com", "b"))

	res, err := s.ListMessages(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("merged list: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("merged list = %v", subjectsOf(res.Messages))
	}
	ids := map[string]bool{}
	for _, m := range res.Messages {
		ids[m.AccountID] = true
	}
	if !ids["acc_test"] || !ids["acc_b"] {
		t.Fatalf("merged accounts = %v", ids)
	}
	// Newest first.
	for i := 1; i < len(res.Messages); i++ {
		if res.Messages[i-1].Date < res.Messages[i].Date {
			t.Fatalf("merged list is not sorted by date desc: %+v", res.Messages)
		}
	}
}

func TestConnReuseAndIdleExpiry(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("first list: %v", err)
	}
	s.mu.Lock()
	first := s.conns["acc_test"]
	s.mu.Unlock()
	if first == nil {
		t.Fatal("no cached connection after a list")
	}

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("second list: %v", err)
	}
	s.mu.Lock()
	second := s.conns["acc_test"]
	s.mu.Unlock()
	if second != first {
		t.Fatal("connection was not reused")
	}

	// Age the connection past the idle timeout and run one sweep: it must be
	// closed and removed.
	s.mu.Lock()
	first.lastUsed = time.Now().Add(-idleTimeout - time.Minute)
	s.mu.Unlock()
	s.sweepOnce()
	s.mu.Lock()
	_, still := s.conns["acc_test"]
	s.mu.Unlock()
	if still {
		t.Fatal("idle connection was not swept")
	}

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("list after sweep: %v", err)
	}
}

func TestReconnectAfterServerRestart(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("first list: %v", err)
	}
	// Kill the cached connection underneath the service, simulating a network
	// drop or a server-side timeout.
	s.mu.Lock()
	c := s.conns["acc_test"]
	s.mu.Unlock()
	if c != nil {
		c.c.Close()
	}
	time.Sleep(50 * time.Millisecond)

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("list after close: %v", err)
	}
}

// A caller waiting for an account's turn waits with its ctx: once the ctx ends it leaves the queue with ctx.Err(),
// does not run, and does not touch the cached connection, which the next live caller reuses. Before (#2344) it waited
// for its turn regardless of the ctx, and on its turn dropped the connection on the way out.
func TestWaitForAccountTurnEndsWithTheContext(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()
	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("first list: %v", err)
	}
	s.mu.Lock()
	a, before := s.accts["acc_test"], s.conns["acc_test"]
	s.mu.Unlock()
	if before == nil {
		t.Fatal("no cached connection after a list")
	}

	// One caller holds the account's turn for a while.
	held, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- s.withConn(ctx, a, "test", func(*imapclient.Client) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	time.AfterFunc(1500*time.Millisecond, func() { close(release) })

	// A second caller with a short ctx gives up on time: it does not run and leaves the connection alone.
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	var ran atomic.Bool
	start := time.Now()
	err := s.withConn(short, a, "test", func(*imapclient.Client) error { ran.Store(true); return nil })
	waited := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller: %v, want context.DeadlineExceeded", err)
	}
	if waited > time.Second {
		t.Fatalf("the caller waited %v for its turn, want about its own 100ms", waited.Round(time.Millisecond))
	}
	if ran.Load() {
		t.Fatal("the caller that gave up ran")
	}
	s.mu.Lock()
	after := s.conns["acc_test"]
	s.mu.Unlock()
	if after != before {
		t.Fatal("the caller that gave up touched the cached connection")
	}

	// The holder finishes; the next live caller gets its turn on the same connection.
	if err := <-holder; err != nil {
		t.Fatalf("holder: %v", err)
	}
	if err := s.withConn(ctx, a, "test", func(*imapclient.Client) error { return nil }); err != nil {
		t.Fatalf("after the holder: %v", err)
	}
	s.mu.Lock()
	after = s.conns["acc_test"]
	s.mu.Unlock()
	if after != before {
		t.Fatal("the connection was not reused after the wait")
	}
}

func subjectsOf(msgs []MailSummary) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// The common mixed(alternative(plain, html), attachment) shape: the preview must come from the nested text/plain part.
func TestTextFromRawNestedMultipart(t *testing.T) {
	raw := []byte("MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: multipart/alternative; boundary=\"ALT\"\r\n" +
		"\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>html</p>\r\n" +
		"--ALT--\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/pdf\r\n" +
		"\r\n" +
		"JVBERi0=\r\n" +
		"--BOUND--\r\n")
	if got := textFromRaw(raw); got != "plain body" {
		t.Fatalf("textFromRaw = %q, want %q", got, "plain body")
	}
	// An empty first text part falls through to a later one.
	emptyFirst := []byte("MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nlater\r\n" +
		"--B--\r\n")
	if got := textFromRaw(emptyFirst); got != "later" {
		t.Fatalf("empty first part: %q, want %q", got, "later")
	}
	// Some senders put html before plain in alternative; the walker takes the first text/* part in order.
	htmlFirst := []byte("MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"A\"\r\n\r\n" +
		"--A\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html first</p>\r\n" +
		"--A\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain second\r\n" +
		"--A--\r\n")
	if got := strings.TrimSpace(textFromRaw(htmlFirst)); got != "html first" {
		t.Fatalf("html-first alternative: %q, want %q", got, "html first")
	}
}
