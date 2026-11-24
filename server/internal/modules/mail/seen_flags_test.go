package mail

import (
	"context"
	"maps"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// SeenFlags says which of the asked mails are seen, and leaves out one the mailbox no longer has; asking marks
// nothing seen (#5993).
func TestSeenFlags(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()
	unread := uint32(srv.appendMessage(t, "INBOX", rawMessage("Unread", "a@example.com", "one")))
	read := uint32(srv.appendMessage(t, "INBOX", rawMessage("Read", "b@example.com", "two"), imap.FlagSeen))
	const gone = 999

	got, err := s.SeenFlags(ctx, "acc_test", "INBOX", []uint32{unread, read, gone})
	if err != nil {
		t.Fatalf("SeenFlags: %v", err)
	}
	if want := map[uint32]bool{unread: false, read: true}; !maps.Equal(got, want) {
		t.Fatalf("SeenFlags = %v, want %v (a UID the mailbox does not have is left out)", got, want)
	}
	again, err := s.SeenFlags(ctx, "acc_test", "", []uint32{unread})
	if err != nil || again[unread] {
		t.Fatalf("asking again (default mailbox) = %v, %v: asking must not mark the mail seen", again, err)
	}
	if none, err := s.SeenFlags(ctx, "acc_test", "INBOX", nil); err != nil || len(none) != 0 {
		t.Fatalf("no UIDs = %v, %v", none, err)
	}
	if _, err := s.SeenFlags(ctx, "nobody", "INBOX", []uint32{unread}); err == nil {
		t.Fatal("an unknown account must fail")
	}
}
