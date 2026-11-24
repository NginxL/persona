//go:build !persona_without_screen_share && !persona_core_only

package node

import (
	"io"

	"github.com/nwverse/persona/server/internal/modules/screen-share/screenlink"
	"github.com/nwverse/persona/server/internal/node/screenport"
)

// The screen-share module on the node: a live screen's session (#6398) over the hub's relays and, where the macOS
// firewall would not ask, this computer's direct routes.
func init() {
	screenport.Provide(func(logf func(string, ...any)) screenport.Links {
		r := screenlink.NewRegistry(screenlink.DefaultLimits, logf)
		if d, err := screenlink.NewDirect(logf); err != nil {
			logf("screen: no direct routes: %v", err)
		} else {
			r.EnableDirect(d)
		}
		return screenLinks{r}
	})
}

// screenLinks is the module's registry as the node's port has it.
type screenLinks struct{ r *screenlink.Registry }

func (l screenLinks) Get(pair string) screenport.Session {
	if s := l.r.Get(pair); s != nil {
		return s
	}
	return nil // not a typed nil: the node checks for none
}

func (l screenLinks) Open(pair string, rfb io.ReadWriteCloser) io.ReadWriteCloser {
	_, relay := l.r.Open(pair, rfb)
	return relay
}

func (l screenLinks) Len() int { return l.r.Len() }
