package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extmgr/mailport"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The mail accounts in persona_settings are the manager's (mail is); the card is the hub's (the fake hub here).

type selfSettingsMail struct {
	Mail
	added []mailwire.AddAccountRequest
}

func (f *selfSettingsMail) Tools() []proto.Tool { return nil }

// PasswordGuide: the example domains have none.
func (f *selfSettingsMail) PasswordGuide(string, string) string { return "" }
func (f *selfSettingsMail) AddAccount(req mailwire.AddAccountRequest) (*mailwire.MailAccount, error) {
	f.added = append(f.added, req)
	return &mailwire.MailAccount{ID: "acc_1", Email: req.Email, Provider: req.Provider}, nil
}

func TestSelfSettingsMailRequest(t *testing.T) {
	req, summary, err := mailAccountRequest(i18n.Default, json.RawMessage(`{"email":" Me@Example.com ","imap_host":"imap.example.com","imap_port":993,"smtp_host":"smtp.example.com"}`), nil)
	if err != nil || req.Email != "me@example.com" || req.Password != "" || req.Provider != mailwire.ProviderIMAP || !strings.Contains(summary, "imap.example.com:993") {
		t.Fatalf("request %+v, summary %q, error %v", req, summary, err)
	}
	for _, input := range []string{`{"email":"a@gmail.com"}`, `{"email":"a@example.com","provider":"gmail"}`, `{"email":"nope"}`} {
		if _, _, err := mailAccountRequest(i18n.Default, json.RawMessage(input), nil); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

// Adding an account asks on the phone, the password typed there (a secret field): declined, or approved without it,
// nothing is added; approved with it, mail gets it.
func TestSelfSettingsMailNeedsPhoneInput(t *testing.T) {
	for _, tt := range []struct {
		name     string
		answer   proto.Resolution
		added    bool
		wantsErr bool
	}{
		{"declined", proto.Resolution{Allow: new(false)}, false, false},
		{"approved without the password", proto.Resolution{Allow: new(true)}, false, true},
		{"approved", proto.Resolution{Allow: new(true), Values: map[string]string{"password": "example-password"}}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestManager(t)
			fm := &selfSettingsMail{}
			h.mail = fm
			hubOf(h).answer = func(proto.Interaction) proto.Resolution { return tt.answer }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := h.toolPersonaSettings(ctx, "server", "s1", json.RawMessage(`{"action":"add","key":"mail.accounts","value":{"email":"me@example.com"}}`))
			asked := hubOf(h).asked()
			if len(asked) != 1 || len(asked[0].Fields) != 1 || !asked[0].Fields[0].Secret || asked[0].MachineID != "server" {
				t.Fatalf("missing secure approval card: %+v", asked)
			}
			if (err != nil) != tt.wantsErr {
				t.Fatalf("error %v", err)
			}
			if tt.added != (len(fm.added) == 1) {
				t.Fatal("account was not gated by approval")
			}
			if tt.added && fm.added[0].Password != "example-password" {
				t.Fatal("phone input was not passed to mail")
			}
		})
	}
}

// What the card says, in each language.
func TestMailAccountRequestInEnglish(t *testing.T) {
	// The providers' guides are the mail module's; a build without it checks the rest.
	var guide func(lang, email string) string
	if mailport.Factory() != nil {
		guide = testMail(t).PasswordGuide
		if _, summary, err := mailAccountRequest(i18n.Default, json.RawMessage(`{"email":"me@qq.com"}`), guide); err != nil ||
			!strings.Contains(summary, "mail.qq.com") || !strings.Contains(summary, "生成授权码") {
			t.Errorf("mail qq.com: %q %v", summary, err)
		}
		if _, summary, err := mailAccountRequest(i18n.En, json.RawMessage(`{"email":"me@163.com"}`), guide); err != nil ||
			!strings.Contains(summary, "mail.163.com") || !strings.Contains(summary, "authorization code") {
			t.Errorf("mail 163.com en: %q %v", summary, err)
		}
	}
	if _, summary, err := mailAccountRequest(i18n.En, json.RawMessage(`{"email":"me@qq.com","imap_host":"imap.example.com"}`), guide); err != nil ||
		strings.Contains(summary, "authorization code") {
		t.Errorf("mail qq.com with its own servers: %q %v", summary, err)
	}
	_, summary, err := mailAccountRequest(i18n.En, json.RawMessage(`{"email":"me@example.com"}`), guide)
	if err != nil || summary != "Add mail account me@example.com (servers detected automatically). After you enter the password it connects once before saving" {
		t.Errorf("mail en: %q %v", summary, err)
	}
	if got := computerSummary(i18n.En, "Studio", "run_script", "open -a Notes\nsecond", nil); got != "Run a script on “Studio”: open -a Notes" {
		t.Errorf("computer en: %q", got)
	}
	if got := computerSummary(i18n.ZhHans, "Studio", "run_script", "open -a Notes", nil); got != "在「Studio」上运行脚本：open -a Notes" {
		t.Errorf("computer zh: %q", got)
	}
	if got := clipped(i18n.En, " "); got != "(none)" {
		t.Errorf("none en: %q", got)
	}
	if got := changeSummary(i18n.ZhHans, "规则", "", "新"); got != "改规则：（无） → 新" {
		t.Errorf("change zh: %q", got)
	}
}
