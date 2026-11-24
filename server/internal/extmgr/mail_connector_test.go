package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/mailwire"
)

// pagedMail is one account's inbox, paged by before_uid like the IMAP listing: newest first, NextBeforeUID the
// lowest UID of the page whenever there is more.
type pagedMail struct {
	inboxOnlyMail
	validity uint32
	msgs     []mailwire.MailSummary // ascending UID
	failOld  bool                   // pages below the newest fail, as a slow server's would time out
}

var mailTestBase = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

func (m *pagedMail) add(from, to uint32) {
	for uid := from; uid <= to; uid++ {
		m.msgs = append(m.msgs, mailwire.MailSummary{AccountID: "acc", Mailbox: "INBOX", UID: uid, Subject: fmt.Sprint("mail ", uid),
			From: mailwire.Address{Email: "sender@example.com"}, Date: mailTestBase.Add(time.Duration(uid) * time.Minute).Format(time.RFC3339),
			Snippet: "snippet"})
	}
}

func (m *pagedMail) ListAccounts() ([]mailwire.MailAccount, error) {
	return []mailwire.MailAccount{{ID: "acc", Email: "me@example.com"}}, nil
}

func (m *pagedMail) ListMessages(_ context.Context, opts mailwire.ListOptions) (*mailwire.ListResult, error) {
	if m.failOld && opts.BeforeUID != 0 {
		return nil, errors.New("i/o timeout")
	}
	var below []mailwire.MailSummary
	for _, s := range m.msgs {
		if opts.BeforeUID == 0 || s.UID < opts.BeforeUID {
			below = append(below, s)
		}
	}
	res := &mailwire.ListResult{UIDValidity: m.validity}
	if len(below) > opts.Limit {
		below = below[len(below)-opts.Limit:]
		res.HasMore, res.NextBeforeUID = true, below[0].UID
	}
	for i := len(below) - 1; i >= 0; i-- {
		res.Messages = append(res.Messages, below[i])
	}
	return res, nil
}

func (m *pagedMail) GetMessage(context.Context, string, uint32, string) (*mailwire.MailMessage, error) {
	return nil, errors.New("not in this test")
}

// archive runs Changes as the knowledge base does (again while more), at most calls times, and returns the UIDs
// archived in order and the cursor.
func archiveMail(t *testing.T, m *pagedMail, cursor string, calls int) ([]uint32, string, bool) {
	t.Helper()
	c := mailConnector{h: &Manager{mail: m}}
	var uids []uint32
	more := true
	for i := 0; i < calls && more; i++ {
		batches, next, again, err := c.Changes(context.Background(), cursor, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range batches {
			id := b.Messages[0].ID
			if i := strings.LastIndexByte(id, ':'); i >= 0 {
				id = id[i+1:]
			}
			uid, err := strconv.ParseUint(id, 10, 32)
			if err != nil {
				t.Fatalf("invalid mail message ID %q: %v", b.Messages[0].ID, err)
			}
			uids = append(uids, uint32(uid))
		}
		cursor, more = next, again
	}
	return uids, cursor, more
}

func uidRange(from, to uint32) []uint32 {
	var out []uint32
	for uid := from; uid <= to; uid++ {
		out = append(out, uid)
	}
	return out
}

func sortedCopy(uids []uint32) []uint32 {
	out := slices.Clone(uids)
	slices.Sort(out)
	return out
}

func cursorOf(t *testing.T, raw string) map[string]uint32 {
	t.Helper()
	var c map[string]uint32
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("cursor %q: %v", raw, err)
	}
	return c
}

// More than a page since the last run: everything between the cursor and the newest page is archived too (it was
// skipped for good when the cursor jumped to the page's top), in one call, oldest first.
func TestMailArchiveFillsGapAboveCursor(t *testing.T) {
	m := &pagedMail{validity: 1}
	m.add(1, 220)
	uids, cursor, more := archiveMail(t, m, `{"acc":100,"acc#v":1}`, 1)
	if !slices.Equal(uids, uidRange(101, 220)) || more {
		t.Fatalf("archived %d mails (more %v): %v", len(uids), more, uids)
	}
	if c := cursorOf(t, cursor); c["acc"] != 220 || c["acc#v"] != 1 || c["acc#h"] != 0 || c["acc#f"] != 0 {
		t.Fatalf("cursor = %s", cursor)
	}
}

