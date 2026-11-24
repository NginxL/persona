package mail

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func addRequest(email string) AddAccountRequest {
	return AddAccountRequest{Email: email, Password: "pw", IMAPHost: "imap.example.com", IMAPPort: 993,
		SMTPHost: "smtp.example.com", SMTPPort: 465}
}

// A second account for an address is refused at once, before the connection test that takes seconds, with 409 and a
// message that says what to do instead (#1766).
func TestAddAccountRefusesADuplicateBeforeTheConnectionTest(t *testing.T) {
	s, _ := newOAuthService(t)
	tests := 0
	s.verify = func(context.Context, *MailAccount) error { tests++; return nil }
	if _, err := s.AddAccount(addRequest("me@example.com")); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := s.AddAccount(addRequest("Me@Example.com"))
	if !errors.Is(err, ErrAccountExists) {
		t.Fatalf("second add: %v", err)
	}
	if tests != 1 {
		t.Fatalf("the duplicate went through the connection test (%d tests)", tests)
	}
	if got := statusFor(err); got != http.StatusConflict {
		t.Fatalf("status %d, want 409", got)
	}
	if !strings.Contains(err.Error(), "先删掉原来的账号") {
		t.Fatalf("message %q does not say what to do", err)
	}
	if accounts, _ := s.ListAccounts(); len(accounts) != 1 {
		t.Fatalf("%d accounts, want 1", len(accounts))
	}
}

// One added by another request while this one was testing is still refused after the test.
func TestAddAccountRefusesADuplicateAddedDuringTheTest(t *testing.T) {
	s, _ := newOAuthService(t)
	s.verify = func(_ context.Context, a *MailAccount) error {
		s.mu.Lock()
		s.accts["acc_other"] = &MailAccount{ID: "acc_other", Email: a.Email, Provider: ProviderIMAP}
		s.mu.Unlock()
		return nil
	}
	if _, err := s.AddAccount(addRequest("me@example.com")); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("err = %v", err)
	}
}
