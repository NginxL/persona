package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newMovingService is a Service on its own data directory (so its own secret.key) and records, as each manager has.
func newMovingService(t *testing.T) (*Service, *memRecords, string) {
	t.Helper()
	dir, recs := t.TempDir(), newMemRecords()
	s, err := New(dir, recs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.verify = func(context.Context, *MailAccount) error { panic("an import must not test the connections") }
	return s, recs, dir
}

// An account exported from one manager and imported into another keeps its ID, servers and credentials, sealed again
// with the new one's key (the new one, restarted, reads them back); one it has already is refused.
func TestExportImportAccountMovesCredentials(t *testing.T) {
	from, fromRecs, _ := newMovingService(t)
	pw := &MailAccount{ID: "acc_pw", Name: "Work", Email: "me@example.com", Provider: ProviderIMAP, Auth: AuthPassword,
		Username: "me", IMAPHost: "imap.example.com", IMAPPort: 993, SMTPHost: "smtp.example.com", SMTPPort: 465,
		SMTPSecurity: "tls", Status: "ok", Password: "example-app-password"}
	oa := &MailAccount{ID: "acc_oa", Name: "Gmail", Email: "me@gmail.example", Provider: ProviderGmail, Auth: AuthOAuth2,
		IMAPHost: "imap.gmail.com", IMAPPort: 993, Status: "ok",
		OAuth: &OAuthCreds{ClientID: "client-1", RefreshToken: "example-refresh-token"}}
	for _, a := range []*MailAccount{pw, oa} {
		if _, err := from.upsertAccount(a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := from.ExportAccount("acc_nope"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("export of an unknown account: %v", err)
	}

	to, toRecs, toDir := newMovingService(t)
	for _, want := range []*MailAccount{pw, oa} {
		data, err := from.ExportAccount(want.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := to.ImportAccount(data)
		if err != nil {
			t.Fatalf("import %s: %v", want.ID, err)
		}
		if got.ID != want.ID || got.Email != want.Email || got.Password != "" || got.OAuth != nil {
			t.Fatalf("imported %+v (secrets must not come back)", got)
		}
		if _, err := to.ImportAccount(data); !errors.Is(err, ErrAccountExists) {
			t.Fatalf("second import of %s: %v", want.ID, err)
		}
	}

	// Sealed with the new key: not in clear text, and not the old manager's ciphertext.
	dump := toRecs.dump()
	for _, secret := range []string{"example-app-password", "example-refresh-token"} {
		if strings.Contains(dump, secret) {
			t.Fatalf("%q stored in clear text", secret)
		}
	}
	fromRows, _ := fromRecs.MailAccounts(context.Background())
	for _, r := range fromRows {
		if (r.PasswordEnc != "" && strings.Contains(dump, r.PasswordEnc)) || (r.OAuthEnc != "" && strings.Contains(dump, r.OAuthEnc)) {
			t.Fatalf("%s's ciphertext was copied, not sealed again", r.ID)
		}
	}
	again, err := New(toDir, toRecs)
	if err != nil {
		t.Fatalf("reopen the receiving service: %v", err)
	}
	defer again.Close()
	gotPW, err := again.account("acc_pw")
	if err != nil || gotPW.Password != pw.Password || gotPW.IMAPHost != pw.IMAPHost || gotPW.SMTPPort != pw.SMTPPort ||
		gotPW.Name != pw.Name || gotPW.Username != pw.Username || gotPW.SMTPSecurity != pw.SMTPSecurity {
		t.Fatalf("password account after the move: %+v, %v", gotPW, err)
	}
	gotOA, err := again.account("acc_oa")
	if err != nil || gotOA.OAuth == nil || *gotOA.OAuth != *oa.OAuth || gotOA.Auth != AuthOAuth2 {
		t.Fatalf("OAuth account after the move: %+v, %v", gotOA, err)
	}

	// The same address under another ID is refused too.
	other := *pw
	other.ID, other.Email = "acc_other", "ME@example.com"
	from2, _, _ := newMovingService(t)
	if _, err := from2.upsertAccount(&other); err != nil {
		t.Fatal(err)
	}
	data, _ := from2.ExportAccount("acc_other")
	if _, err := to.ImportAccount(data); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("import of an address it has: %v", err)
	}
}

// What cannot be an account is refused without quoting it (it holds secrets).
func TestImportAccountRefusesBadData(t *testing.T) {
	s, _, _ := newMovingService(t)
	for _, data := range []string{`{"password":"example-secret"`, `{"email":"a@example.com","password":"example-secret"}`,
		`{"id":"x","email":"a@example.com","auth":"oauth2"}`} {
		_, err := s.ImportAccount([]byte(data))
		if err == nil || strings.Contains(err.Error(), "example-secret") {
			t.Fatalf("import of %s: %v", data, err)
		}
	}
	if as, _ := s.ListAccounts(); len(as) != 0 {
		t.Fatalf("accounts after refused imports: %+v", as)
	}
}