// A gap larger than one call's pages goes on with more; mail that comes meanwhile waits for the gap and is archived
// after it, each mail once.
func TestMailArchiveLargeGapAcrossCalls(t *testing.T) {
	m := &pagedMail{validity: 1}
	m.add(1, 500)
	uids, cursor, more := archiveMail(t, m, `{"acc":100,"acc#v":1}`, 1)
	if len(uids) != mailArchivePages*mailArchivePage || !more {
		t.Fatalf("first call archived %d (more %v)", len(uids), more)
	}
	if c := cursorOf(t, cursor); c["acc"] != 500 || c["acc#f"] != 100 || c["acc#h"] == 0 {
		t.Fatalf("cursor after the first call = %s", cursor)
	}
	m.add(501, 550) // more than a page above the cursor while the gap is open
	rest, cursor, more := archiveMail(t, m, cursor, 10)
	all := append(uids, rest...)
	if !slices.Equal(sortedCopy(all), uidRange(101, 550)) || more {
		t.Fatalf("archived %d mails, more %v", len(all), more)
	}
	if c := cursorOf(t, cursor); c["acc"] != 550 || c["acc#h"] != 0 {
		t.Fatalf("final cursor = %s", cursor)
	}
}

// The server numbered the mailbox anew (UIDVALIDITY changed): its UIDs are below the cursor, yet it is new mail;
// only mail newer than the newest archived one goes in.
func TestMailArchiveRestartsOnUIDValidityChange(t *testing.T) {
	m := &pagedMail{validity: 2}
	m.add(1, 15)
	newest := mailTestBase.Add(5 * time.Minute).Unix() // mails 1–5 were archived under the old numbering
	uids, cursor, _ := archiveMail(t, m, fmt.Sprintf(`{"acc":500,"acc#v":1,"acc#t":%d}`, newest), 1)
	if !slices.Equal(uids, uidRange(6, 15)) {
		t.Fatalf("archived %v", uids)
	}
	c := cursorOf(t, cursor)
	if c["acc"] != 15 || c["acc#v"] != 2 || int64(c["acc#t"]) != mailTestBase.Add(15*time.Minute).Unix() {
		t.Fatalf("cursor = %s", cursor)
	}
	if again, _, _ := archiveMail(t, m, cursor, 1); len(again) != 0 {
		t.Fatalf("archived again: %v", again)
	}
}

// A cursor from before UIDVALIDITY was kept ({"<account>": uid}) goes on where it was and learns the validity.
func TestMailArchiveLegacyCursor(t *testing.T) {
	m := &pagedMail{validity: 7}
	m.add(1, 8)
	uids, cursor, more := archiveMail(t, m, `{"acc":5}`, 1)
	if !slices.Equal(uids, uidRange(6, 8)) || more {
		t.Fatalf("archived %v (more %v)", uids, more)
	}
	if c := cursorOf(t, cursor); c["acc"] != 8 || c["acc#v"] != 7 {
		t.Fatalf("cursor = %s", cursor)
	}
}

// The first run reads only the newest page, as before: no gap below it.
func TestMailArchiveFirstRunNewestPage(t *testing.T) {
	m := &pagedMail{validity: 1}
	m.add(1, 100)
	uids, cursor, more := archiveMail(t, m, "", 3)
	if !slices.Equal(uids, uidRange(61, 100)) || more {
		t.Fatalf("archived %v (more %v)", uids, more)
	}
	if c := cursorOf(t, cursor); c["acc"] != 100 || c["acc#h"] != 0 {
		t.Fatalf("cursor = %s", cursor)
	}
}

// A page of the hole that cannot be read keeps the hole for the next run (more), and nothing is archived twice.
func TestMailArchiveHolePageFails(t *testing.T) {
	m := &pagedMail{validity: 1, failOld: true}
	m.add(1, 220)
	uids, cursor, more := archiveMail(t, m, `{"acc":100,"acc#v":1}`, 1)
	if !slices.Equal(uids, uidRange(181, 220)) || !more {
		t.Fatalf("archived %v (more %v)", uids, more)
	}
	if c := cursorOf(t, cursor); c["acc"] != 220 || c["acc#f"] != 100 || c["acc#h"] != 181 {
		t.Fatalf("cursor = %s", cursor)
	}
	m.failOld = false
	rest, cursor, more := archiveMail(t, m, cursor, 3)
	if !slices.Equal(rest, uidRange(101, 180)) || more {
		t.Fatalf("then archived %v (more %v)", rest, more)
	}
	if c := cursorOf(t, cursor); c["acc"] != 220 || c["acc#h"] != 0 || c["acc#f"] != 0 {
		t.Fatalf("final cursor = %s", cursor)
	}
}

// An inbox that cannot be read at all leaves the cursor as it was.
func TestMailArchiveUnreadableInbox(t *testing.T) {
	m := &pagedMail{validity: 1}
	m.add(1, 10)
	c := mailConnector{h: &Manager{mail: failingMail{m}}}
	batches, next, more, err := c.Changes(context.Background(), `{"acc":5,"acc#v":1}`, 20)
	if err != nil || len(batches) != 0 || more || next != `{"acc":5,"acc#v":1}` {
		t.Fatalf("batches %d, cursor %s, more %v, err %v", len(batches), next, more, err)
	}
}

type failingMail struct{ *pagedMail }

func (failingMail) ListMessages(context.Context, mailwire.ListOptions) (*mailwire.ListResult, error) {
	return nil, errors.New("login failed")
}
