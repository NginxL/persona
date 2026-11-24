package extmgr

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/cliwire"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// inboxSendWait bounds one reply's send.
const inboxSendWait = 2 * time.Minute

// errReplyInDoubt: a send under this id was cut off before (the process stopped or a link on the way dropped during
// it, or it ran out of time): it may have gone out, so it is not sent again.
var errReplyInDoubt = &rpc.Error{Code: assistantwire.CodeAssistantInDoubt, Message: "这条回复上次发送时中断了，可能已经发出去了：先看看再决定要不要重发"}

// sendInboxOnce sends one of the manager's replies at most once per send id, with the guard the apps' messages have
// (sent_messages, kept for good): the same id after it went out is already sent (nothing goes again), after a send that was
// cut off it is in doubt (errReplyInDoubt). A send that failed is forgotten: asked again, it is sent. A send once
// begun runs to its end (inboxSendWait) even when the manager stops waiting or drops: what it did is recorded for the
// retry. Without an id or the database it is just sent. A failure comes back coded (inboxSendFailure): in doubt, or
// the reason it did not go out (#9125).
func (h *Manager) sendInboxOnce(ctx context.Context, sendID, source string, send func(context.Context) error) (already bool, err error) {
	st, _ := h.store.(sendStore)
	if sendID == "" || st == nil {
		err = send(ctx)
		return false, h.inboxSendFailure(sendID, source, inboxSendOutcome(err), err)
	}
	id := "inbox:" + sendID
	state, err := st.ClaimSend(ctx, id, "inbox", source)
	switch {
	case err != nil: // nothing was sent: without its record the send could go out twice
		h.logf("inbox: reply %s through %s not sent: %s (claim: %T)", sendID, source, assistantwire.CodeInboxSendUnrecorded, err)
		return false, inboxSendError(assistantwire.CodeInboxSendUnrecorded, 0)
	case state == sendDelivered:
		return true, nil
	case state != "":
		return false, errReplyInDoubt
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inboxSendWait)
	defer cancel()
	err = send(sctx)
	outcome := inboxSendOutcome(err)
	if serr := st.SettleSend(context.WithoutCancel(ctx), id, outcome); serr != nil {
		h.logf("inbox: settle reply %s: %v", sendID, serr)
	}
	return false, h.inboxSendFailure(sendID, source, outcome, err)
}

// inboxSendFailure is what the manager is told of a send that ended in err (nil: it went out), its outcome
// (inboxSendOutcome) taken first: one that may have gone out is assistant_in_doubt, whatever the source said; one the
// source refused is a coded reason (inboxSendRefusal). Either is logged with an allowlisted cause only.
func (h *Manager) inboxSendFailure(sendID, source, outcome string, err error) error {
	switch {
	case err == nil:
		return nil
	case outcome == sendUnknown:
		h.logf("inbox: reply %s through %s may have gone out: %s (%s)", sendID, source, assistantwire.CodeAssistantInDoubt, inboxSendLogKind(err))
		return &rpc.Error{Code: assistantwire.CodeAssistantInDoubt, Message: errReplyInDoubt.Message}
	}
	refusal := inboxSendRefusal(err) // nothing went out: its record is forgotten, so the same reply may be sent again
	h.logf("inbox: reply %s through %s not sent: %s (%s)", sendID, source, refusal.Code, inboxSendLogKind(err))
	return refusal
}

// inboxSendOutcome is what a reply's error says of it (sent_messages' state): cut off (the time ran out, a link on the
// way dropped, a mail server that got the message and did not answer it) it may have gone out; refused it did not ("":
// forgotten).
func inboxSendOutcome(err error) string {
	switch {
	case err == nil:
		return sendDelivered
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, rpc.ErrDropped),
		cliwire.KindOf(err) == cliwire.KindTimeout, // lark-cli stopped by its own deadline: the message may be out
		errors.Is(err, mailwire.ErrMaybeSent):      // the SMTP server has the whole mail and never answered it (#9166)
		return sendUnknown
	}
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == assistantwire.CodeAssistantInDoubt { // a manager further on was in doubt
		return sendUnknown
	}
	return ""
}

// replyMail answers a mail from the account that received it.
func (h *Manager) replyMail(ctx context.Context, ref assistantwire.InboxMailRef, text string) error {
	if h.mail == nil {
		return errCannotReply
	}
	orig, err := h.mail.GetMessage(ctx, ref.AccountID, ref.UID, ref.Mailbox)
	if err != nil {
		return err
	}
	return h.mail.Send(ctx, mailReply(h.userLang(), ref.AccountID, orig, text))
}

// mailReply is a reply to orig: Re: subject, threading headers, the original quoted below.
func mailReply(lang i18n.Lang, accountID string, orig *mailwire.MailMessage, text string) mailwire.SendRequest {
	subject := orig.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	refs := append(append([]string{}, orig.References...), orig.MessageID)
	var quoted []string
	for i, l := range strings.Split(orig.Text, "\n") {
		if i >= 40 {
			break
		}
		quoted = append(quoted, "> "+l)
	}
	from := orig.From.Name
	if from == "" {
		from = orig.From.Email
	}
	body := text + "\n\n" + i18n.T(lang, "hub-inbox.reply_attribution", i18n.Args{"date": orig.Date, "from": from}) + "\n" + strings.Join(quoted, "\n")
	return mailwire.SendRequest{AccountID: accountID, To: []mailwire.Address{orig.From}, Subject: subject, Text: body,
		InReplyTo: orig.MessageID, References: mailwire.MessageIDs(strings.Join(refs, " "))}
}
