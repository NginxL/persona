package mail

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// dialIMAP opens and authenticates a fresh IMAP connection.
func (s *Service) dialIMAP(ctx context.Context, a *MailAccount) (*imapclient.Client, error) {
	if s.imapDial != nil {
		return s.imapDial(a)
	}
	token, err := s.oauthToken(ctx, a)
	if err != nil {
		return nil, err
	}
	type result struct {
		c   *imapclient.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := dialAndLogin(a, token, s.proxyFor(a))
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.err
	}
}

func dialAndLogin(a *MailAccount, token string, proxy *url.URL) (*imapclient.Client, error) {
	addr := serverAddr(a.IMAPHost, a.IMAPPort)
	opts := &imapclient.Options{
		TLSConfig:   &tls.Config{ServerName: a.IMAPHost},
		WordDecoder: mailWordDecoder(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	var (
		c    *imapclient.Client
		conn net.Conn
		err  error
	)
	switch imapSecurity(a) {
	case "tls":
		if conn, err = dialTLS(ctx, proxy, addr, a.IMAPHost); err == nil {
			c = imapclient.New(conn, opts)
		}
	case "starttls":
		if conn, err = dialTCP(ctx, proxy, addr); err == nil {
			c, err = imapclient.NewStartTLS(conn, opts)
		}
	default:
		if conn, err = dialTCP(ctx, proxy, addr); err == nil {
			c = imapclient.New(conn, opts)
		}
	}
	if err != nil {
		return nil, err
	}
	// NetEase (163/126) rejects logins from clients that do not identify
	// themselves, so send ID before authenticating. Servers without the ID
	// extension answer BAD; that is harmless here.
	if c.Caps().Has(imap.CapID) {
		sendClientID(c)
	}
	if err := loginIMAP(c, a, token); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// sendClientID announces the client to the server (RFC 2971), best effort.
func sendClientID(c *imapclient.Client) {
	data := &imap.IDData{Name: "Persona", Version: "1.0", Vendor: "Persona"}
	cmd := c.ID(data)
	_, _ = cmd.Wait()
}

func imapSecurity(a *MailAccount) string {
	if a.IMAPSecurity != "" {
		return a.IMAPSecurity
	}
	switch a.IMAPPort {
	case 993:
		return "tls"
	case 143:
		return "starttls"
	default:
		return "none"
	}
}

var netDialer = net.Dialer{Timeout: networkTimeout}
