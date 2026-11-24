package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestUnknownAccountError(t *testing.T) {
	s := &Service{}
	for _, check := range []struct {
		name string
		call func() error
	}{
		{"read", func() error { _, err := s.GetMessage(context.Background(), "none", 1, "INBOX"); return err }},
		{"delete", func() error { return s.DeleteAccount("none") }},
	} {
		var unknown *UnknownAccountError
		if err := check.call(); !errors.As(err, &unknown) || unknown.Account != "none" {
			t.Fatalf("%s: got %v, want UnknownAccountError naming none", check.name, err)
		}
	}
	if got := statusFor(fmt.Errorf("read mail: %w", &UnknownAccountError{Account: "none"})); got != http.StatusNotFound {
		t.Fatalf("wrapped error status %d, want 404", got)
	}
}

// mailLanguageWriter stands for the hub or extmgr writer carrying the request's language.
type mailLanguageWriter struct {
	http.ResponseWriter
	language i18n.Lang
}

func (w mailLanguageWriter) Lang() i18n.Lang { return w.language }

func TestHTTPUnknownAccountLocalized(t *testing.T) {
	s := &Service{}
	const english = `The mail account "none" isn't connected to this assistant any more.`
	for _, c := range []struct {
		lang         i18n.Lang
		method, path string
		want         string
	}{
		{"", http.MethodGet, "/api/v1/mail/messages/none/1", english},
		{i18n.En, http.MethodGet, "/api/v1/mail/messages/none/1", english},
		{i18n.ZhHans, http.MethodGet, "/api/v1/mail/messages/none/1", "邮箱账号“none”已不在这个助手上了。"},
		{i18n.Ja, http.MethodGet, "/api/v1/mail/messages/none/1", "メールアカウント「none」はこのアシスタントに接続されていません。"},
		{i18n.Ja, http.MethodDelete, "/api/v1/mail/accounts/none", "メールアカウント「none」はこのアシスタントに接続されていません。"},
	} {
		t.Run(string(c.lang)+"/"+c.method+c.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(mailLanguageWriter{w, c.lang}, httptest.NewRequest(c.method, c.path, nil))
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusNotFound || body["code"] != "mail_account_unknown" || body["error"] != c.want || body["detail"] != nil {
				t.Fatalf("status %d, body %s; want 404 and %q", w.Code, w.Body, c.want)
			}
		})
	}
}
