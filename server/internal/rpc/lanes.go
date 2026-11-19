package rpc

// Lanes (#10660, docs/protocol/protocol.md "Lanes"): a connection both of whose ends opt in (EnableLanes) can switch
// from JSON lines to frames, each carrying a piece of one line on one of four lanes. The writer sends the lanes in
// strict priority (control, interactive, events, bulk), short lines of a lane taking turns with its oldest long one, so
// a pong or an answer to a tap waits at most for one frame already on its way, not for a 1 MiB file chunk ahead of it
// on the one writer. Requests and bulk lines also wait for credit (creditWindow): the other side acknowledges the
// frames it has read, and only so many of their bytes may be sent and not yet acknowledged, so what sits in the
// transport's own buffers (ssh, a relay, TCP) ahead of a pong stays bounded too, and a peer that stops reading holds
// those writers back (route says why events, answers and control never wait).
//
// The switch is negotiated on the connection itself, so old peers on either end, and every byte pipe in between,
// see nothing new:
//
//	→ {"id":"h3","method":"rpc.lanes","params":{"versions":[1]}}     (a line)
//	← {"id":"h3","result":{"version":1}}                              (the answering side's last line; frames follow)
//	→ {"event":"rpc.lanes","params":{"version":1}}                    (the asking side's last line; frames follow)
//
// A peer without lanes answers the request with an error (an unknown method) and both sides keep lines. Each side
// switches its output under its writer's lock, right after the line that announces it, and its input in the read
// loop, right after the line that announces the other side's switch, so no byte in flight is read the wrong way.
//
// A frame is a 16-byte header and its payload: up to framePayloadMax bytes of a line (without its newline), or an
// acknowledgement's 4 bytes:
//
//	[0]     kind: 'D' a piece of a line, 'A' an acknowledgement
//	[1]     lane (D), 0 (A)
//	[2]     flags: 1 the line's last piece (D), 0 (A); the other bits 0
//	[3]     0
//	[4:6]   slot: which of the lane's lines in progress the piece belongs to (D), 0 (A)
//	[6:8]   payload length, big endian (D), 4 (A)
//	[8:16]  D: the frame's sequence number in this direction, 1 for the first, big endian; A: the last sequence
//	        number the sender has read, every frame before it included
//
// An acknowledgement's payload is the time its sender read that last frame, by its own clock (clockMS, big endian):
// with the time the frame was written, by the other's, it gives a delay that counts only this direction, whatever
// waits ahead of the acknowledgement on its own way back (creditWindow).
//
// Sequence numbers and the cumulative acknowledgement are what a resumed connection (#10663) is built on: it keeps the
// frames not yet acknowledged and sends them again after a reconnect. Version 1 uses them for the window only.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Lane is one of the classes a framed connection sends in strict priority.
type Lane uint8

const (
	LaneControl     Lane = iota // ping and its pong, rpc.cancel, flow control
	LaneInteractive             // requests and their answers that are not bulk
	LaneEvents                  // events that are not bulk
	LaneBulk                    // file chunks, uploads, node update packages, port bytes
	laneCount
)

// creditLane: frames of lane may wait for credit (creditWindow), so the reader acknowledges them; which lines do is
// their route's.
func creditLane(l Lane) bool { return l == LaneInteractive || l == LaneBulk }

// route is where a line goes once the output is framed: its lane, and whether it waits for credit. Interactive
// requests and bulk lines do (bulk events too: the port streams send them from their pumps). An event of the events
// lane never does: a synchronous event handler can send one from the read loop, where waiting for credit would wait for
// an acknowledgement only that loop reads (control is what credit travels on). Nor
// does an interactive answer: a callback in the read loop may wait for one to be written (AfterReply), which would
// wait for credit the same way, and on the one writer a pong waited behind a whole answer anyway. A read loop must not
// wait for this side's requests or bulk lines to be written: a request from it would wait for an answer only it reads,
// and a bulk line for credit.
type route struct {
	lane   Lane
	credit bool
}

var controlRoute = route{lane: LaneControl}

var laneNames = [laneCount]string{"control", "interactive", "events", "bulk"}

func (l Lane) String() string {
	if l < laneCount {
		return laneNames[l]
	}
	return fmt.Sprintf("lane%d", l)
}

// lanesMethod is the request that asks for frames and the event that marks the asking side's switch.
const lanesMethod = "rpc.lanes"

// lanesVersion is the frame format this code speaks.
const lanesVersion = 1

// CodeLanesRefused is the code of an answer to rpc.lanes from a peer with lanes enabled that will not switch.
const CodeLanesRefused = "lanes_refused"

