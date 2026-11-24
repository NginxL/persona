package mail

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

// smtpAccount points the test IMAP account's SMTP side at addr with the given security.
func smtpAccount(t *testing.T, srv *testIMAP, addr, security string) *MailAccount {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	a := srv.testAccount()
	a.SMTPHost, a.SMTPPort, a.SMTPSecurity = host, port, security
	return a
}

// A server that accepted the login and then hung up on QUIT has a working account: adding it must not fail
// because of the goodbye (#4616).
func TestCheckSMTPSucceedsWhenQuitFailsAfterLogin(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	addr, _ := hangUpSMTP(t)
	a := smtpAccount(t, srv, addr, "none")
	if err := s.checkSMTP(context.Background(), a); err != nil {
		t.Fatalf("checkSMTP = %v, want success: the login was accepted", err)
	}
}

// A STARTTLS server that takes the connection but never greets must not hold the account check past the
// caller's deadline; go-smtp alone waits five minutes for the greeting (#4616).
func TestCheckSMTPGivesUpWhenTheGreetingNeverComes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() }) // keep it open and silent
		}
	}()
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	a := smtpAccount(t, srv, ln.Addr().String(), "starttls")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.checkSMTP(ctx, a) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("checkSMTP = %v, want the deadline error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("checkSMTP still waiting for the greeting long after its deadline")
	}
}
