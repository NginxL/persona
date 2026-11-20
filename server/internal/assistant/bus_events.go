package assistant

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// queueEvent puts one of the hub's events in line (the read loop's: it must not block). The text a followed
// conversation writes (session.delta) comes many times a second and only its latest matters: one per conversation
// waits at a time, the newest text replacing the older. A full queue drops only what the next one makes up for (a
// delta, a followed conversation's items: its answer is read from the transcript); any other event waits for room
// on its own, so none is lost.
func (b *BusHub) queueEvent(event string, params json.RawMessage) {
	if event == assistantwire.EventSessionDelta {
		var p struct {
			MachineID string `json:"machine_id"`
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &p)
		key := p.MachineID + "/" + p.SessionID
		b.deltaMu.Lock()
		if b.deltas == nil {
			b.deltas = map[string]json.RawMessage{}
		}
		_, waiting := b.deltas[key]
		b.deltas[key] = params
		b.deltaMu.Unlock()
		if waiting {
			return // the one in line takes this text
		}
		params = json.RawMessage(`"` + key + `"`) // the key: the text is taken when it comes up (takeDelta)
	}
	b.pending.Add(1)
	ev := busEvent{event, params, b.conns.Load()}
	select {
	case b.events <- ev:
		return
	default:
	}
	switch event {
	case assistantwire.EventSessionDelta, assistantwire.EventSessionItems:
		if event == assistantwire.EventSessionDelta {
			b.takeDelta(params)
		}
		b.pending.Add(-1)
		b.opts.Logf("assistant: %s dropped: too many events waiting", event)
	default:
		go func() { b.events <- ev }()
	}
}

// takeDelta is the latest text of the conversation a queued session.delta names (its key), taken out.
func (b *BusHub) takeDelta(key json.RawMessage) json.RawMessage {
	var k string
	_ = json.Unmarshal(key, &k)
	b.deltaMu.Lock()
	defer b.deltaMu.Unlock()
	p := b.deltas[k]
	delete(b.deltas, k)
	if p == nil {
		return json.RawMessage(`{}`)
	}
	return p
}
