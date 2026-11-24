package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type ownerAccountsMail struct {
	Mail
	accounts []mailwire.MailAccount
}

func (m ownerAccountsMail) Tools() []proto.Tool                           { return nil }
func (m ownerAccountsMail) ListAccounts() ([]mailwire.MailAccount, error) { return m.accounts, nil }
func TestKBConnectorOwnerOnlyReturnsIdentityMetadata(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.mail = ownerAccountsMail{accounts: []mailwire.MailAccount{{ID: "account", Name: "private label", Email: "owner@example.com", Username: "private login", IMAPHost: "imap.example.com", Password: "private secret"}}}
	if err := h.addConnector(mailConnector{h: h}); err != nil {
		t.Fatal(err)
	}
	ask := func() string {
		t.Helper()
		raw, ok, err := h.kbSourcesCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsOwner, Params: json.RawMessage(`{}`)})
		if !ok || err != nil {
			t.Fatalf("owner metadata: %v %v", ok, err)
		}
		return string(raw)
	}
	out := ask()
	if out != `{"sources":[{"source":"mail","handles":[{"kind":"email","value":"owner@example.com"}]}]}` {
		t.Fatalf("unexpected metadata: %s", out)
	}
	for _, private := range []string{"private", "password", "username", "imap", "account"} {
		if strings.Contains(out, private) {
			t.Fatalf("account data leaked: %s", out)
		}
	}
	if err := h.connectors.SetFeed("mail", connector.FeedArchive, false); err != nil {
		t.Fatal(err)
	}
	if out := ask(); out != `{"sources":[]}` {
		t.Fatalf("owner from disabled source: %s", out)
	}
}
