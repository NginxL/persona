package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// TestAccount connects to IMAP and authenticates, and checks SMTP is reachable.
func (s *Service) TestAccount(id string) error {
	a, err := s.account(id)
	if err != nil {
		return err
	}
	return s.testAccount(context.Background(), a)
}

func (s *Service) testAccount(ctx context.Context, a *MailAccount) error {
	if s.verify != nil {
		return s.verify(ctx, a)
	}
	ctx, cancel := context.WithTimeout(ctx, networkTimeout*2)
	defer cancel()

	c, err := s.dialIMAP(ctx, a)
	if err != nil {
		if _, ok := errors.AsType[userError](err); ok {
			return err // already says what to fix; no protocol prefix in front of it
		}
		if ue := connectFailure(a.IMAPHost, a.IMAPPort, s.proxyFor(a) != nil, err); ue != nil {
			return ue
		}
		return fmt.Errorf("imap: %w", err)
	}
	c.Close()

	if a.Auth == AuthOAuth2 && a.Provider == ProviderGmail {
		// Sent through the Gmail API, not SMTP (gmailAPIBase).
		return s.gmailAPICheck(ctx, a)
	}
	if err := s.checkSMTP(ctx, a); err != nil {
		if _, ok := errors.AsType[userError](err); ok {
			return err
		}
		if ue := connectFailure(a.SMTPHost, a.SMTPPort, s.proxyFor(a) != nil, err); ue != nil {
			return ue
		}
		return fmt.Errorf("smtp: %w", err)
	}
	return nil
}

// pickFeishuServers tests a Feishu account against every set in feishuServers at
// once and keeps the first that passes, so a hub in mainland China ends up on
// feishu.cn and one overseas on larkoffice.com without asking the user which. The
// slower tests are left to finish on their own; each is bounded by testAccount's
// timeout.
func (s *Service) pickFeishuServers(ctx context.Context, a *MailAccount) error {
	type result struct {
		p   *preset
		err error
	}
	results := make(chan result, len(feishuServers))
	for _, p := range feishuServers {
		c := *a
		c.IMAPHost, c.IMAPPort, c.SMTPHost, c.SMTPPort, c.SMTPSecurity = p.IMAPHost, p.IMAPPort, p.SMTPHost, p.SMTPPort, p.SMTPSecurity
		go func() { results <- result{p, s.testAccount(ctx, &c)} }()
	}
	errs := make([]error, 0, len(feishuServers))
	var answered error
	for range feishuServers {
		r := <-results
		if r.err == nil {
			a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort, a.SMTPSecurity = r.p.IMAPHost, r.p.IMAPPort, r.p.SMTPHost, r.p.SMTPPort, r.p.SMTPSecurity
			return nil
		}
		err := fmt.Errorf("%s: %w", r.p.IMAPHost, r.err)
		errs = append(errs, err)
		if answered == nil && !unreachable(r.err) {
			answered = err
		}
	}
	// A wrong password is reported by whichever set was reachable; the other set
	// timing out or refusing the connection must not hide it.
	if answered != nil {
		return answered
	}
	return errors.Join(errs...)
}

// unreachable reports whether err means the server could not be reached at all
// (timeout, refused, DNS), as opposed to an answer such as a rejected login.
func unreachable(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne)
}
