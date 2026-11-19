package hub

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// A session.modes call carries who asked, for the manager's record of mode changes (#10297): a device with its id and
// name, the hub's own token, or an extension by its id. Display only.
func TestModesActor(t *testing.T) {
	for _, c := range []struct {
		who  principal
		want devicewire.Actor
	}{
		{principal{ID: "d1", Name: "Phone", Role: roleFull}, devicewire.Actor{Kind: "device", DeviceID: "d1", DeviceName: "Phone"}},
		{principal{Role: roleFull}, devicewire.Actor{Kind: "hub"}},
		{principal{ID: "x", Name: "token name", Role: roleExtension, Extension: "notes"}, devicewire.Actor{Kind: "extension", Extension: "notes"}},
	} {
		got := modesActor(context.WithValue(context.Background(), principalKey{}, c.who))
		if got == nil || *got != c.want {
			t.Errorf("%+v: actor = %+v, want %+v", c.who, got, c.want)
		}
	}
}
