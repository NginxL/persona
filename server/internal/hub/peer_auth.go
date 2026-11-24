package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Calls between hubs of a group (docs/server/mesh.md "Mesh links"): a call for a hub the caller has no link to goes hop by hop along
// the route (relay). What one hub may ask another: the center takes over a member's device credentials (creds.import,
// plan-hub-center.md 3 "Device credentials"); a revocation reaches every copy of a credential (creds.revoked, docs/server/mesh.md).

// meshRelayReq is a call to another hub of the group, passed along the way to it.
type meshRelayReq struct {
	To     string          `json:"to"`
	From   string          `json:"from"`
	TTL    int             `json:"ttl"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

var errHubUnreachable = errors.New("that hub cannot be reached now")

// meshCall calls method on hub `to` of the group, through the links on the way.
func (h *Hub) meshCall(ctx context.Context, to, method string, params any, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	self := h.hubID()
	if to == self {
		res, err := h.meshLocalCall(ctx, self, method, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(res)
		if out != nil {
			return json.Unmarshal(b, out)
		}
		return nil
	}
	l := h.nextLink(to)
	if l == nil {
		return errHubUnreachable
	}
	return l.rpc.Call(ctx, "relay", meshRelayReq{To: to, From: self, TTL: meshTTL, Method: method, Params: raw}, out)
}

// meshRelay answers a relayed call for this hub, or passes it on.
func (h *Hub) meshRelay(ctx context.Context, from *peerLink, req meshRelayReq) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second) // the caller gives up sooner; nothing here outlives it long
	defer cancel()
	if req.To == h.hubID() {
		return h.meshLocalCall(ctx, req.From, req.Method, req.Params)
	}
	if req.TTL <= 1 {
		return nil, errors.New("mesh: too many hops")
	}
	l := h.nextLink(req.To)
	if l == nil || l == from {
		return nil, errHubUnreachable
	}
	req.TTL--
	var out json.RawMessage
	err := l.rpc.Call(ctx, "relay", req, &out)
	return out, err
}

// meshLocalCall is what another hub may ask this one.
func (h *Hub) meshLocalCall(ctx context.Context, from, method string, params json.RawMessage) (any, error) {
	switch method {
	case "creds.import":
		// A member's device credentials (hashes): the devices paired there keep working, now that their requests reach
		// this hub. Only the center takes them, and only from a hub that has it for its center.
		if !h.isCenter() {
			return nil, errors.New("this hub is not the center")
		}
		if r := h.recordOf(from); r == nil || r.Center != h.hubID() {
			return nil, errors.New("that hub is not a member of this hub's group")
		}
		var p struct {
			Tokens []db.ExportedToken `json:"tokens"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		cs := h.centerStore()
		if cs == nil {
			return nil, errors.New("no database")
		}
		var keep []db.ExportedToken
		for _, t := range p.Tokens {
			if len(t.Hash) == 64 && (t.Role == roleFull || t.Role == roleAnswer) && t.ID != "" {
				t.MachineID = ""
				keep = append(keep, t)
			}
		}
		n, err := cs.ImportDeviceTokens(ctx, keep)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			h.logf("mesh: took over %d device credentials from %q", n, h.hubNameOf(from))
			// A request that came before the handover cached "no such token" for tokenCacheFor: forget it, or the
			// device just handed over is refused (a 401 apps read as a revoked credential) for that long.
			hashes := make([]string, len(keep))
			for i, t := range keep {
				hashes[i] = t.Hash
			}
			h.forgetUnknownTokens(hashes)
		}
		return map[string]any{"added": n}, nil
	}
	if method == "creds.revoked" {
		var p struct {
			Tokens []db.ExportedToken `json:"tokens"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		cs := h.centerStore()
		if cs == nil {
			return nil, errors.New("no database")
		}
		var keep []db.ExportedToken
		for _, t := range p.Tokens {
			if len(t.Hash) == 64 && (t.Role == roleFull || t.Role == roleAnswer) && t.ID != "" {
				keep = append(keep, t)
			}
		}
		ids, err := cs.ApplyRevokedTokens(ctx, keep)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			h.revoked(id)
		}
		return map[string]any{"revoked": len(ids)}, nil
	}
	if method == "pair.code" { // a member's `personad hub pair`: the app pairs with the center
		if !h.isCenter() {
			return nil, errors.New("this hub is not the center")
		}
		return h.newPairCode(ctx, "")
	}
	return nil, errors.New("unknown mesh call " + method)
}

// revocationResendEvery is how often a hub repeats the revoked credentials to the hubs it can reach: whatever a lost
// message, a change viewLoop did not see or a failed write left behind.
var revocationResendEvery = 5 * time.Minute

// sendRevocations tells these hubs which app credentials this one knows to be revoked. Every hub keeps its own copy of a
// credential it issued or was handed, and a revocation that stayed on one hub would end the day another became the
// center. Sent when a credential is revoked, when a hub can be reached again and every revocationResendEvery; the whole
// list each time, it is short and taking it twice changes nothing.
func (h *Hub) sendRevocations(to []string) {
	cs := h.centerStore()
	if cs == nil || len(to) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		list, err := cs.RevokedAppTokens(ctx)
		cancel()
		if err != nil {
			h.logf("mesh: the revoked credentials: %v", err)
			return
		}
		if len(list) == 0 {
			return
		}
		for _, id := range to {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := h.meshCall(ctx, id, "creds.revoked", map[string]any{"tokens": list}, nil)
			cancel()
			// A hub from before this cannot take it until it is updated.
			if err != nil && !errors.Is(err, errHubUnreachable) && !strings.Contains(err.Error(), "unknown mesh call") {
				h.logf("mesh: tell %s about the revoked credentials: %v", h.hubLabel(id), err)
			}
		}
	}()
}

// recordOf is the newest record of a hub (nil when none is held).
func (h *Hub) recordOf(id string) *hubRecord {
	ms := &h.mesh
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.records[id]
}
