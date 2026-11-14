package mail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// GetMessage fetches one full message. It never marks the message as seen.
func (s *Service) GetMessage(ctx context.Context, accountID string, uid uint32, mailbox string) (*MailMessage, error) {
	a, err := s.account(accountID)
	if err != nil {
		return nil, err
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	var out *MailMessage
	err = s.withConn(ctx, a, func(c *imapclient.Client) error {
		out = nil
		if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
			UID:          true,
			Envelope:     true,
			Flags:        true,
			InternalDate: true,
			RFC822Size:   true,
			BodySection:  []*imap.FetchItemBodySection{{Peek: true}},
		}).Collect()
		if err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
		if len(msgs) == 0 {
			return fmt.Errorf("message %d not found in %s", uid, mailbox)
		}
		out = messageFrom(a, mailbox, msgs[0])
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SeenFlags says, for each of uids still in mailbox, whether it is seen; a UID the mailbox no longer has (moved,
// archived or deleted) is not in the map. One read-only UID FETCH (FLAGS): nothing is marked.
func (s *Service) SeenFlags(ctx context.Context, accountID, mailbox string, uids []uint32) (map[uint32]bool, error) {
	a, err := s.account(accountID)
	if err != nil {
		return nil, err
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	out := map[uint32]bool{}
	if len(uids) == 0 {
		return out, nil
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	err = s.withConn(ctx, a, func(c *imapclient.Client) error {
		clear(out)
		if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
		if err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
		for _, m := range msgs {
			out[uint32(m.UID)] = hasFlag(m.Flags, imap.FlagSeen)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// messageFrom parses a fully fetched message.
func messageFrom(a *MailAccount, mailbox string, m *imapclient.FetchMessageBuffer) *MailMessage {
	sum := MailSummary{
		AccountID: a.ID,
		Mailbox:   mailbox,
		UID:       uint32(m.UID),
		Seen:      hasFlag(m.Flags, imap.FlagSeen),
		Answered:  hasFlag(m.Flags, imap.FlagAnswered),
		Flagged:   hasFlag(m.Flags, imap.FlagFlagged),
	}
	if m.Envelope != nil {
		sum.Subject = m.Envelope.Subject
		sum.MessageID = m.Envelope.MessageID
		sum.From = firstAddress(m.Envelope.From)
		for _, addr := range m.Envelope.To {
			if e := addr.Addr(); e != "" {
				sum.To = append(sum.To, Address{Name: addr.Name, Email: e})
			}
		}
		if !m.Envelope.Date.IsZero() {
			sum.Date = m.Envelope.Date.UTC().Format(time.RFC3339)
		}
	}
	if sum.Date == "" && !m.InternalDate.IsZero() {
		sum.Date = m.InternalDate.UTC().Format(time.RFC3339)
	}
	msg := &MailMessage{MailSummary: sum, Attachments: []Attachment{}, References: []string{}, Cc: []Address{}}

	var raw []byte
	for _, sec := range m.BodySection {
		if sec.Section != nil && len(sec.Section.Part) == 0 && sec.Section.Specifier == imap.PartSpecifierNone {
			raw = sec.Bytes
		}
	}
	if raw == nil && len(m.BodySection) > 0 {
		raw = m.BodySection[0].Bytes
	}
	parseMessage(raw, msg)
	msg.HasAttach = len(msg.Attachments) > 0
	msg.Snippet = truncateRunes(strings.Join(strings.Fields(msg.Text), " "), snippetRunes)
	if msg.Snippet == "" {
		msg.Snippet = truncateRunes(strings.Join(strings.Fields(stripHTML(msg.HTML)), " "), snippetRunes)
	}
	return msg
}

// parseMessage fills text, html, attachments and threading headers.
func parseMessage(raw []byte, msg *MailMessage) {
	if len(raw) == 0 {
		return
	}
	// Headers first, so threading info survives a body parse failure.
	if h, err := readMailHeader(raw); err == nil {
		// The envelope's Message-ID comes from the same strict parser, so it is empty for an id without '@'.
		if msg.MessageID == "" {
			if ids := msgIDs(h, "Message-Id"); len(ids) > 0 {
				msg.MessageID = ids[0]
			}
		}
		if ids := msgIDs(h, "In-Reply-To"); len(ids) > 0 {
			msg.InReplyTo = ids[len(ids)-1]
		}
		if ids := msgIDs(h, "References"); len(ids) > 0 {
			msg.References = ids
		}
		if len(msg.To) == 0 {
			if addrs, err := h.AddressList("To"); err == nil {
				for _, a := range addrs {
					msg.To = append(msg.To, Address{Name: a.Name, Email: a.Address})
				}
			}
		}
		if addrs, err := h.AddressList("Cc"); err == nil {
			for _, a := range addrs {
				msg.Cc = append(msg.Cc, Address{Name: a.Name, Email: a.Address})
			}
		}
	}

	body := messageBody(raw)
	msg.Text = body.text
	if body.html != "" {
		msg.HTML = body.html
	}

	// Attachments come from a walk of their own over the flat part list: their IDs count parts in the order the
	// download (FetchAttachment, the HTTP handler) reads them through nextPart, so both must walk the same way.
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		_ = err
		return
	}
	defer r.Close()
	attachmentIndex := 0
	for {
		part, err := nextPart(r)
		if err != nil {
			break
		}
		if h, ok := part.Header.(*gomail.AttachmentHeader); ok {
			// Downloads count every attachment nextPart returns, including parts whose body cannot be decoded.
			attachmentIndex++
			attachment, err := attachmentMetadata(h, part.Body, attachmentIndex, io.Discard)
			if err == nil {
				msg.Attachments = append(msg.Attachments, attachment)
			}
		}
	}
	if msg.Text == "" && msg.HTML != "" {
		msg.Text = stripHTML(msg.HTML)
	}
	if msg.Attachments == nil {
		msg.Attachments = []Attachment{}
	}
}

// maxBodyBytes caps the text and HTML a message's body may take in all.
const maxBodyBytes = 8 << 20

// mailBody is the readable body of a message or of one MIME entity in it.
type mailBody struct {
	text string // text/plain
	html string // text/html
}

// messageBody collects a message's text and HTML body from its MIME tree. The body can come in several inline parts:
// Apple Mail on iPhone and Mac splits it around every file or photo placed in the text (multipart/mixed {text, image,
// text}, or multipart/related {html, image, html}), so the parts of a mixed or related entity are joined in order
// (RFC 2046 5.1.3). The parts of a multipart/alternative are renderings of the same content (5.1.4): one of each kind
// is kept, so nothing is doubled. go-message's mail.Reader flattens the tree and loses that difference, hence the walk.
func messageBody(raw []byte) mailBody {
	e, err := gomessage.Read(bytes.NewReader(raw))
	if e == nil || (err != nil && !gomessage.IsUnknownCharset(err)) {
		return mailBody{} // what gomail.CreateReader gives up on, as the attachment walk does
	}
	w := bodyWalk{left: maxBodyBytes}
	body := w.entity(e, 0)
	if body.text == "" {
		body.text = w.namedText
	}
	if body.html == "" {
		body.html = w.namedHTML
	}
	return body
}

// bodyWalk is one walk of messageBody.
type bodyWalk struct {
	left                 int    // bytes the body may still take
	namedText, namedHTML string // the first named inline text/plain and text/html part, the body only when no other is
}

// entity returns e's body. A part in an unknown charset counts with its raw body; one in an unknown transfer encoding
// is skipped, as nextPart does.
func (w *bodyWalk) entity(e *gomessage.Entity, depth int) mailBody {
	if mr := e.MultipartReader(); mr != nil {
		if depth >= textFromEntityMaxDepth {
			return mailBody{}
		}
		mediaType, _, _ := e.Header.ContentType()
		alternative := mediaType == "multipart/alternative"
		var out mailBody
		for {
			p, err := mr.NextPart()
			if gomessage.IsUnknownEncoding(err) {
				continue
			}
			if p == nil || (err != nil && !gomessage.IsUnknownCharset(err)) {
				return out // end of this multipart (or a truncated tail)
			}
			part := w.entity(p, depth+1)
			if alternative {
				if out.text == "" {
					out.text = part.text
				}
				if out.html == "" {
					out.html = part.html
				}
			} else {
				out.text = joinText(out.text, part.text)
				out.html += part.html
			}
		}
	}
	contentType, disposition := e.Header.Get("Content-Type"), e.Header.Get("Content-Disposition")
	mediaType, _, _ := e.Header.ContentType()
	isText, isHTML := strings.HasPrefix(mediaType, "text/plain"), strings.HasPrefix(mediaType, "text/html")
	// Only text is body; text marked attachment is a file, as in fileAsAttachment.
	if (!isText && !isHTML) || headerToken(disposition) == "attachment" {
		return mailBody{}
	}
	body := readAllTolerant(e.Body, max(w.left, 0))
	w.left -= len(body)
	if strings.TrimSpace(body) == "" {
		return mailBody{} // Apple Mail ends with an empty text part after a file
	}
	if hasParam(disposition, "filename") || hasParam(contentType, "name") {
		// A text file shown inline (notes.txt): not part of the letter.
		if isText && w.namedText == "" {
			w.namedText = body
		}
		if isHTML && w.namedHTML == "" {
			w.namedHTML = body
		}
		return mailBody{}
	}
	if isText {
		return mailBody{text: body}
	}
	return mailBody{html: body}
}

// joinText joins two pieces of a plain text body, on a new line when neither side has one (a file or photo sat
// between them).
func joinText(a, b string) string {
	if a == "" || b == "" || strings.HasSuffix(a, "\n") || strings.HasPrefix(b, "\n") || strings.HasPrefix(b, "\r\n") {
		return a + b
	}
	return a + "\n" + b
}

// readMailHeader parses just the header block of a raw message.
func readMailHeader(raw []byte) (gomail.Header, error) {
	e, err := gomessage.Read(bytes.NewReader(raw))
	if e == nil {
		return gomail.Header{}, err
	}
	return gomail.Header{Header: e.Header}, err
}

// SetFlags updates seen / flagged for one message.
func (s *Service) SetFlags(ctx context.Context, accountID string, uid uint32, req SetFlagsRequest) error {
	a, err := s.account(accountID)
	if err != nil {
		return err
	}
	mailbox := req.Mailbox
	if mailbox == "" {
		mailbox = "INBOX"
	}
	return s.withConn(ctx, a, func(c *imapclient.Client) error {
		if _, err := c.Select(mailbox, nil).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if req.Seen != nil {
			if err := storeFlag(c, set, imap.FlagSeen, *req.Seen); err != nil {
				return err
			}
		}
		if req.Flagged != nil {
			if err := storeFlag(c, set, imap.FlagFlagged, *req.Flagged); err != nil {
				return err
			}
		}
		return nil
	})
}

func storeFlag(c *imapclient.Client, set imap.UIDSet, flag imap.Flag, on bool) error {
	op := imap.StoreFlagsAdd
	if !on {
		op = imap.StoreFlagsDel
	}
	_, err := c.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{flag}}, nil).Collect()
	return err
}
