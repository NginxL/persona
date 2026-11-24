package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"net/mail"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
)

// RenderMessage builds the RFC 5322 message for a send request. Sender is the
// From address, e.g. "Alice <me@example.com>".
func RenderMessage(req SendRequest, from Address) ([]byte, error) {
	if len(req.To) == 0 && len(req.Cc) == 0 && len(req.Bcc) == 0 {
		return nil, errors.New("mail: no recipients")
	}

	var h gomail.Header
	h.SetDate(time.Now())
	if err := h.GenerateMessageIDWithHostname(messageIDDomain(from.Email)); err != nil {
		// A missing Message-ID is not fatal, but it breaks threading; fall back
		// to a random local id.
		h.SetMessageID(fmt.Sprintf("%d.persona", time.Now().UnixNano()))
	}
	h.SetSubject(req.Subject)
	h.SetAddressList("From", []*mail.Address{{Name: from.Name, Address: from.Email}})
	h.SetAddressList("To", toNetAddresses(req.To))
	if len(req.Cc) > 0 {
		h.SetAddressList("Cc", toNetAddresses(req.Cc))
	}
	if req.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{trimMsgID(req.InReplyTo)})
	}
	if refs := parseReferences(string(req.References), req.InReplyTo); len(refs) > 0 {
		h.SetMsgIDList("References", refs)
	}

	var buf bytes.Buffer
	if req.HTML != "" {
		mw, err := gomail.CreateInlineWriter(&buf, h)
		if err != nil {
			return nil, err
		}
		if req.Text != "" {
			tw, err := mw.CreatePart(textPartHeader())
			if err != nil {
				return nil, err
			}
			if _, err := tw.Write([]byte(req.Text)); err != nil {
				return nil, err
			}
			if err := tw.Close(); err != nil {
				return nil, err
			}
		}
		hw, err := mw.CreatePart(htmlPartHeader())
		if err != nil {
			return nil, err
		}
		if _, err := hw.Write([]byte(req.HTML)); err != nil {
			return nil, err
		}
		if err := hw.Close(); err != nil {
			return nil, err
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
	} else {
		// CreateSingleInlineWriter sets no Content-Type; without one the body is US-ASCII (RFC 2045 §5.2).
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		sw, err := gomail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, err
		}
		if _, err := sw.Write([]byte(req.Text)); err != nil {
			return nil, err
		}
		if err := sw.Close(); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// messageIDDomain is the part of a sent message's Message-ID after the @: the sender's domain, so the ID does not carry
// the host name of the machine the hub runs on. email is already lowercased by normalizeEmail before it is stored.
func messageIDDomain(email string) string {
	if i := strings.LastIndexByte(email, '@'); i >= 0 && i+1 < len(email) {
		return email[i+1:]
	}
	return "localhost"
}

func textPartHeader() gomail.InlineHeader {
	var h gomessage.Header
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	return gomail.InlineHeader{Header: h}
}

func htmlPartHeader() gomail.InlineHeader {
	var h gomessage.Header
	h.SetContentType("text/html", map[string]string{"charset": "utf-8"})
	return gomail.InlineHeader{Header: h}
}

func toNetAddresses(addrs []Address) []*mail.Address {
	out := make([]*mail.Address, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, &mail.Address{Name: a.Name, Address: strings.TrimSpace(a.Email)})
	}
	return out
}

// trimMsgID strips angle brackets so go-message can add them back.
func trimMsgID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return s
}

