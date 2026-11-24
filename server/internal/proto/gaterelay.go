package proto

// gate.relay: a call of a relayed command gate (CommandGate.Relay) that the node's gate proxy sends to the manager,
// which owns the extension's unix socket (#12904). The node holds the call to the gate's rules first, as for any
// proxied gate; the manager adds the credentials itself (the node sends none) and answers what the extension said.

// MethodGateRelay is the node's request to the manager.
const MethodGateRelay = "gate.relay"

// GateRelayMaxBody bounds a relayed request's body and its answer's body.
const GateRelayMaxBody = 8 << 20

// GateRelayParams is one call: the gated command, the method, the path past the base (with the gate's path_prefix, as
// the script sent it), the raw query, the headers the gate lets through and the body.
type GateRelayParams struct {
	Command string              `json:"command"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Header  map[string][]string `json:"header,omitempty"`
	Body    []byte              `json:"body,omitempty"` // base64 in JSON
}

// GateRelayAnswer is the extension's answer, its body read to the end (at most GateRelayMaxBody).
type GateRelayAnswer struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}
