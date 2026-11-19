package subscriptions

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestSubscriptionKindDescriptionsFollowOwnerLanguage(t *testing.T) {
	for _, tc := range []struct {
		lang i18n.Lang
		word string
	}{{i18n.ZhHans, "订阅"}, {i18n.En, "subscription"}, {i18n.Ja, "購読"}} {
		kinds := kindsOf("原名 #42", tc.lang)
		for kind, text := range kinds {
			if !strings.Contains(text, "原名 #42") || !strings.Contains(text, tc.word) {
				t.Fatalf("%s %s: %s", tc.lang, kind, text)
			}
		}
	}
}
