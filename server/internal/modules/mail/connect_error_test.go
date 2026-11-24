package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// A server that cannot be reached is told apart from a rejected login: it names the hub's machine, says the password
// is not the problem and what to do next, and is a 400 rather than the raw "HTTP 504: imap: dial tcp …" (#9389).
func TestConnectFailureExplainsUnreachableServers(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}
	cases := []struct {
		name     string
		err      error
		viaProxy bool
		want     []string
	}{
		{"timeout", fmt.Errorf("imap: %w", timeout), false, []string{"hub 的那台机器连不上邮件服务器 imap.gmail.com:993", "不是密码", "google.proxy"}},
		{"timeout via proxy", timeout, true, []string{"已经走了代理也不通"}},
		{"deadline", context.DeadlineExceeded, false, []string{"连不上邮件服务器"}},
		{"dns", &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "imap.gmail.com"}}, false, []string{"解析不了邮件服务器地址 imap.gmail.com"}},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, false, []string{"拒绝了"}},
		{"tls", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, false, []string{"TLS 握手失败"}},
		{"proxy refused", &proxyError{proxy: "proxy.example:8118", addr: "imap.gmail.com:993", err: errors.New("504 Gateway Timeout")}, true,
			[]string{"通过代理（proxy.example:8118）", "504 Gateway Timeout", "google.proxy"}},
	}
	for _, c := range cases {
		err := connectFailure("imap.gmail.com", 993, c.viaProxy, c.err)
		if err == nil {
			t.Errorf("%s: not explained", c.name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", c.name, err, w)
			}
		}
		if statusFor(err) != http.StatusBadRequest || strings.Contains(err.Error(), "dial tcp") {
			t.Errorf("%s: status %d, text %q", c.name, statusFor(err), err)
		}
	}
	// A server's answer is not a connection failure: loginRejected owns it.
	no := &imap.Error{Type: imap.StatusResponseTypeNo, Text: "denied"}
	if connectFailure("imap.gmail.com", 993, false, fmt.Errorf("imap: login: %w", no)) != nil {
		t.Error("a rejected login was reported as unreachable")
	}
}

// An address whose provider needs an app password says so, with the link, when the login is rejected.
func TestLoginRejectedGmailAndICloudSayAppPassword(t *testing.T) {
	no := &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "Invalid credentials"}
	for email, want := range map[string]string{"me@gmail.com": "myaccount.google.com/apppasswords", "me@icloud.com": "account.apple.com"} {
		if msg := loginRejected(&MailAccount{Email: email}, no).Error(); !strings.Contains(msg, want) || !strings.Contains(msg, "专用密码") {
			t.Errorf("%s: %q", email, msg)
		}
	}
	// Google Workspace: own domain, but the Gmail provider.
	if msg := loginRejected(&MailAccount{Email: "me@corp.example", Provider: ProviderGmail}, no).Error(); !strings.Contains(msg, "apppasswords") {
		t.Errorf("workspace: %q", msg)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
