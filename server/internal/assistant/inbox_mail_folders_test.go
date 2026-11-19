package assistant

import (
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Existing INBOX mail, an empty folder first seen during upgrade, and mail subsequently delivered by a rule.
func TestInboxMailFolderUIDs(t *testing.T) {
	h := newInboxHub(t)
	ctx := t.Context()
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	h.in.mail = &fakeMail{bodies: map[uint32]assistantwire.InboxMailMessage{9000: {Text: "build result"}}}
	h.inbox.mu.Lock()
	h.inbox.mailBaselines = map[string]uint32{"a1": 9000}
	h.inbox.mu.Unlock()
	mailPage := func(box string, uid uint32, seen bool) assistantwire.InboxMailSummary {
		m := seenMail(uid, "CI failed", seen, time.Minute, "me@example.test")
		m.Mailbox = box
		return m
	}
	round := func(inbox, folder []assistantwire.InboxMailSummary) {
		a := assistantwire.InboxMailAccount{ID: "a1", Email: "me@example.test", Messages: inbox,
			Folders: []assistantwire.InboxMailFolder{{Name: "GitHub", UIDValidity: 8, Messages: folder}}}
		h.collectMail(ctx, &assistantwire.InboxMailRead{Accounts: []assistantwire.InboxMailAccount{a}})
	}
	inbox := []assistantwire.InboxMailSummary{mailPage("INBOX", 9000, false)}
	round(inbox, nil)
	legacyID := "mail:a1:" + mailThread("CI failed") + ":9000"
	if h.inbox.items[legacyID] == nil {
		t.Fatal("legacy INBOX item ID changed")
	}
	// A read-on-arrival UID lower than the other mailbox's baseline is still a new message.
	github := []assistantwire.InboxMailSummary{mailPage("GitHub", 50, true)}
	round(inbox, github)
	if len(h.inbox.items) != 2 {
		t.Fatalf("got %d items, want one per folder", len(h.inbox.items))
	}
	for _, item := range h.inbox.items {
		if item.ID != legacyID && (item.Mail.Mailbox != "GitHub" || item.open() || item.DoneBy != "read") {
			t.Fatalf("rule-delivered seen mail: %+v", item)
		}
	}
	// The same number, sender, and subject as the first mail is genuinely new in the second folder.
	github = append(github, mailPage("GitHub", 9000, false))
	round(inbox, github)
	round(inbox, github)
	if len(h.inbox.items) != 3 {
		t.Fatalf("same UID twice or repeat poll: got %d items", len(h.inbox.items))
	}
	for _, item := range h.inbox.items {
		if item.Mail.UIDs[0] == 9000 && !item.open() {
			t.Fatalf("filed the other folder's mail: %+v", item)
		}
	}
}

// Mail still on INBOX's page does not stand in for the same UID and thread in another folder.
// A changed UIDVALIDITY in that folder alone invalidates its old targets, even when the number repeats.
func TestInboxMailFoldersOffPageAndRenumber(t *testing.T) {
	h := newInboxHub(t)
	ctx := t.Context()
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	h.in.mail = &fakeMail{bodies: map[uint32]assistantwire.InboxMailMessage{7: {Text: "body"}}, box: map[uint32]bool{}}
	msg := seenMail(7, "CI failed", false, time.Minute, "me@example.test")
	a := assistantwire.InboxMailAccount{ID: "acc", UIDValidity: 1, Messages: []assistantwire.InboxMailSummary{msg}}
	f := assistantwire.InboxMailFolder{Name: "GitHub", UIDValidity: 8, Messages: []assistantwire.InboxMailSummary{msg}}
	collect := func(folders ...assistantwire.InboxMailFolder) {
		a.Folders = folders
		h.collectMail(ctx, &assistantwire.InboxMailRead{Accounts: []assistantwire.InboxMailAccount{a}})
	}
	collect(f)
	if len(h.in.mail.bodyAsks) != 2 || !slices.Contains(h.in.mail.bodyAsks, assistantwire.InboxMailRef{AccountID: a.ID, Mailbox: f.Name, UID: 7}) {
		t.Fatalf("body targets = %+v", h.in.mail.bodyAsks)
	}
	var old, inbox *InboxItem
	for _, it := range h.inbox.items {
		if it.Mail.Mailbox == f.Name {
			old = it
		} else {
			inbox = it
		}
	}
	if old == nil || inbox == nil {
		t.Fatal("missing mail items")
	}
	old.Tier = TierFYI
	collect() // A failed folder has no page at all.
	if len(h.in.mail.flagAsks) != 0 || old.Read {
		t.Fatal("failed folder was settled")
	}
	f.Messages = nil
	collect(f)
	if len(h.in.mail.flagAsks) != 1 || h.in.mail.flagAsks[0].Mailbox != f.Name || !slices.Equal(h.in.mail.flagAsks[0].UIDs, []uint32{7}) || !old.Read || inbox.Read {
		t.Fatalf("off-page settled the wrong folder: %+v", h.in.mail.flagAsks)
	}
	f.UIDValidity, f.Messages = 9, []assistantwire.InboxMailSummary{msg}
	collect(f)
	collect(f)
	if len(h.inbox.items) != 3 || len(old.Mail.UIDs) != 0 || old.listed().Mail.UIDs == nil || mailTarget(*old) != nil || len(inbox.Mail.UIDs) != 1 || inbox.Read {
		t.Fatalf("renumber disturbed history or another folder: old=%+v inbox=%+v total=%d", old.Mail, inbox.Mail, len(h.inbox.items))
	}
	for _, it := range h.inbox.items {
		if it != old && it != inbox && (!it.open() || it.Read || it.Mail.Mailbox != f.Name || !slices.Equal(it.Mail.UIDs, []uint32{7})) {
			t.Fatalf("bad new generation: %+v", it)
		}
	}
}

// Omitting the mailbox in a translation request means INBOX, even if a folder has the same UID and a saved translation.
func TestInboxMailFolderTranslation(t *testing.T) {
	h := newInboxHub(t)
	ctx := t.Context()
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	msg := seenMail(1, "Receipt", false, time.Minute, "me@example.test")
	a := assistantwire.InboxMailAccount{ID: "acc", Folders: []assistantwire.InboxMailFolder{{Name: "Receipts", Messages: []assistantwire.InboxMailSummary{msg}}}}
	items := h.inbox.applyMail(a, nil, time.Now())
	if len(items) != 1 {
		t.Fatal(items)
	}
	items[0].Translations = map[string]*InboxTranslation{"zh-Hans": {Of: items[0].Last, Text: "收据", ModelFingerprint: "model"}}
	ref := assistantwire.InboxMailRef{AccountID: "acc", UID: 1}
	if _, ok := h.storedMailTranslation(ctx, ref, "zh-Hans", "model"); ok {
		t.Fatal("INBOX returned another folder's translation")
	}
	ref.Mailbox = "Receipts"
	if got, ok := h.storedMailTranslation(ctx, ref, "zh-Hans", "model"); !ok || got.Text != "收据" {
		t.Fatalf("folder translation = %+v, %v", got, ok)
	}
}

// A completed INBOX thread at UID 9000 cannot consume the lower UIDs from rules for the same subject.
func TestInboxMailFolderBelowCompletedThread(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	if err := h.inboxReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	a := assistantwire.InboxMailAccount{ID: "acc", Messages: []assistantwire.InboxMailSummary{seenMail(9000, "CI failed", false, time.Minute, "me@example.test")}}
	items := h.inbox.applyMail(a, a.Messages, now)
	if len(items) != 1 {
		t.Fatalf("got %d INBOX items", len(items))
	}
	items[0].done(now, "user")
	msg := seenMail(50, "CI failed", false, time.Minute, "me@example.test")
	for _, name := range []string{"Rules/GitHub", "Rules/github"} {
		a.Folders = append(a.Folders, assistantwire.InboxMailFolder{Name: name, Messages: []assistantwire.InboxMailSummary{msg}})
	}
	items = h.inbox.applyMail(a, a.Messages, now)
	if len(items) != 2 || items[0].ID == items[1].ID || atomOf(items[0]) == atomOf(items[1]) || !items[0].open() || !items[1].open() {
		t.Fatalf("expected independent items in case-sensitive folders, got %+v", items)
	}
	a.ID, a.Messages = "other", nil
	if other := h.inbox.applyMail(a, nil, now); len(other) != 2 || other[0].ID == items[0].ID {
		t.Fatalf("other account = %+v", other)
	}
}

// A pre-upgrade INBOX baseline has no known UIDVALIDITY. Keep its existing lower-page recovery at migration.
func TestInboxMailFolderBaselineUpgrade(t *testing.T) {
	b := inboxBook{mailBaselines: map[string]uint32{"acc": 9000}}
	f := assistantwire.InboxMailFolder{Name: "INBOX", UIDValidity: 12, Messages: []assistantwire.InboxMailSummary{{UID: 50}}}
	if baseline, since, gen := b.mailBaseline("acc", f); baseline != 50 || since || gen != 0 {
		t.Fatalf("upgraded baseline = %d, %v, generation %d", baseline, since, gen)
	}
	f.Messages = nil // With the same known validity, an empty page can also mean a user cleared the mailbox.
	if baseline, since, gen := b.mailBaseline("acc", f); baseline != 50 || !since || gen != 0 {
		t.Fatalf("cleared mailbox reset its baseline: %d, %v, generation %d", baseline, since, gen)
	}
}
