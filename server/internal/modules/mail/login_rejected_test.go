package mail

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-smtp"

	"github.com/nwverse/persona/server/internal/i18n"
)

// A wrong password or authorization code is the user's to fix: it says so in words and is a 400, not the server's raw
// "imap: imap: login: imap: NO [AUTHENTICATIONFAILED] …" as a 502 (#3772).
func TestAddAccountWrongPasswordSaysWhatToFix(t *testing.T) {
	srv := newTestIMAP(t)
	s, err := New(t.TempDir(), newMemRecords())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.AddAccount(AddAccountRequest{Email: "me@example.com", Password: "wrong", IMAPHost: srv.host, IMAPPort: srv.port,
		IMAPSecurity: "none", SMTPHost: srv.host, SMTPPort: 2525, SMTPSecurity: "none"})
	if err == nil {
		t.Fatal("AddAccount succeeded with a wrong password")
	}
	if !strings.Contains(err.Error(), "授权码") || strings.HasPrefix(err.Error(), "imap") {
		t.Fatalf("err = %q, want the plain explanation first", err)
	}
	if got := statusFor(err); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
	if accts, _ := s.ListAccounts(); len(accts) != 0 {
		t.Fatalf("a failed account was saved: %+v", accts)
	}
}

// A rejected QQ or NetEase login also says where that provider makes the authorization code (#5123), first: an
// account's stored reason is cut at maxLoginErrLen, and a long server answer must not push the steps out.
func TestLoginRejectedTellsTheProvidersSteps(t *testing.T) {
	no := &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "LOGIN failed"}
	if msg := loginRejected(&MailAccount{Email: "me@qq.com"}, no).Error(); !strings.Contains(msg, "mail.qq.com") ||
		!strings.Contains(msg, "生成授权码") || !strings.Contains(msg, "LOGIN failed") {
		t.Errorf("qq.com: %q", msg)
	}
	long := &imap.Error{Type: imap.StatusResponseTypeNo, Text: strings.Repeat("Login fail. ", 40)}
	if msg := loginRejected(&MailAccount{Email: "me@qq.com"}, long).Error(); !strings.HasPrefix(msg, PasswordGuide(i18n.Default, "me@qq.com")) {
		t.Errorf("qq.com, long answer: %q", msg)
	}
	if msg := loginRejected(&MailAccount{Email: "me@163.com"}, no).Error(); !strings.Contains(msg, "mail.163.com") {
		t.Errorf("163.com: %q", msg)
	}
	if msg := loginRejected(&MailAccount{Email: "me@example.com"}, no).Error(); strings.Contains(msg, "\n") {
		t.Errorf("example.com: %q", msg)
	}
	refused := &smtp.SMTPError{Code: 535, Message: "Login fail"}
	if msg := smtpLoginErr(&MailAccount{Email: "me@126.com", Auth: AuthPassword}, refused).Error(); !strings.Contains(msg, "mail.126.com") ||
		!strings.Contains(msg, "SMTP") {
		t.Errorf("126.com smtp: %q", msg)
	}
}

// IMAP accepted the login but SMTP refused it (SMTP not switched on, or a different code): said apart from a wrong
// IMAP password, also a 400.
func TestAddAccountSMTPLoginRejectedSaysWhatToFix(t *testing.T) {
	srv := newTestIMAP(t)
	fs := newFakeSMTPServer(t)
	fs.pass = "other"
	host, portStr, _ := net.SplitHostPort(fs.addr)
	port, _ := strconv.Atoi(portStr)
	s, err := New(t.TempDir(), newMemRecords())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a := srv.testAccount()
	a.SMTPHost, a.SMTPPort = host, port
	err = s.testAccount(context.Background(), a)
	if err == nil {
		t.Fatal("testAccount passed with SMTP refusing the login")
	}
	if !strings.Contains(err.Error(), "SMTP") || !strings.Contains(err.Error(), "授权码") || strings.HasPrefix(err.Error(), "smtp") {
		t.Fatalf("err = %q, want the plain SMTP explanation", err)
	}
	if got := statusFor(err); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

// A rejected login is a protocol answer: reading the mailbox tries it once, not again as if the connection broke
// (servers such as QQ and 163 lock an account after repeated failures).
func TestRejectedLoginIsNotRetried(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	s.mu.Lock()
	s.accts["acc_test"].Password = "wrong"
	s.mu.Unlock()
	dials := 0
	s.imapDial = func(a *MailAccount) (*imapclient.Client, error) {
		dials++
		return dialAndLogin(a, "", nil)
	}
	_, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test"})
	if err == nil || !strings.Contains(err.Error(), "授权码") {
		t.Fatalf("err = %v, want the plain explanation", err)
	}
	if dials != 1 {
		t.Fatalf("logged in %d times, want 1", dials)
	}
}

// A NO that is not about the credentials (here UNAVAILABLE: the server is busy) is not called a wrong password.
func TestLoginUnavailableIsNotAWrongPassword(t *testing.T) {
	err := loginRejected(&MailAccount{}, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeUnavailable, Text: "try later"})
	if strings.Contains(err.Error(), "授权码") || statusFor(err) == http.StatusBadRequest {
		t.Fatalf("err = %q (status %d), want the server error as before", err, statusFor(err))
	}
}

// Feishu tries two server sets at once: a wrong authorization code from the reachable one is still told as the user's to fix.
func TestFeishuWrongPasswordIsAUserError(t *testing.T) {
	rejected := loginRejected(&MailAccount{}, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed,
		Text: "wrong authorization code"})
	s := newFeishuService(t, nil, map[string]error{"imap.larkoffice.com": context.DeadlineExceeded, "imap.feishu.cn": rejected})
	_, err := addFeishu(s)
	if err == nil || !strings.Contains(err.Error(), "授权码") || statusFor(err) != http.StatusBadRequest {
		t.Fatalf("err = %v (status %d), want the plain explanation as a 400", err, statusFor(err))
	}
}

// Only password accounts get the authorization-code wording; an OAuth account's refused SMTP login stays as it was.
func TestSMTPLoginErrLeavesOAuthAlone(t *testing.T) {
	refused := &smtp.SMTPError{Code: 535, Message: "Authentication unsuccessful"}
	if err := smtpLoginErr(&MailAccount{Auth: AuthOAuth2}, refused); strings.Contains(err.Error(), "授权码") {
		t.Fatalf("err = %q, want the server error as before", err)
	}
}
