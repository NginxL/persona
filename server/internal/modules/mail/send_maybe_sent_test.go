package mail

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-smtp"
)

// endOfDataSMTP is an SMTP server that takes one message and answers its end with answer, or with answer "" drops the
// connection without answering it, as when the link breaks before the server's reply. got gets the message's body.
func endOfDataSMTP(t *testing.T, answer string) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		write := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		write("220 example.com ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				write("250-example.com")
				write("250 AUTH PLAIN")
			case strings.HasPrefix(cmd, "AUTH"):
				write("235 ok")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				write("250 ok")
			case cmd == "DATA":
				write("354 go ahead")
				var body strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					body.WriteString(l)
				}
				got <- body.String()
				if answer == "" {
					return // the whole message is in; the connection drops before the server answers it
				}
				write(answer)
			case cmd == "QUIT":
				write("221 bye")
				return
			default:
				write("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

// sendVia sends one message through the SMTP server at addr from the test account.
func sendVia(t *testing.T, addr string) error {
	t.Helper()
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	a := srv.testAccount()
	a.SMTPHost, a.SMTPPort, a.SMTPSecurity = host, port, "none"
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()
	return s.Send(context.Background(), SendRequest{AccountID: "acc_test", To: []Address{{Email: "li@example.com"}}, Subject: "Queued", Text: "body"})
}

// A server that got the whole message and dropped the connection without answering it may have taken it: Send says so
// (ErrMaybeSent), so a caller that sends once does not send it again (#9166). A server that answers the end of the
// message with a refusal did not take it: that failure is the server's answer, not ErrMaybeSent.
func TestSendSaysAMessageItsServerDidNotAnswerMayHaveGone(t *testing.T) {
	addr, got := endOfDataSMTP(t, "")
	err := sendVia(t, addr)
	if !errors.Is(err, ErrMaybeSent) {
		t.Fatalf("Send = %v, want ErrMaybeSent: the server had the whole message", err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "Queued") {
			t.Fatalf("the server's message has no subject: %q", body)
		}
	default:
		t.Fatal("the server got no message")
	}

	addr, _ = endOfDataSMTP(t, "554 5.7.1 message rejected")
	err = sendVia(t, addr)
	var refused *smtp.SMTPError
	if errors.Is(err, ErrMaybeSent) || !errors.As(err, &refused) || refused.Code != 554 {
		t.Fatalf("Send = %v, want the server's 554 and not ErrMaybeSent", err)
	}
}