// parseReferences splits a References value into message ids.
func parseReferences(refs, inReplyTo string) []string {
	refs = strings.TrimSpace(refs)
	if refs == "" {
		if inReplyTo == "" {
			return nil
		}
		return []string{trimMsgID(inReplyTo)}
	}
	var out []string
	for _, tok := range strings.Fields(refs) {
		id := trimMsgID(tok)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// Send composes and delivers a message over SMTP, then appends it to the
// provider's Sent mailbox when the provider does not do that itself.
func (s *Service) Send(ctx context.Context, req SendRequest) error {
	_, err := s.SendMessage(ctx, req)
	return err
}

// SendMessage is Send that also says the Message-ID of the message handed to the transport, as its header has it but
// without the angle brackets (the form MailSummary.MessageID has). It is empty when the message did not go. Whether the
// provider keeps that Message-ID on the message it delivers has not been verified.
func (s *Service) SendMessage(ctx context.Context, req SendRequest) (string, error) {
	a, err := s.account(req.AccountID)
	if err != nil {
		return "", err
	}
	from := Address{Name: a.Name, Email: a.Email}
	if a.Name == "" || a.Name == a.Email {
		from.Name = ""
	}
	raw, err := RenderMessage(req, from)
	if err != nil {
		return "", err
	}
	var rcpts []string
	for _, list := range [][]Address{req.To, req.Cc, req.Bcc} {
		for _, addr := range list {
			rcpts = append(rcpts, strings.TrimSpace(addr.Email))
		}
	}

	if s.sendMail != nil {
		if err := s.sendMail(ctx, a, a.Email, rcpts, raw); err != nil {
			return "", err
		}
	} else if a.Auth == AuthOAuth2 && a.Provider == ProviderGmail {
		if err := s.gmailAPISend(ctx, a, req.Bcc, raw); err != nil {
			return "", err
		}
	} else if err := s.smtpSend(ctx, a, a.Email, rcpts, raw); err != nil {
		if a.Provider == ProviderOutlook {
			return "", outlookHint(fmt.Errorf("smtp: %w", err))
		}
		return "", err
	}

	// Gmail and Exchange file what they send in Sent themselves.
	if a.Provider != ProviderGmail && a.Provider != ProviderOutlook {
		s.appendToSent(ctx, a, raw)
	}
	return messageIDOf(raw), nil
}

// messageIDOf is the Message-ID of a rendered message, as its header has it but without the angle brackets: read back
// from the message handed to the transport, so it is the one Persona wrote, not a promise about what a provider makes
// of it. Empty when the header is missing.
func messageIDOf(raw []byte) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return trimMsgID(m.Header.Get("Message-Id"))
}

// smtpSend delivers raw over SMTP with the account's security settings.
func (s *Service) smtpSend(ctx context.Context, a *MailAccount, from string, rcpts []string, raw []byte) error {
	security := a.SMTPSecurity
	if security == "" {
		if a.SMTPPort == 465 {
			security = "tls"
		} else {
			security = "starttls"
		}
	}
	addr := serverAddr(a.SMTPHost, a.SMTPPort)
	tlsConfig := &tls.Config{ServerName: a.SMTPHost}

	type dialResult struct {
		c   *smtp.Client
		err error
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, err := dialSMTP(ctx, s.proxyFor(a), addr, security, tlsConfig)
		ch <- dialResult{c, err}
	}()
	var client *smtp.Client
	select {
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("smtp: dial %s: %w", addr, r.err)
		}
		client = r.c
	}
	defer client.Close()
	client.CommandTimeout = networkTimeout
	client.SubmissionTimeout = networkTimeout

	_ = client.Hello(ehloName)

	if auth, err := s.smtpAuth(ctx, a); err != nil {
		return err
	} else if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}

	// MAIL FROM with UTF-8 support when the server offers SMTPUTF8 and an address needs it: a server refuses a non-ASCII
	// recipient in RCPT TO unless MAIL FROM declared SMTPUTF8 (RFC 6531).
	var mailOpts *smtp.MailOptions
	needUTF8 := !isASCII(from) || !isASCII(a.Username) ||
		slices.ContainsFunc(rcpts, func(r string) bool { return !isASCII(r) })
	if ok, _ := client.Extension("SMTPUTF8"); ok && needUTF8 {
		mailOpts = &smtp.MailOptions{UTF8: true}
	}
	if err := client.Mail(from, mailOpts); err != nil {
		return fmt.Errorf("smtp: mail from: %w", err)
	}
	for _, rcpt := range rcpts {
		if err := client.Rcpt(rcpt, nil); err != nil {
			return fmt.Errorf("smtp: rcpt %s: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		// A server that answers the end of the body with an error (an *smtp.SMTPError: 554 and the like) did not take
		// the message. With no answer (the connection dropped, the wait for it ran out) the server has the whole body and
		// may have taken it: sent again, it would go twice (#9166).
		var refused *smtp.SMTPError
		if !errors.As(err, &refused) {
			return fmt.Errorf("smtp: finish body: %w: %w", ErrMaybeSent, err)
		}
		return fmt.Errorf("smtp: finish body: %w", err)
	}
	// The server took the message with its reply to the end of DATA, so it is delivered. QUIT is only a goodbye:
	// some servers hang up or time out instead of answering it, and reporting that as a failed send gets the
	// message sent twice (#4560). The deferred Close drops the connection either way.
	if err := client.Quit(); err != nil {
		logf("mail: smtp quit after delivery to %s: %v", addr, err)
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// ehloName is the name the SMTP client greets the server with: an address literal, which RFC 5321 (section 4.1.4) asks
// of a client whose host name does not resolve. The machine's own name is also private, and servers record the EHLO
// name in the Received header of every message they accept (section 4.4).
const ehloName = "[127.0.0.1]"

// checkSMTP verifies the SMTP server is reachable and the credentials are
// accepted, without sending any mail.
func (s *Service) checkSMTP(ctx context.Context, a *MailAccount) error {
	security := a.SMTPSecurity
	if security == "" {
		if a.SMTPPort == 465 {
			security = "tls"
		} else {
			security = "starttls"
		}
	}
	addr := serverAddr(a.SMTPHost, a.SMTPPort)
	tlsConfig := &tls.Config{ServerName: a.SMTPHost}
	var (
		client *smtp.Client
		err    error
	)
	client, err = dialSMTP(ctx, s.proxyFor(a), addr, security, tlsConfig)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer client.Close()
	client.CommandTimeout = networkTimeout

	_ = client.Hello(ehloName)
	auth, err := s.smtpAuth(ctx, a)
	if err != nil {
		return err
	}
	if err := client.Auth(auth); err != nil {
		return smtpLoginErr(a, err)
	}
	// The login was accepted, which is what the check is for. QUIT is only a goodbye: a server that hangs up
	// or times out on it still has a working account (#4616, as for sending in #4560).
	if err := client.Quit(); err != nil {
		logf("mail: smtp quit after check of %s: %v", addr, err)
	}
	return nil
}

// appendToSent copies the sent message into the account's Sent mailbox. Failures
// are logged but not returned: the mail itself was delivered.
func (s *Service) appendToSent(ctx context.Context, a *MailAccount, raw []byte) {
	ctx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()
	sent, err := s.sentMailbox(ctx, a)
	if err != nil {
		logf("mail: find sent mailbox for %s: %v", a.Email, err)
		return
	}
	if sent == "" {
		return
	}
	err = s.withConn(ctx, a, "save to "+sent, func(c *imapclient.Client) error {
		cmd := c.Append(sent, int64(len(raw)), &imap.AppendOptions{
			Flags: []imap.Flag{imap.FlagSeen},
			Time:  time.Now(),
		})
		if _, err := cmd.Write(raw); err != nil {
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		_, err := cmd.Wait()
		return err
	})
	if err != nil {
		logf("mail: append to %s for %s: %v", sent, a.Email, err)
	}
}

// sentMailbox finds the Sent mailbox name, preferring SPECIAL-USE \Sent.
func (s *Service) sentMailbox(ctx context.Context, a *MailAccount) (string, error) {
	var out string
	err := s.withConn(ctx, a, "find sent mailbox", func(c *imapclient.Client) error {
		out = ""
		data, err := c.List("", "*", listOptions(c.Caps(), nil)).Collect()
		if err != nil {
			return err
		}
		for _, ld := range data {
			if hasAttr(ld.Attrs, imap.MailboxAttrSent) {
				out = ld.Mailbox
				return nil
			}
		}
		for _, ld := range data {
			if roleOf(ld.Mailbox, ld.Attrs) == RoleSent {
				out = ld.Mailbox
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}
