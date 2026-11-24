package netproxy

import (
	"net/http"
	"time"
)

// Egress is how a connector reaches the services it reads. A connector's hub.json config embeds it to get a "proxy"
// key: an http://[user:pass@]host:port proxy for everything the connector sends out (some networks reach a service
// only through one); empty is direct. The address belongs in the hub's own hub.json only.
type Egress struct {
	Proxy string `json:"proxy,omitempty"`
}

// HTTPClient is the connector's client: through the proxy when one is set, else direct. A malformed proxy is an
// error, never a silent fall back to direct.
func (e Egress) HTTPClient(timeout time.Duration) (*http.Client, error) {
	u, err := Parse(e.Proxy)
	if err != nil {
		return nil, err
	}
	return HTTPClient(u, timeout), nil
}
