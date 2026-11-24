package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// replyFailMail is mail whose replies fail with err; it counts the sends tried.
type replyFailMail struct {
	Mail
	err   error
	sends atomic.Int32
}

func (m *replyFailMail) GetMessage(context.Context, string, uint32, string) (*mailwire.MailMessage, error) {
	return &mailwire.MailMessage{From: mailwire.Address{Email: "li@example.com"}, Subject: "Question", MessageID: "q1@example.com", Text: "When?"}, nil
}

func (m *replyFailMail) Send(context.Context, mailwire.SendRequest) error {
	m.sends.Add(1)
	return m.err
}

// A mail reply whose message the SMTP server got and never answered may have gone out: it is assistant_in_doubt, its
// record stays unknown, and the same reply asked again is not sent. The server's refusal of it went nowhere: a reason,
// the record forgotten, and the same reply asked again is sent again (#9166).
func TestInboxMailReplyItsServerDidNotAnswerStaysInDoubt(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		code   string
		sends  int32
		record string
		log    string
	}{
		{"no answer to the message", fmt.Errorf("smtp: finish body: %w: %w", mailwire.ErrMaybeSent, io.EOF), assistantwire.CodeAssistantInDoubt, 1, sendUnknown,
			"may have gone out: assistant_in_doubt (mail: " + mailwire.ErrMaybeSent.Error() + ")"},
		{"refused by the server", errors.New("smtp: finish body: 554 5.7.1 message rejected"), assistantwire.CodeInboxSendFailed, 2, "",
			"not sent: inbox_send_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, logs := sendTestManager(t, "/nonexistent/lark-cli", time.Second)
			m := &replyFailMail{err: c.err}
			h.mail = m
			raw, _ := json.Marshal(map[string]any{"account_id": "a", "uid": 7, "mailbox": "INBOX", "text": "the reply text itself", "send_id": "send-1"})
			for attempt := 1; attempt <= 2; attempt++ {
				_, _, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInbox, Method: assistantwire.InboxReplyMail, Params: raw})
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != c.code {
					t.Fatalf("attempt %d: %#v, want %s", attempt, err, c.code)
				}
			}
			if n := m.sends.Load(); n != c.sends {
				t.Fatalf("the reply was sent %d times, want %d", n, c.sends)
			}
			if st := h.store.(*a2SentStore).rows["inbox:send-1"]; st != c.record {
				t.Fatalf("its record: %q, want %q", st, c.record)
			}
			if l := logs(); !strings.Contains(l, "through mail "+c.log) {
				t.Fatalf("the log does not say it: %s", l)
			}
		})
	}
}
