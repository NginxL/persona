package extmgr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extmgr/mailport"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mailwire"

	// The built-in modules, as the manager links them: the mail module provides itself to mailport.
	_ "github.com/nwverse/persona/server/internal/modules/all"
)

// needsMailModule skips a test of the mail module's behaviour when the build leaves it out (persona_without_mail,
// persona_core_only).
func needsMailModule(t *testing.T) mailwire.Factory {
	t.Helper()
	f := mailport.Factory()
	if f == nil {
		t.Skip("this build has no mail module (persona_without_mail or persona_core_only)")
	}
	return f
}

// openTestMail is the real mail module on dir (its own secret.key there) and rows, closed when the test ends.
func openTestMail(t *testing.T, dir string, rows mailwire.Store) Mail {
	t.Helper()
	s, err := needsMailModule(t)(mailwire.Config{DataDir: dir, SecretKey: filepath.Join(dir, "secret.key"), Store: rows})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := s.(io.Closer); ok {
		t.Cleanup(func() { c.Close() })
	}
	return s
}

// testMail is the real mail module with no accounts, on a directory of its own.
func testMail(t *testing.T) Mail {
	t.Helper()
	return openTestMail(t, t.TempDir(), &mailRows{})
}

// mailRows keeps a test's mail accounts in memory (the mail_accounts table) and no settings.
func (m *mailRows) Setting(context.Context, string, any) (bool, error) { return false, nil }
func (m *mailRows) PutSetting(context.Context, string, any) error      { return nil }

// unknownMailAccount stands for the mail module's error for an account no longer connected (its UnknownAccountError):
// the same code and status, an error the core sees only through them.
type unknownMailAccount string

func (u unknownMailAccount) Error() string {
	return i18n.T(i18n.En, "mail-errors.account_unknown", i18n.Args{"account": string(u)})
}
func (unknownMailAccount) Code() string    { return "mail_account_unknown" }
func (unknownMailAccount) HTTPStatus() int { return http.StatusNotFound }

// Without the mail module (persona_without_mail, persona_core_only, or a factory that failed) mail is absent: its
// routes answer 404, no mail_* tool reaches the agents or the assistant, and persona_settings has no mail.accounts.
func TestManagerWithoutMail(t *testing.T) {
	h := newTestManager(t)
	h.mail = nil
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	for _, assistant := range []bool{false, true} {
		for _, tl := range toolsForAgents(t, h, assistant) {
			if strings.HasPrefix(tl.Name, "mail_") {
				t.Errorf("assistant %v is offered %s without mail", assistant, tl.Name)
			}
		}
	}
	if h.connectors.Get("mail") != nil {
		t.Error("the mail connector is registered without mail")
	}
	for _, s := range h.selfSettings() {
		if strings.HasPrefix(s.key, "mail.") {
			t.Errorf("persona_settings lists %s without mail", s.key)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/accounts", nil)
	if code, body := callReq(t, h.routes(), req, h.cfg.Token); code != http.StatusNotFound {
		t.Errorf("GET /api/v1/mail/accounts without mail: %d %v, want 404", code, body)
	}
}
