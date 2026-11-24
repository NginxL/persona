package mail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestEncryptedStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	recs := newMemRecords()
	s, err := New(dir, recs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	// The key file must be created with 0600.
	info, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("stat secret.key: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("secret.key mode = %o, want 600", got)
	}
	if info.Size() != 32 {
		t.Fatalf("secret.key size = %d, want 32", info.Size())
	}

	a := &MailAccount{
		ID:       "acc_1",
		Name:     "Gmail",
		Email:    "me@gmail.com",
		Provider: ProviderGmail,
		Auth:     AuthPassword,
		Username: "me@gmail.com",
		Password: "hunter2-中文",
		IMAPHost: "imap.gmail.com", IMAPPort: 993,
		SMTPHost: "smtp.gmail.com", SMTPPort: 465, SMTPSecurity: "tls",
		Status: "ok",
	}
	if err := s.store.save([]*MailAccount{a}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// What reaches the database must not contain the secrets in clear text.
	raw := recs.dump()
	for _, secret := range []string{"hunter2"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("stored rows leak %q: %s", secret, raw)
		}
	}

	// Reload through a fresh Service: secrets must decrypt back.
	s2, err := New(dir, recs)
	if err != nil {
		t.Fatalf("New again: %v", err)
	}
	defer s2.Close()
	got, err := s2.account("acc_1")
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if got.Password != a.Password {
		t.Fatalf("password = %q, want %q", got.Password, a.Password)
	}

	// Public views never expose secrets.
	pub := publicAccount(got)
	if pub.Password != "" {
		t.Fatalf("public() leaked secrets: %+v", pub)
	}
	b, err := json.Marshal(pub)
	if err != nil {
		t.Fatalf("marshal public: %v", err)
	}
	if strings.Contains(string(b), `"password":`) || strings.Contains(string(b), `"oauth":`) {
		t.Fatalf("public JSON contains secret fields: %s", b)
	}
}

func TestStoreRejectsBadKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir, filepath.Join(dir, "secret.key"), newMemRecords()); err == nil {
		t.Fatal("openStore accepted a 5-byte key")
	}
}

func TestPresets(t *testing.T) {
	cases := []struct {
		email    string
		provider string
		imapHost string
		imapPort int
		smtpHost string
		smtpPort int
		security string
	}{
		{"me@gmail.com", ProviderGmail, "imap.gmail.com", 993, "smtp.gmail.com", 465, "tls"},
		{"ME@GoogleMail.com", ProviderGmail, "imap.gmail.com", 993, "smtp.gmail.com", 465, "tls"},
		{"a@qq.com", ProviderIMAP, "imap.qq.com", 993, "smtp.qq.com", 465, "tls"},
		{"a@163.com", ProviderIMAP, "imap.163.com", 993, "smtp.163.com", 465, "tls"},
		{"a@126.com", ProviderIMAP, "imap.126.com", 993, "smtp.126.com", 465, "tls"},
		{"a@foxmail.com", ProviderIMAP, "imap.qq.com", 993, "smtp.qq.com", 465, "tls"},
		{"a@vip.qq.com", ProviderIMAP, "imap.qq.com", 993, "smtp.qq.com", 465, "tls"},
		{"a@yeah.net", ProviderIMAP, "imap.yeah.net", 993, "smtp.yeah.net", 465, "tls"},
		{"a@vip.163.com", ProviderIMAP, "imap.vip.163.com", 993, "smtp.vip.163.com", 465, "tls"},
		{"a@vip.126.com", ProviderIMAP, "imap.vip.126.com", 993, "smtp.vip.126.com", 465, "tls"},
		{"a@188.com", ProviderIMAP, "imap.188.com", 993, "smtp.188.com", 465, "tls"},
		{"a@outlook.com", ProviderIMAP, "outlook.office365.com", 993, "smtp-mail.outlook.com", 587, "starttls"},
		{"a@hotmail.com", ProviderIMAP, "outlook.office365.com", 993, "smtp-mail.outlook.com", 587, "starttls"},
		{"a@icloud.com", ProviderIMAP, "imap.mail.me.com", 993, "smtp.mail.me.com", 587, "starttls"},
		{"a@yahoo.com", ProviderIMAP, "imap.mail.yahoo.com", 993, "smtp.mail.yahoo.com", 465, "tls"},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			a, err := buildAccount(AddAccountRequest{Email: tc.email, Password: "p"})
			if err != nil {
				t.Fatalf("buildAccount: %v", err)
			}
			if a.Provider != tc.provider {
				t.Errorf("provider = %q, want %q", a.Provider, tc.provider)
			}
			if a.IMAPHost != tc.imapHost || a.IMAPPort != tc.imapPort {
				t.Errorf("imap = %s:%d, want %s:%d", a.IMAPHost, a.IMAPPort, tc.imapHost, tc.imapPort)
			}
			if a.SMTPHost != tc.smtpHost || a.SMTPPort != tc.smtpPort {
				t.Errorf("smtp = %s:%d, want %s:%d", a.SMTPHost, a.SMTPPort, tc.smtpHost, tc.smtpPort)
			}
			if a.SMTPSecurity != tc.security {
				t.Errorf("security = %q, want %q", a.SMTPSecurity, tc.security)
			}
		})
	}
}

