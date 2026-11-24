//go:build !persona_without_lark && !persona_core_only

package all

import (
	"net/http"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/modules/lark"
	larkconnector "github.com/nwverse/persona/server/internal/modules/lark/connector/larkcli"
	"github.com/nwverse/persona/server/internal/modules/lark/feishubot"
	"github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

// The Feishu / Lark module: the bot's client (internal/botport) and the lark-cli adapter and its connector
// (internal/cliport), which the extension host drives, and its assistant layer, the outbound channel port the
// assistant reaches the owner's chat through (internal/larkhost).
func init() {
	modules = append(modules, lark.Module{})
	botport.Provide(botport.Provider{
		New: func(appID, appSecret, domain string, h *http.Client) botport.Client {
			c := feishubot.New(appID, appSecret, domain)
			if h != nil {
				c = c.WithHTTP(h)
			}
			return c
		},
		NewConn: func(api botport.Client, handler botcard.Handler, logf func(string, ...any)) botport.Conn {
			return feishubot.NewConn(api.(*feishubot.Client), handler, logf)
		},
		NewRegistrar: func(host string) botport.Registrar {
			r := feishubot.NewRegistrar()
			if host != "" {
				r = r.WithHost(host)
			}
			return r
		},
	})
	larkhost.Provide(func(h larkhost.Host) larkhost.Layer { return lark.New(h) })
	cliport.Provide(cliport.Provider{
		NewRunner:    func(cfg cliwire.Config) cliport.Runner { return larkcli.New(cfg) },
		NewConnector: func(cfg cliwire.Config) cliport.Connector { return larkconnector.New(cfg) },
	})
}
