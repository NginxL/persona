package mail

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// QQ-style IMAP4rev1 accepts plain LIST. The nested and non-ASCII names returned here must work verbatim in SELECT.
func TestIntakeMailboxes(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.ignoreListReturn(t)
	for _, name := range []string{"GitHub", "GitHub/通知", "Important", "草稿箱", " INBOX "} {
		if err := srv.user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, box := range []string{"GitHub/通知", " INBOX "} {
		srv.appendMessage(t, box, rawMessage("CI failed", "ci@example.test", "build result"))
	}
	s := newServiceWithAccount(t, srv)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	boxes, err := s.IntakeMailboxes(ctx, "acc_test")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"INBOX", " INBOX ", "Archive", "GitHub", "GitHub/通知", "Important"}; !slices.Equal(boxes, want) {
		t.Fatalf("folders = %q, want %q", boxes, want)
	}
	for _, box := range []string{"GitHub/通知", " INBOX "} {
		page, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Mailbox: box})
		if err != nil || len(page.Messages) != 1 || page.Messages[0].Mailbox != box || page.Messages[0].Seen {
			t.Fatalf("rule-delivered message in %q = %+v, %v", box, page, err)
		}
	}
}

func TestIntakeMailboxRoles(t *testing.T) {
	for _, attr := range []imap.MailboxAttr{imap.MailboxAttrNoSelect, imap.MailboxAttrNonExistent, imap.MailboxAttrAll,
		imap.MailboxAttrFlagged, imap.MailboxAttrImportant, imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrTrash, imap.MailboxAttrJunk} {
		if intakeMailbox(&imap.ListData{Mailbox: "localized name", Attrs: []imap.MailboxAttr{attr}}) {
			t.Errorf("included mailbox with role %q", attr)
		}
	}
	for _, name := range []string{"Drafts", "Sent Items", "Trash", "Spam", "已发送", "草稿箱", "垃圾箱", "[Gmail]/All Mail", "[Google Mail]/Starred"} {
		if intakeMailbox(&imap.ListData{Mailbox: name}) {
			t.Errorf("included system mailbox %q", name)
		}
	}
	for _, name := range []string{"Reports/Sent", "Starred", "Important", "Apple", "大小写"} {
		if !intakeMailbox(&imap.ListData{Mailbox: name}) {
			t.Errorf("excluded custom mailbox %q", name)
		}
	}
	if !intakeMailbox(&imap.ListData{Mailbox: "归档", Attrs: []imap.MailboxAttr{imap.MailboxAttrArchive}}) {
		t.Error("excluded selectable archive")
	}
}
