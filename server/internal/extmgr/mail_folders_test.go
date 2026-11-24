package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/mailwire"
)

type inboxOnlyMail struct{ Mail }

func (inboxOnlyMail) IntakeMailboxes(context.Context, string) ([]string, error) {
	return []string{"INBOX"}, nil
}

type folderMail struct {
	*pagedMail
	boxes   map[string]*pagedMail
	listErr error
}

func (m *folderMail) IntakeMailboxes(context.Context, string) ([]string, error) {
	return []string{"INBOX", "GitHub"}, m.listErr
}

func (m *folderMail) ListMessages(ctx context.Context, opts mailwire.ListOptions) (*mailwire.ListResult, error) {
	if opts.Mailbox == "" {
		opts.Mailbox = "INBOX"
	}
	page := m.boxes[opts.Mailbox]
	if page == nil {
		return nil, fmt.Errorf("unknown folder %q", opts.Mailbox)
	}
	return page.ListMessages(ctx, opts)
}

func (m *folderMail) GetMessage(_ context.Context, account string, uid uint32, box string) (*mailwire.MailMessage, error) {
	return &mailwire.MailMessage{Text: fmt.Sprintf("%s/%s/%d", account, box, uid)}, nil
}

// A rule puts the second message straight into GitHub. Its UID and sender happen to match the INBOX mail.
func TestInboxAndArchiveReadMailFolders(t *testing.T) {
	inbox, github := &pagedMail{validity: 3}, &pagedMail{validity: 8}
	inbox.add(1, 1)
	github.add(1, 1)
	github.msgs[0].Mailbox = "GitHub"
	m := &folderMail{pagedMail: inbox, boxes: map[string]*pagedMail{"INBOX": inbox, "GitHub": github}}
	ctx := t.Context()
	h := newTestManager(t)
	h.mail = m
	page, err := h.readMailAccount(ctx, mailwire.MailAccount{ID: "acc", Email: "me@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var old struct{ Messages []mailwire.MailSummary }
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if len(old.Messages) != 1 || old.Messages[0].Mailbox != "INBOX" || page.UIDValidity != 3 || len(page.Folders) != 1 ||
		page.Folders[0].Name != "GitHub" || page.Folders[0].UIDValidity != 8 || len(page.Folders[0].Messages) != 1 ||
		page.Folders[0].Messages[0].Mailbox != "GitHub" {
		t.Errorf("incorrect original and additional pages: %s", raw)
	}
	c := mailConnector{h: h}
	batches, cursor, more, err := c.Changes(ctx, "", 20)
	if err != nil || more || len(batches) != 2 {
		t.Fatalf("archive got %d messages, more %v, error %v", len(batches), more, err)
	}
	if batches[0].Chat.ID != batches[1].Chat.ID || batches[0].Messages[0].ID != "1" || batches[1].Messages[0].ID == "1" {
		t.Fatalf("the same UID in two folders must give distinct receipts in the correspondent's chat: %v", batches)
	}
	rerun, _, more, err := c.Changes(ctx, cursor, 20)
	if err != nil || more || len(rerun) != 0 {
		t.Errorf("replayed %d messages, more %v, error %v", len(rerun), more, err)
	}
	legacy, _, _, err := c.Changes(ctx, `{"acc":1,"acc#v":3}`, 20)
	if err != nil || len(legacy) != 1 || legacy[0].Messages[0].ID != batches[1].Messages[0].ID {
		t.Errorf("legacy cursor: got %v, %v", legacy, err)
	}
}

func TestMailFolderArchiveRecovery(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	inbox, github := &pagedMail{validity: 3}, &pagedMail{validity: 8}
	inbox.msgs = []mailwire.MailSummary{receiptMail("acc", 1, now, "inbox")}
	github.msgs = []mailwire.MailSummary{receiptMail("acc", 1, now, "first build")}
	m := &folderMail{pagedMail: inbox, boxes: map[string]*pagedMail{"INBOX": inbox, "GitHub": github}}
	c := mailConnector{h: &Manager{mail: m}}
	e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
	first, cursor := receiptChanges(t, c, "", 2)
	retry, _ := receiptChanges(t, c, "", 2)
	for i, box := range []string{"INBOX", "GitHub"} {
		if !strings.Contains(first[i].Messages[0].Text, "acc/"+box+"/1") {
			t.Fatalf("body read from wrong folder: %v", first[i])
		}
		ingestMail(t, e, first[i], 1)
		ingestMail(t, e, retry[i], 0)
	}

	// A temporarily unreadable INBOX does not prevent the rule folder from advancing.
	delete(m.boxes, "INBOX")
	github.validity = 9
	github.msgs = []mailwire.MailSummary{receiptMail("acc", 1, now.Add(time.Minute), "next build")}
	gen, next := receiptChanges(t, c, cursor, 1)
	if gen[0].Messages[0].ID == first[1].Messages[0].ID {
		t.Fatal("renumbered folder reused a receipt")
	}
	ingestMail(t, e, gen[0], 1)
	inbox.msgs = append(inbox.msgs, receiptMail("acc", 2, now.Add(time.Minute), "inbox next"))
	m.boxes["INBOX"] = inbox
	recovered, next := receiptChanges(t, c, next, 1)
	if recovered[0].Messages[0].ID != "2" {
		t.Fatalf("recovered INBOX got %q", recovered[0].Messages[0].ID)
	}
	ingestMail(t, e, recovered[0], 1)
	receiptChanges(t, c, next, 0)
}

func TestMailFolderPartialReads(t *testing.T) {
	inbox, github := &pagedMail{validity: 3}, &pagedMail{validity: 8}
	m := &folderMail{pagedMail: inbox, boxes: map[string]*pagedMail{"GitHub": github}}
	h := newTestManager(t)
	h.mail = m
	account := mailwire.MailAccount{ID: "acc"}
	read := func() *assistantwire.InboxMailAccount {
		t.Helper()
		a, err := h.readMailAccount(t.Context(), account)
		if a == nil || err == nil {
			t.Fatalf("expected partial read with its error: %+v, %v", a, err)
		}
		return a
	}
	if a := read(); !a.InboxUnavailable || len(a.Folders) != 1 || a.Folders[0].UIDValidity != 8 {
		t.Fatalf("missing successful folder: %+v", a)
	}
	m.boxes = map[string]*pagedMail{"INBOX": inbox}
	if a := read(); a.InboxUnavailable || a.UIDValidity != 3 || len(a.Folders) != 0 {
		t.Fatalf("missing successful INBOX: %+v", a)
	}
	m.listErr = fmt.Errorf("list unavailable")
	if a := read(); a.InboxUnavailable || a.UIDValidity != 3 {
		t.Fatalf("LIST failure lost INBOX: %+v", a)
	}
}

func TestMailFolderHeldPages(t *testing.T) {
	msgs := func(uids ...uint32) []assistantwire.InboxMailSummary {
		var msgs []assistantwire.InboxMailSummary
		for _, uid := range uids {
			msgs = append(msgs, assistantwire.InboxMailSummary{UID: uid})
		}
		return msgs
	}
	folder := func(name string, validity uint32, uids ...uint32) assistantwire.InboxMailFolder {
		return assistantwire.InboxMailFolder{Name: name, UIDValidity: validity, Messages: msgs(uids...)}
	}
	round := func(a assistantwire.InboxMailAccount) assistantwire.InboxRound {
		return assistantwire.InboxRound{Mail: &assistantwire.InboxMailRead{Accounts: []assistantwire.InboxMailAccount{a}}}
	}
	old := round(assistantwire.InboxMailAccount{ID: "acc", UIDValidity: 1, Messages: msgs(1),
		Folders: []assistantwire.InboxMailFolder{folder("GitHub", 8, 1, 2), folder("Apple", 4, 1), folder("Other", 5, 1)}})
	n := round(assistantwire.InboxMailAccount{ID: "acc", InboxUnavailable: true,
		Folders: []assistantwire.InboxMailFolder{folder("GitHub", 9, 1), folder("Apple", 4, 2)}})
	unknown := round(assistantwire.InboxMailAccount{ID: "acc", InboxUnavailable: true,
		Folders: []assistantwire.InboxMailFolder{folder("GitHub", 0, 3)}})
	got := mergeRounds(mergeRounds(old, unknown), n).Mail.Accounts[0]
	if got.InboxUnavailable || got.UIDValidity != 1 || len(got.Messages) != 1 || got.Messages[0].UID != 1 ||
		len(got.Folders) != 3 || len(got.Folders[0].Messages) != 1 || got.Folders[0].UIDValidity != 9 ||
		len(got.Folders[1].Messages) != 2 || got.Folders[1].Messages[0].UID != 1 || got.Folders[1].Messages[1].UID != 2 ||
		got.Folders[2].Name != "Other" || len(n.Mail.Accounts[0].Folders[1].Messages) != 1 {
		t.Fatalf("held folder pages or original round changed: %+v, %+v", got, n.Mail.Accounts[0])
	}
}
