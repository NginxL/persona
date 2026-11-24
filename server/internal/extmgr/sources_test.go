package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// mailRows is mail's Records in memory, one per manager (each has its own database).
type mailRows struct {
	mu   sync.Mutex
	rows []db.MailAccountRow
}

func (m *mailRows) MailAccounts(context.Context) ([]db.MailAccountRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]db.MailAccountRow(nil), m.rows...), nil
}

func (m *mailRows) ReplaceMailAccounts(_ context.Context, rows []db.MailAccountRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append([]db.MailAccountRow(nil), rows...)
	return nil
}

// managerWithMail is a test manager with the real mail on its own data directory (its own secret.key).
func managerWithMail(t *testing.T) (*Manager, Mail) {
	t.Helper()
	h := newTestManager(t)
	s := openTestMail(t, h.cfg.dir, &mailRows{})
	h.mail = s
	return h, s
}

func sourceRequest(t *testing.T, h *Manager, method string, params any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	out, handled, err := h.Request(context.Background(), method, raw)
	if !handled {
		t.Fatalf("%s not handled", method)
	}
	return out, err
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error %v, want code %s", err, code)
	}
	if strings.Contains(re.Message, "example-app-password") {
		t.Fatalf("the error quotes the secret: %s", re.Message)
	}
}

// A mail account moves from one manager to another over the three bus methods, its password with it; a second import
// is refused, the old manager no longer has it, and what is not a mail account here gets its code.
func TestSourceMoveMailAccount(t *testing.T) {
	from, fromMail := managerWithMail(t)
	to, toMail := managerWithMail(t)

	// Put an account in the old manager by importing it (AddAccount would test the connections).
	seed, _ := json.Marshal(map[string]any{"id": "acc_1", "name": "Work", "email": "me@example.com", "provider": "imap",
		"auth": "password", "imap_host": "imap.example.com", "imap_port": 993, "smtp_host": "smtp.example.com",
		"smtp_port": 465, "smtp_security": "tls", "password": "example-app-password"})
	if _, err := fromMail.ImportAccount(seed); err != nil {
		t.Fatal(err)
	}

	out, err := sourceRequest(t, from, extwire.MethodSourceExport, extwire.SourceRef{Source: "mail:acc_1"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := out.(extwire.SourceBundle)
	if bundle.Source != "mail:acc_1" || bundle.Kind != "mail" || !strings.Contains(string(bundle.Data), "example-app-password") {
		t.Fatalf("bundle %s %s (the password must be in it)", bundle.Source, bundle.Kind)
	}
	// Over the bus it is JSON: the import takes what the export's answer marshals to.
	wire, _ := json.Marshal(bundle)
	var got extwire.SourceBundle
	_ = json.Unmarshal(wire, &got)

	out, err = sourceRequest(t, to, extwire.MethodSourceImport, got)
	if err != nil {
		t.Fatal(err)
	}
	if ref := out.(extwire.SourceRef); ref.Source != "mail:acc_1" {
		t.Fatalf("import answered %+v", ref)
	}
	accounts, _ := toMail.ListAccounts()
	if len(accounts) != 1 || accounts[0].ID != "acc_1" || accounts[0].IMAPHost != "imap.example.com" {
		t.Fatalf("receiving manager has %+v", accounts)
	}
	again, _ := toMail.ExportAccount("acc_1")
	if !strings.Contains(string(again), "example-app-password") {
		t.Fatal("the password did not move")
	}
	_, err = sourceRequest(t, to, extwire.MethodSourceImport, got)
	wantCode(t, err, extwire.CodeSourceExists)

	if _, err := sourceRequest(t, from, extwire.MethodSourceRelease, extwire.SourceRef{Source: "mail:acc_1"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := fromMail.ListAccounts(); len(left) != 0 {
		t.Fatalf("old manager still has %+v", left)
	}
	_, err = sourceRequest(t, from, extwire.MethodSourceExport, extwire.SourceRef{Source: "mail:acc_1"})
	wantCode(t, err, extwire.CodeNoSource)
	_, err = sourceRequest(t, from, extwire.MethodSourceRelease, extwire.SourceRef{Source: "mail:acc_1"})
	wantCode(t, err, extwire.CodeNoSource)

	// Other kinds cannot move.
	_, err = sourceRequest(t, from, extwire.MethodSourceExport, extwire.SourceRef{Source: "feishu:x"})
	wantCode(t, err, extwire.CodeSourceKind)
	_, err = sourceRequest(t, to, extwire.MethodSourceImport, extwire.SourceBundle{Source: "feishu:x", Kind: "feishu", Data: json.RawMessage(`{}`)})
	wantCode(t, err, extwire.CodeSourceKind)
}

// Without mail, there is no mail source here.
func TestSourceWithoutMail(t *testing.T) {
	h := newTestManager(t)
	h.mail = nil
	_, err := sourceRequest(t, h, extwire.MethodSourceExport, extwire.SourceRef{Source: "mail:acc_1"})
	wantCode(t, err, extwire.CodeNoSource)
	_, err = sourceRequest(t, h, extwire.MethodSourceRelease, extwire.SourceRef{Source: "mail:acc_1"})
	wantCode(t, err, extwire.CodeNoSource)
	_, err = sourceRequest(t, h, extwire.MethodSourceImport, extwire.SourceBundle{Source: "mail:acc_1", Kind: "mail", Data: json.RawMessage(`{}`)})
	wantCode(t, err, extwire.CodeNoSource)
}
