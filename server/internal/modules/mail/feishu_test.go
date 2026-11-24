package mail

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newFeishuService returns a service whose login check answers per IMAP host
// from check, sleeping delay[host] first.
func newFeishuService(t *testing.T, delay map[string]time.Duration, check map[string]error) *Service {
	t.Helper()
	s, err := New(t.TempDir(), newMemRecords())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.verify = func(ctx context.Context, a *MailAccount) error {
		select {
		case <-time.After(delay[a.IMAPHost]):
		case <-ctx.Done():
			return ctx.Err()
		}
		return check[a.IMAPHost]
	}
	return s
}

func addFeishu(s *Service) (*MailAccount, error) {
	return s.AddAccount(AddAccountRequest{Email: "me@example.com", Provider: ProviderFeishu, Password: "pw"})
}

func TestFeishuKeepsTheServersThatSignIn(t *testing.T) {
	refused := errors.New("dial tcp: connection refused")
	for _, tc := range []struct {
		name     string
		delay    map[string]time.Duration
		check    map[string]error
		wantIMAP string
		wantSMTP string
	}{
		{"only feishu.cn answers", nil, map[string]error{"imap.larkoffice.com": refused},
			"imap.feishu.cn", "smtp.feishu.cn"},
		{"only larkoffice.com answers", nil, map[string]error{"imap.feishu.cn": refused},
			"imap.larkoffice.com", "smtp.larkoffice.com"},
		{"feishu.cn is faster", map[string]time.Duration{"imap.larkoffice.com": 200 * time.Millisecond}, nil,
			"imap.feishu.cn", "smtp.feishu.cn"},
		{"larkoffice.com is faster", map[string]time.Duration{"imap.feishu.cn": 200 * time.Millisecond}, nil,
			"imap.larkoffice.com", "smtp.larkoffice.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFeishuService(t, tc.delay, tc.check)
			if _, err := addFeishu(s); err != nil {
				t.Fatalf("AddAccount: %v", err)
			}
			accts, err := s.ListAccounts()
			if err != nil || len(accts) != 1 {
				t.Fatalf("ListAccounts = %v, %v", accts, err)
			}
			a, err := s.account(accts[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if a.IMAPHost != tc.wantIMAP || a.SMTPHost != tc.wantSMTP || a.IMAPPort != 993 || a.SMTPPort != 465 ||
				a.SMTPSecurity != "tls" || a.Provider != ProviderFeishu {
				t.Fatalf("saved account = %+v, want %s / %s", a, tc.wantIMAP, tc.wantSMTP)
			}
		})
	}
}

func TestFeishuReportsTheReachableServersError(t *testing.T) {
	s := newFeishuService(t, nil, map[string]error{
		"imap.larkoffice.com": context.DeadlineExceeded,
		"imap.feishu.cn":      errors.New("invalid credentials"),
	})
	_, err := addFeishu(s)
	if err == nil {
		t.Fatal("AddAccount succeeded with both servers failing")
	}
	if !strings.Contains(err.Error(), "imap.feishu.cn: invalid credentials") || strings.Contains(err.Error(), "larkoffice") {
		t.Fatalf("err = %q, want only the feishu.cn login error", err)
	}
	if got := statusFor(err); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a wrong password", got)
	}
	if accts, _ := s.ListAccounts(); len(accts) != 0 {
		t.Fatalf("a failed account was saved: %+v", accts)
	}
}

func TestFeishuPrefersALoginErrorOverARefusedConnection(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	// The refused connection comes back first, so picking the first error would pick it.
	s := newFeishuService(t, map[string]time.Duration{"imap.larkoffice.com": 50 * time.Millisecond}, map[string]error{
		"imap.larkoffice.com": errors.New("invalid credentials"),
		"imap.feishu.cn":      refused,
	})
	_, err := addFeishu(s)
	if err == nil || !strings.Contains(err.Error(), "imap.larkoffice.com: invalid credentials") || strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want only the larkoffice.com login error", err)
	}
}

func TestFeishuReportsBothServersWhenNeitherAnswers(t *testing.T) {
	s := newFeishuService(t, nil, map[string]error{
		"imap.larkoffice.com": context.DeadlineExceeded,
		"imap.feishu.cn":      context.DeadlineExceeded,
	})
	_, err := addFeishu(s)
	if err == nil || !strings.Contains(err.Error(), "imap.larkoffice.com") || !strings.Contains(err.Error(), "imap.feishu.cn") {
		t.Fatalf("err = %v, want both servers named", err)
	}
	if got := statusFor(err); got != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", got)
	}
}