// QQ and NetEase sign mail clients in with an authorization code: the guide says where each one's web mail makes
// it, in the asked language (#5123).
func TestPasswordGuide(t *testing.T) {
	for email, want := range map[string]string{
		"a@qq.com": "mail.qq.com", "a@Foxmail.com": "生成授权码", "a@163.com": "mail.163.com", "a@126.com": "mail.126.com",
		"a@yeah.net": "mail.yeah.net", "a@vip.163.com": "vip.163.com", "a@188.com": "www.188.com",
	} {
		if got := PasswordGuide(i18n.ZhHans, email); !strings.Contains(got, want) || !strings.Contains(got, "授权码") {
			t.Errorf("PasswordGuide(%q) = %q, want it to mention %q", email, got, want)
		}
	}
	for email, want := range map[string]string{"a@gmail.com": "myaccount.google.com/apppasswords", "a@googlemail.com": "apppasswords",
		"a@icloud.com": "account.apple.com"} {
		if got := PasswordGuide(i18n.ZhHans, email); !strings.Contains(got, want) || !strings.Contains(got, "专用密码") {
			t.Errorf("PasswordGuide(%q) = %q, want it to mention %q", email, got, want)
		}
	}
	for _, email := range []string{"a@outlook.com", "a@example.com", "nope"} {
		if got := PasswordGuide(i18n.En, email); got != "" {
			t.Errorf("PasswordGuide(%q) = %q, want none", email, got)
		}
	}
	if got := PasswordGuide(i18n.En, "a@163.com"); !strings.Contains(got, "mail.163.com") || !strings.Contains(got, "authorization code") {
		t.Errorf("PasswordGuide(en, 163.com) = %q", got)
	}
}

func TestPresetExplicitFieldsWin(t *testing.T) {
	a, err := buildAccount(AddAccountRequest{
		Email:    "me@gmail.com",
		Password: "p",
		IMAPHost: "imap.internal.example",
		IMAPPort: 1993,
		SMTPHost: "smtp.internal.example",
		SMTPPort: 1587,
	})
	if err != nil {
		t.Fatalf("buildAccount: %v", err)
	}
	if a.IMAPHost != "imap.internal.example" || a.IMAPPort != 1993 {
		t.Errorf("explicit imap overridden: %s:%d", a.IMAPHost, a.IMAPPort)
	}
	if a.SMTPHost != "smtp.internal.example" || a.SMTPPort != 1587 {
		t.Errorf("explicit smtp overridden: %s:%d", a.SMTPHost, a.SMTPPort)
	}
}

func TestBuildAccountValidation(t *testing.T) {
	cases := []struct {
		name string
		req  AddAccountRequest
		want string
	}{
		{"missing email", AddAccountRequest{Password: "p"}, "email is required"},
		{"unknown domain", AddAccountRequest{Email: "me@nowhere.example", Password: "p"}, "unknown mail provider"},
		{"password auth without password", AddAccountRequest{Email: "me@gmail.com", Auth: AuthPassword}, "password is required"},
		{"oauth2 outside Gmail", AddAccountRequest{Email: "me@qq.com", Auth: AuthOAuth2}, "invalid auth"},
		{"bad auth", AddAccountRequest{Email: "me@gmail.com", Auth: "carrier-pigeon", Password: "p"}, "invalid auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAccount(tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRoleOf(t *testing.T) {
	cases := []struct {
		name  string
		attrs []string
		want  string
	}{
		{"INBOX", nil, RoleInbox},
		{"inbox", nil, RoleInbox},
		{"[Gmail]/Sent Mail", []string{`\Sent`}, RoleSent},
		{"Drafts", []string{`\Drafts`}, RoleDrafts},
		{"Trash", []string{`\Trash`}, RoleTrash},
		{"Archive", []string{`\Archive`}, RoleArchive},
		{"Junk", []string{`\Junk`}, RoleJunk},
		{"[Gmail]/All Mail", []string{`\All`}, ""},
		{"Sent", nil, RoleSent},
		{"Deleted Items", nil, RoleTrash},
		{"Custom", nil, ""},
	}
	for _, tc := range cases {
		var list []imap.MailboxAttr
		for _, a := range tc.attrs {
			list = append(list, imap.MailboxAttr(a))
		}
		if got := roleOf(tc.name, list); got != tc.want {
			t.Errorf("roleOf(%q, %v) = %q, want %q", tc.name, tc.attrs, got, tc.want)
		}
	}
}

func TestIsConnError(t *testing.T) {
	if isConnError(nil) {
		t.Fatal("nil is not a connection error")
	}
	if !isConnError(errors.New("broken pipe")) {
		t.Fatal("a bare error should trigger a reconnect")
	}
	if isConnError(context.DeadlineExceeded) {
		t.Fatal("a deadline is not a broken connection")
	}
}

func TestFeishuProviderUsesFixedServers(t *testing.T) {
	a, err := buildAccount(AddAccountRequest{Email: "me@example.com", Provider: ProviderFeishu, Password: "pw",
		IMAPHost: "ignored.example.com", IMAPPort: 143})
	if err != nil {
		t.Fatal(err)
	}
	if a.IMAPHost != "imap.larkoffice.com" || a.IMAPPort != 993 || a.SMTPHost != "smtp.larkoffice.com" ||
		a.SMTPPort != 465 || a.SMTPSecurity != "tls" || a.Provider != ProviderFeishu || a.Username != "me@example.com" {
		t.Fatalf("feishu account = %+v", a)
	}
}

// The key in a directory not made yet (the built-in manager's own) is made with it.
func TestStoreKeyInANewDirectory(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "managers", "assistant", "secret.key")
	if _, err := openStore(dir, keyPath, newMemRecords()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(keyPath); err != nil || fi.Size() != 32 {
		t.Fatalf("key %v %v", fi, err)
	}
}