type lanesRequest struct {
	Versions []int `json:"versions"`
}

type lanesAnswer struct {
	Version int `json:"version"`
}

// lanesAccepted: result, the answer to this side's rpc.lanes, switches the other side to frames this code reads.
func lanesAccepted(m message) bool {
	if m.Error != nil || len(m.Result) == 0 {
		return false
	}
	var a lanesAnswer
	return json.Unmarshal(m.Result, &a) == nil && a.Version == lanesVersion
}

// laneTable says which lane a message goes on, by method (a request and its answer) or event; nil for a peer without
// lanes. A name on no list goes interactive (a request, an answer) or on the events lane (an event).
type laneTable map[string]Lane

func newLaneTable(control, bulk []string) laneTable {
	t := laneTable{}
	for _, n := range bulk {
		t[n] = LaneBulk
	}
	for _, n := range control { // a name on both lists is control: it cannot wait for credit
		t[n] = LaneControl
	}
	return t
}

// method is the lane of a request for method, and of its answer.
func (t laneTable) method(method string) Lane {
	if l, ok := t[method]; ok {
		return l
	}
	return LaneInteractive
}

// request is the route of a request for method; reply the route of its answer.
func (t laneTable) request(method string) route {
	l := t.method(method)
	return route{lane: l, credit: l == LaneInteractive || l == LaneBulk}
}

func (t laneTable) reply(method string) route {
	l := t.method(method)
	return route{lane: l, credit: l == LaneBulk}
}

// event is the lane of an event.
func (t laneTable) event(event string) Lane {
	if l, ok := t[event]; ok {
		return l
	}
	if event == cancelEvent {
		return LaneControl
	}
	return LaneEvents
}

// eventRoute is the route of an event.
func (t laneTable) eventRoute(event string) route {
	l := t.event(event)
	return route{lane: l, credit: l == LaneBulk}
}

// Frame sizes: a whole frame, header included, is at most frameMax.
const (
	frameHeader     = 16
	frameMax        = 32 << 10
	framePayloadMax = frameMax - frameHeader
)

const (
	frameData  byte = 'D'
	frameAck   byte = 'A'
	flagFin    byte = 1
	ackPayload      = 4
)

// clockStart and clockMS are this process's clock for acknowledgements: milliseconds since it started, wrapping
// after 49 days, from the monotonic clock (a stepped wall clock would read as a jump in delay). Only differences of
// two readings of the same clock, or of such differences, mean anything.
var clockStart = time.Now()

func clockMS(t time.Time) uint32 { return uint32(t.Sub(clockStart) / time.Millisecond) }

// frameHead is a frame's header.
type frameHead struct {
	kind byte
	lane Lane
	fin  bool
	slot uint16
	size int    // payload bytes
	seq  uint64 // D: this frame's number; A: the last one read
}

func putHead(b []byte, h frameHead) {
	_ = b[frameHeader-1]
	b[0], b[1], b[2], b[3] = h.kind, byte(h.lane), 0, 0
	if h.fin {
		b[2] = flagFin
	}
	binary.BigEndian.PutUint16(b[4:6], h.slot)
	binary.BigEndian.PutUint16(b[6:8], uint16(h.size))
	binary.BigEndian.PutUint64(b[8:16], h.seq)
}

// ErrFrame is what a framed connection ends with when the other side's bytes are not frames this version writes (its
// read, errors.Is): the two ends disagree about where frames are, or something between them changed the bytes.
var ErrFrame = errors.New("rpc: malformed frame")

// parseHead reads and checks a header: anything this version does not write is an error, so a connection whose two
// ends disagree about where frames are fails at once instead of reading garbage as lines.
func parseHead(b []byte) (frameHead, error) {
	_ = b[frameHeader-1]
	h := frameHead{kind: b[0], lane: Lane(b[1]), fin: b[2] == flagFin, slot: binary.BigEndian.Uint16(b[4:6]),
		size: int(binary.BigEndian.Uint16(b[6:8])), seq: binary.BigEndian.Uint64(b[8:16])}
	switch h.kind {
	case frameData:
		if h.lane >= laneCount || b[2]&^flagFin != 0 || b[3] != 0 || h.size > framePayloadMax {
			return h, fmt.Errorf("%w: data header % x", ErrFrame, b[:8])
		}
	case frameAck:
		if b[1]|b[2]|b[3]|b[4]|b[5] != 0 || h.size != ackPayload {
			return h, fmt.Errorf("%w: ack header % x", ErrFrame, b[:8])
		}
	default:
		return h, fmt.Errorf("%w: kind 0x%02x", ErrFrame, h.kind)
	}
	return h, nil
}
