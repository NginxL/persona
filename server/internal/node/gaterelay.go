package node

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// gateRelayFor bounds one relayed call: the manager's own bound and the way there and back.
const gateRelayFor = 75 * time.Second

// relay sends a call the proxy let through for a relayed gate (proto.CommandGate.Relay, #12904) to the manager, which
// owns the extension's socket and puts the credentials on it (gate.relay), and answers what the extension said. path is
// the call's path past the base, as the script sent it; allowed the request headers the gate lets through.
func (p *gateProxy) relay(ctx context.Context, n *Node, w http.ResponseWriter, r *http.Request, command, path string, allowed []string) {
	peer := n.peer
	if peer == nil {
		gateRefuse(w, http.StatusServiceUnavailable, command+" 现在用不了：这台电脑没连上 hub。")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, proto.GateRelayMaxBody+1))
	if err != nil {
		gateRefuse(w, http.StatusBadRequest, "读不完请求："+err.Error())
		return
	}
	if len(body) > proto.GateRelayMaxBody {
		gateRefuse(w, http.StatusRequestEntityTooLarge, "请求太大了（最多 "+strconv.Itoa(proto.GateRelayMaxBody>>20)+" MiB）。")
		return
	}
	params := proto.GateRelayParams{Command: command, Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
		Header: map[string][]string{}}
	for name, vs := range r.Header {
		if gatePassHeader(name, allowed) {
			params.Header[name] = vs
		}
	}
	ctx, cancel := context.WithTimeout(ctx, gateRelayFor)
	defer cancel()
	var ans proto.GateRelayAnswer
	if err := peer.Call(ctx, proto.MethodGateRelay, params, &ans); err != nil {
		gateRefuse(w, http.StatusBadGateway, command+" 的接口连不上："+err.Error())
		return
	}
	if ans.Status < 100 || ans.Status > 999 {
		gateRefuse(w, http.StatusBadGateway, command+" 的接口没给出有效的应答。")
		return
	}
	for k, vs := range ans.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(ans.Body)))
	w.WriteHeader(ans.Status)
	_, _ = w.Write(ans.Body)
}
