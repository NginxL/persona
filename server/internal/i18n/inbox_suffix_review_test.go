package i18n

import (
	"strings"
	"testing"
)

// InboxText re-finds a text's key in the catalog and drops the key's plural suffix to get the base it looks up
// (makeCatalogTemplates). A key whose name merely ends in _one, _two, _few, _many or _other is therefore looked up
// as a stem that is not a key, and the text reads back as that stem (#6955: inbox-errors.merge_few). A suffix is only
// a plural form when its stem is a key itself or when the catalog holds several of its forms; any other inbox error
// key has a name to change. The message text stays as it is.
func TestInboxErrorKeysCarryNoBarePluralSuffix(t *testing.T) {
	suffixes := []string{"_zero", "_one", "_two", "_few", "_many", "_other"}
	for _, l := range []Lang{En, ZhHans, Ja} {
		cat := catalogs()[l]
		for key, text := range cat {
			if !strings.HasPrefix(key, "inbox-errors.") {
				continue
			}
			stem := ""
			for _, s := range suffixes {
				if strings.HasSuffix(key, s) {
					stem, _ = strings.CutSuffix(key, s)
				}
			}
			if stem == "" {
				continue
			}
			if _, ok := cat[stem]; ok {
				continue // a plural form of a key that exists
			}
			forms := 0
			for _, s := range suffixes {
				if _, ok := cat[stem+s]; ok {
					forms++
				}
			}
			if forms < 2 {
				t.Errorf("%s: %s reads back as %q, not %q: its name ends in a plural suffix and %s is not a key; rename the key",
					l, key, stem, text, stem)
			}
		}
	}
}
