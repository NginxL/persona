package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// gmailSearch runs "UID SEARCH X-GM-RAW <query>" on a short-lived raw IMAP
// connection, because go-imap's typed client cannot express vendor extensions.
// Gmail's query language ("from:foo has:attachment", ...) is far more useful
// than SEARCH TEXT, so it is worth the extra connection.
func (s *Service) gmailSearch(ctx context.Context, a *MailAccount, mailbox, query string, beforeUID uint32) ([]imap.UID, error) {
	ctx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()

	raw, err := s.rawDial(ctx, a)
	if err != nil {
		return nil, err
	}
	defer raw.close()

	token, err := s.oauthToken(ctx, a)
	if err != nil {
		return nil, err
	}
	if err := raw.login(ctx, a, token); err != nil {
		return nil, err
	}
	if _, err := raw.command(ctx, "SELECT "+quoteIMAP(mailbox)); err != nil {
		return nil, err
	}
	criteria := "X-GM-RAW " + quoteIMAP(query)
	if beforeUID > 1 {
		criteria += fmt.Sprintf(" UID 1:%d", beforeUID-1)
	}
	lines, err := raw.command(ctx, "UID SEARCH "+criteria)
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	for _, line := range lines {
		if !strings.HasPrefix(strings.ToUpper(line), "* SEARCH") {
			continue
		}
		for _, tok := range strings.Fields(line)[2:] {
			n, err := strconv.ParseUint(tok, 10, 32)
			if err != nil {
				continue
			}
			uids = append(uids, imap.UID(n))
		}
	}
	return uids, nil
}

// rawConn is a minimal line-oriented IMAP connection.
type rawConn struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

func (s *Service) rawDial(ctx context.Context, a *MailAccount) (*rawConn, error) {
	addr := serverAddr(a.IMAPHost, a.IMAPPort)
	proxy := s.proxyFor(a)
	var (
		c   net.Conn
		err error
	)
	switch imapSecurity(a) {
	case "tls":
		c, err = dialTLS(ctx, proxy, addr, a.IMAPHost)
	case "starttls":
		c, err = dialTCP(ctx, proxy, addr)
		if err == nil {
			rc := &rawConn{conn: c, r: bufio.NewReader(c)}
			if _, err = rc.command(ctx, "STARTTLS"); err != nil {
				c.Close()
				return nil, err
			}
			tc := tls.Client(c, &tls.Config{ServerName: a.IMAPHost})
			if err = tc.HandshakeContext(ctx); err != nil {
				c.Close()
				return nil, err
			}
			return &rawConn{conn: tc, r: bufio.NewReader(tc)}, nil
		}
	default:
		c, err = dialTCP(ctx, proxy, addr)
	}
	if err != nil {
		return nil, err
	}
	return &rawConn{conn: c, r: bufio.NewReader(c)}, nil
}

func (rc *rawConn) close() {
	rc.conn.Close()
}

// readLine reads one CRLF-terminated line.
func (rc *rawConn) readLine(ctx context.Context) (string, error) {
	if d, ok := ctx.Deadline(); ok {
		rc.conn.SetReadDeadline(d)
	}
	line, err := rc.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// command sends one command and returns its untagged response lines, or an
// error built from the tagged NO/BAD response.
func (rc *rawConn) command(ctx context.Context, cmd string) ([]string, error) {
	rc.tag++
	tag := fmt.Sprintf("J%d", rc.tag)
	if d, ok := ctx.Deadline(); ok {
		rc.conn.SetWriteDeadline(d)
	}
	if _, err := io.WriteString(rc.conn, tag+" "+cmd+"\r\n"); err != nil {
		return nil, err
	}
	var lines []string
	for {
		line, err := rc.readLine(ctx)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, tag+" ") {
			rest := strings.TrimPrefix(line, tag+" ")
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return lines, nil
			}
			return lines, errors.New("imap: " + rest)
		}
		if strings.HasPrefix(line, "+") {
			// Unexpected continuation: abort it so the session can continue.
			if _, err := io.WriteString(rc.conn, "\r\n"); err != nil {
				return nil, err
			}
			continue
		}
		lines = append(lines, line)
	}
}

func (rc *rawConn) login(ctx context.Context, a *MailAccount, token string) error {
	if a.Auth == AuthOAuth2 {
		_, err := rc.command(ctx, "AUTHENTICATE XOAUTH2 "+base64.StdEncoding.EncodeToString(xoauth2Payload(a.Username, token)))
		return err
	}
	_, err := rc.command(ctx, "LOGIN "+quoteIMAP(a.Username)+" "+quoteIMAP(a.Password))
	return err
}

// quoteIMAP renders s as an IMAP quoted string.
func quoteIMAP(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return `"` + s + `"`
}
