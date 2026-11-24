package mail

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/mailwire"
)

// The Outlook client ID is sealed in settings (microsoftClientSetting): the key rotation re-seals the field the contract
// names for it (db.SealedSettings lists mailwire.MicrosoftClientSealedField), so sealedSetting holds exactly that one.
func TestTheMicrosoftClientIsReSealed(t *testing.T) {
	typ := reflect.TypeOf(sealedSetting{})
	var fields []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		fields = append(fields, name)
	}
	if want := []string{mailwire.MicrosoftClientSealedField}; !slices.Equal(fields, want) {
		t.Errorf("sealedSetting has %v, the key rotation re-seals %v", fields, want)
	}
}
