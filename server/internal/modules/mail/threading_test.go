package mail

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// One comma, obsolete phrase or id that go-message rejects must not cost the rest of the thread (RFC 5322 4.5.4).
func TestParseMessageThreadingHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, headers string
		messageID     string
		inReplyTo     string
		references    []string
	}{
		{"normal", "In-Reply-To: <p2@example.com>\r\nReferences: <p1@example.com> <p2@example.com>\r\n",
			"", "p2@example.com", []string{"p1@example.com", "p2@example.com"}},
		{"folded", "References: <p1@example.com>\r\n <p2@example.com>\r\n\t<p3@example.com>\r\n",
			"", "", []string{"p1@example.com", "p2@example.com", "p3@example.com"}},
		{"comma", "References: <p1@example.com>, <p2@example.com>\r\n", "", "", []string{"p1@example.com", "p2@example.com"}},
		{"obsolete phrase", "In-Reply-To: Your message of Mon, 2 Nov 2026 <p2@example.com>\r\n", "", "p2@example.com", []string{}},
		{"id without at", "Message-ID: <m.example>\r\nReferences: <p0.example> <p1@example.com> <p2@example.com>\r\n",
			"m.example", "", []string{"p0.example", "p1@example.com", "p2@example.com"}},
		{"quoted local part", "In-Reply-To: <\"p 1\"@example.com> <\"p2\"@example.com>\r\nReferences: <p1@example.com> <\"p2\"@example.com>\r\n",
			"", `"p2"@example.com`, []string{"p1@example.com", `"p2"@example.com`}},
		{"empty, unclosed and control groups", "References: <> < p1@example.com > <p2@example.com <p3@example.com> <p\x01@example.com> <p4\r\n",
			"", "", []string{"p1@example.com", "p3@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "From: a@example.com\r\nSubject: s\r\n" + tc.headers + "\r\nbody\r\n"
			msg := &MailMessage{References: []string{}}
			parseMessage([]byte(raw), msg)
			if msg.MessageID != tc.messageID || msg.InReplyTo != tc.inReplyTo || !slices.Equal(msg.References, tc.references) {
				t.Fatalf("Message-ID %q, In-Reply-To %q, References %q; want %q, %q, %q",
					msg.MessageID, msg.InReplyTo, msg.References, tc.messageID, tc.inReplyTo, tc.references)
			}
		})
	}
	// The envelope's Message-ID wins over the header.
	msg := &MailMessage{MailSummary: MailSummary{MessageID: "envelope@example.com"}}
	parseMessage([]byte("Message-ID: <m.example>\r\n\r\nbody\r\n"), msg)
	if msg.MessageID != "envelope@example.com" {
		t.Fatalf("Message-ID = %q", msg.MessageID)
	}
}

// GetMessage and mail_read keep the whole chain, so a reply built from them threads under the root.
func TestGetMessageThreadingHeadersLenient(t *testing.T) {
	srv := newTestIMAP(t)
	service := newServiceWithAccount(t, srv)
	ctx := context.Background()
	raw := "From: sender@example.com\r\nTo: me@example.com\r\nSubject: Re: plan\r\nMessage-ID: <p3.example>\r\n" +
		"In-Reply-To: Your message of Mon, 2 Nov 2026 <p2@example.com>\r\n" +
		"References: <p0.example>, <p1@example.com>\r\n <p2@example.com>\r\n\r\nbody\r\n"
	uid := uint32(srv.appendMessage(t, "INBOX", raw))
	msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"p0.example", "p1@example.com", "p2@example.com"}; msg.MessageID != "p3.example" ||
		msg.InReplyTo != "p2@example.com" || !slices.Equal(msg.References, want) {
		t.Fatalf("Message-ID %q, In-Reply-To %q, References %q", msg.MessageID, msg.InReplyTo, msg.References)
	}
	args, _ := json.Marshal(map[string]any{"account_id": "acc_test", "uid": uid})
	read := service.Call(ctx, ToolRead, args, nil)
	if read.IsError || len(read.Content) != 1 {
		t.Fatalf("mail_read: %+v", read)
	}
	for _, line := range []string{"Message-ID: <p3.example>\n", "In-Reply-To: <p2@example.com>\n",
		"References: <p0.example> <p1@example.com> <p2@example.com>\n"} {
		if !strings.Contains(read.Content[0].Text, line) {
			t.Fatalf("mail_read lacks %q:\n%s", line, read.Content[0].Text)
		}
	}
}
