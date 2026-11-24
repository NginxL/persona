package mail

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

// hangUpSMTP is an SMTP server that accepts one message and then drops the connection instead of answering QUIT,
// as some servers do once the message is queued. It returns the address and a channel that gets the message.
func hangUpSMTP(t *testing.T) (string, <-chan string) {
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
				write("250 queued")
				got <- body.String()
			case cmd == "QUIT":
				return // hang up without the 221
			default:
				write("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

// A server that took the message (250 after DATA) and then hung up on QUIT delivered it: Send reports success and
// files the message in Sent, so the user is not told to send it again (#4560).
func TestSendSucceedsWhenQuitFailsAfterDelivery(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	addr, got := hangUpSMTP(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	a := srv.testAccount()
	a.SMTPHost, a.SMTPPort, a.SMTPSecurity = host, port, "none"
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()

	ctx := context.Background()
	err := s.Send(ctx, SendRequest{
		AccountID: "acc_test",
		To:        []Address{{Email: "li@example.com"}},
		Subject:   "Queued",
		Text:      "body",
	})
	if err != nil {
		t.Fatalf("Send = %v, want success: the server had taken the message", err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "Queued") {
			t.Fatalf("delivered message has no subject: %q", body)
		}
	default:
		t.Fatal("the server got no message")
	}
	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Mailbox: "Sent"})
	if err != nil {
		t.Fatalf("list Sent: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("Sent has %d messages, want 1", len(res.Messages))
	}
}
