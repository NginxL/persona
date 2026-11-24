package devices

import (
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// A manager whose hub is in office network mode never dials tailcat, whatever its machines say.
func TestTailcatRefusedInOfficeNetworkMode(t *testing.T) {
	d := &Devices{cfg: devicewire.Config{PrivateNetwork: true}}
	if c, err := d.tailcatClient(); c != nil || !errors.Is(err, errTailcatPrivate) {
		t.Fatalf("client %v, err %v", c, err)
	}
}
