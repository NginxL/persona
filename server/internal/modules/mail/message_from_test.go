package mail

import (
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestMessageFromRetainsHeaderRecipients(t *testing.T) {
	for _, tc := range []struct {
		name string
		to   []imap.Address
		want []Address
	}{
		{
			name: "empty envelope recipients",
			want: []Address{{Name: "Header", Email: "header@example.test"}, {Email: "second@example.test"}},
		},
		{
			name: "envelope recipients take precedence",
			to:   []imap.Address{{Name: "Envelope", Mailbox: "envelope", Host: "example.test"}},
			want: []Address{{Name: "Envelope", Email: "envelope@example.test"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &imapclient.FetchMessageBuffer{
				UID:   7,
				Flags: []imap.Flag{imap.FlagSeen, imap.FlagFlagged},
				Envelope: &imap.Envelope{
					Subject: "Envelope subject", MessageID: "message@example.test", To: tc.to,
				},
				BodySection: []imapclient.FetchBodySectionBuffer{{
					Section: &imap.FetchItemBodySection{},
					Bytes: []byte("To: Header <header@example.test>, second@example.test\r\n" +
						"Content-Type: text/plain; charset=utf-8\r\n\r\nMessage body"),
				}},
			}
			msg := messageFrom(&MailAccount{ID: "test-account"}, "INBOX", m)
			if !slices.Equal(msg.To, tc.want) {
				t.Errorf("recipients = %+v, want %+v", msg.To, tc.want)
			}
			if msg.AccountID != "test-account" || msg.Mailbox != "INBOX" || msg.UID != 7 || msg.Subject != "Envelope subject" || msg.MessageID != "message@example.test" || !msg.Seen || !msg.Flagged {
				t.Errorf("envelope metadata changed: %+v", msg.MailSummary)
			}
			if msg.Text != "Message body" || msg.Snippet != "Message body" || msg.HasAttach || len(msg.Attachments) != 0 {
				t.Errorf("message body or attachment state changed: %+v", msg)
			}
		})
	}
}
