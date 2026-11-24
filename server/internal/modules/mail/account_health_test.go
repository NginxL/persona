package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// accountStatus is the status and error the settings get for acc_test from GET /api/v1/mail/accounts.
func accountStatus(t *testing.T, s *Service) (status, reason string) {
	t.Helper()
	code, body := doJSON(t, s, http.MethodGet, "/api/v1/mail/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("GET accounts = %d", code)
	}
	var accts []MailAccount
	if err := json.Unmarshal(body["accounts"], &accts); err != nil {
		t.Fatal(err)
	}
	for _, a := range accts {
		if a.ID == "acc_test" {
			return a.Status, a.Error
		}
	}
	t.Fatalf("acc_test not listed: %s", body["accounts"])
	return "", ""
}

// newHealthService is a Service with acc_test (status ok, as loaded from disk) on the in-process test IMAP server.
func newHealthService(t *testing.T) *Service {
	t.Helper()
	s := newServiceWithAccount(t, newTestIMAP(t))
	s.mu.Lock()
	s.accts["acc_test"].Status = "ok"
	s.mu.Unlock()
	return s
}

func setPassword(s *Service, pw string) {
	s.mu.Lock()
	s.accts["acc_test"].Password = pw
	s.mu.Unlock()
	s.dropConn("acc_test")
}

// A changed password: the read fails, the account says error with the reason (not the password), and once the
// login works again the next read puts it back to ok (#4722).
func TestRefusedLoginMarksAccountErrorUntilNextGoodRead(t *testing.T) {
	s := newHealthService(t)
	ctx := context.Background()
	setPassword(s, "changed-elsewhere")

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err == nil {
		t.Fatal("ListMessages succeeded with a wrong password")
	}
	status, reason := accountStatus(t, s)
	if status != "error" || !strings.Contains(reason, "授权码") {
		t.Fatalf("status = %q, error = %q; want error with the reason", status, reason)
	}
	if strings.Contains(reason, "changed-elsewhere") {
		t.Fatalf("error %q shows the password", reason)
	}
	// Nothing written to disk: a restart starts from ok.
	if loaded, _ := s.store.load(); len(loaded) != 1 || loaded[0].Status != "ok" || loaded[0].Error != "" {
		t.Fatalf("stored account = %+v, want status ok", loaded)
	}

	setPassword(s, "secret")
	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("ListMessages after the fix: %v", err)
	}
	if status, reason := accountStatus(t, s); status != "ok" || reason != "" {
		t.Fatalf("after a good read status = %q, error = %q; want ok", status, reason)
	}
}

// Timeouts, refused connections and DNS failures pass: they leave the status as it was, ok or error.
func TestTransientConnectErrorLeavesStatus(t *testing.T) {
	transient := []error{
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		&net.DNSError{Err: "no such host", Name: "imap.example.com", IsTimeout: true},
		context.DeadlineExceeded,
		fmt.Errorf("oauth: token endpoint returned HTTP 503: backend error"),
		&imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeUnavailable, Text: "try later"},
	}
	for _, start := range []string{"ok", "error"} {
		for _, terr := range transient {
			t.Run(start+"/"+terr.Error(), func(t *testing.T) {
				s := newHealthService(t)
				if start == "error" {
					s.noteLogin("acc_test", errGoogleRevoked)
				}
				s.imapDial = func(*MailAccount) (*imapclient.Client, error) { return nil, terr }
				if _, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test"}); err == nil {
					t.Fatal("ListMessages succeeded with the dial failing")
				}
				if status, _ := accountStatus(t, s); status != start {
					t.Fatalf("status = %q, want it left at %q", status, start)
				}
			})
		}
	}
}

// A protocol answer to a command after a good login (a folder that does not exist) is not the account being broken.
func TestCommandNoAfterLoginLeavesStatusOK(t *testing.T) {
	s := newHealthService(t)
	if _, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Mailbox: "No Such Folder"}); err == nil {
		t.Fatal("ListMessages of a missing folder succeeded")
	}
	if status, reason := accountStatus(t, s); status != "ok" || reason != "" {
		t.Fatalf("status = %q, error = %q; want ok", status, reason)
	}
}

// Revoked OAuth: Google refusing the refresh token, or the server refusing the XOAUTH2 token, marks the account.
func TestRevokedOAuthMarksAccountError(t *testing.T) {
	for name, refused := range map[string]error{
		"google invalid_grant": errGoogleRevoked,
		"xoauth2 NO":           fmt.Errorf("imap: XOAUTH2: %w", &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "Invalid credentials"}),
		"outlook refresh":      microsoftError("invalid_grant", "AADSTS70008: expired", "", true),
	} {
		t.Run(name, func(t *testing.T) {
			s := newHealthService(t)
			s.imapDial = func(*MailAccount) (*imapclient.Client, error) { return nil, refused }
			if _, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test"}); err == nil {
				t.Fatal("ListMessages succeeded with the login refused")
			}
			if status, reason := accountStatus(t, s); status != "error" || reason == "" {
				t.Fatalf("status = %q, error = %q; want error with a reason", status, reason)
			}
		})
	}
}

// Signing in again (an upsert with new credentials) clears the mark at once, without waiting for the next round.
func TestUpsertClearsLoginError(t *testing.T) {
	s := newHealthService(t)
	s.noteLogin("acc_test", errGoogleRevoked)
	a, _ := s.account("acc_test")
	if _, err := s.upsertAccount(a); err != nil {
		t.Fatal(err)
	}
	if status, _ := accountStatus(t, s); status != "ok" {
		t.Fatalf("status = %q after signing in again, want ok", status)
	}
}

func TestLoginErrorReasonIsBounded(t *testing.T) {
	s := newHealthService(t)
	s.noteLogin("acc_test", userErr(strings.Repeat("长", 1000)))
	if _, reason := accountStatus(t, s); len([]rune(reason)) != maxLoginErrLen {
		t.Fatalf("reason is %d runes, want %d", len([]rune(reason)), maxLoginErrLen)
	}
}
