package extension

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestPushGradingScopeIsNamedOnly(t *testing.T) {
	const scope = "push.grading"
	if !slices.Contains(HubScopes, scope) {
		t.Errorf("the manifest vocabulary does not include %s", scope)
	}
	if InEveryScope(scope) {
		t.Errorf("the legacy all-scope grant includes %s", scope)
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		if title, ok := ScopeTitleIn(lang, scope); !ok || title == "" {
			t.Errorf("%s has no install-screen explanation for %s", lang, scope)
		}
	}
}
