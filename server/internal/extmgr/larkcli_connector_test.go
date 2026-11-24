package extmgr

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// needsLarkCLI skips a test of the lark-cli connector in a build without the lark module (internal/cliport).
func needsLarkCLI(t testing.TB) {
	t.Helper()
	if !cliport.Available() {
		t.Skip("the lark module is not in this build")
	}
}

// The lark-cli connector (internal/modules/lark) passes the connector framework's own check, as the registry runs it.
func TestLarkCLIConnectorPassesTheFrameworkCheck(t *testing.T) {
	needsLarkCLI(t)
	c := cliport.NewConnector(cliwire.Config{Bin: "lark-cli"})
	if err := connector.Check(c); err != nil {
		t.Fatal(err)
	}
}

// Its display name follows the language.
func TestLarkCLIConnectorDisplayNameFollowsLanguage(t *testing.T) {
	needsLarkCLI(t)
	c := cliport.NewConnector(cliwire.Config{Bin: "lark-cli"})
	if got := connector.DisplayName(i18n.With(context.Background(), i18n.ZhHans), c); got != "飞书 · 我的消息" { // i18n-ignore: the zh-Hans catalog entry under test
		t.Fatalf("zh-Hans name = %q", got)
	}
	if got := connector.DisplayName(i18n.With(context.Background(), "en"), c); got != "Lark · my messages" {
		t.Fatalf("en name = %q", got)
	}
	if c.Name() != "Lark via lark-cli" {
		t.Fatalf("stable name changed: %q", c.Name())
	}
}
