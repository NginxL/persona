package node

// What a stopped omp session (an omp disk row, ompdisk.go) answers: a notice in place of the conversation, and a
// message that resumes it in herdr (ompresume.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ompStoppedRefusal is a call a stopped session has nothing to answer with (no terminal, nothing running). The text
// is the node's own words (the refusal's detail); clients get rpc-errors.omp_stopped in their language (apierr).
const ompStoppedRefusal = "这个 omp 会话已停止，没有这个操作：发一条消息会在电脑上接着它运行。"

// ompOfflineMethods are the calls ompOfflineCalls answers. Only these look a session up among the stopped ones, so
// the other calls never wait on a scan of omp's session files.
var ompOfflineMethods = map[string]bool{
	"session.items": true, "session.item": true, "session.image": true, "session.visualization": true,
	"session.watch": true, "session.unwatch": true, "session.send": true,
	"session.interrupt": true, "session.modes": true, "session.queue.edit": true, "session.queue.send_now": true,
	"session.rename": true, "session.close": true,
}

// ompStopped: a call ompOfflineCalls answers, about a stopped omp session (ompdisk.go).
func (n *Node) ompStopped(method, id string) bool {
	if !ompOfflineMethods[method] {
		return false
	}
	_, ok := n.ompSession(id)
	return ok
}

// ompOfflineCalls answers the calls only a reader of the conversation could, for a stopped session: an empty
// conversation saying it is stopped, and a send that resumes it in herdr. Terminal calls are not this branch's: a
// stopped session has no terminal.
func (n *Node) ompOfflineCalls(ctx context.Context, method, id string, params json.RawMessage) (any, error, bool) {
	switch method {
	case "session.items":
		return map[string]any{"items": []proto.Item{{ID: "omp#0", Seq: 1, Role: "system", Kind: "notice",
			Text: transcript.OmpStoppedNotice, TS: time.Now().UTC()}}, "has_more": false, "history": "omp-disk:" + id, "to_seq": 1}, nil, true
	case "session.item", "session.image", "session.visualization":
		return nil, fmt.Errorf("no item"), true
	case "session.watch", "session.unwatch":
		return okResult(nil), nil, true
	case "session.send": // resumed in herdr, then the message goes there (ompresume.go)
		// One resume at a time: a second send (a quick double tap, two phones) waits for the first and goes to the omp
		// it started, rather than opening a second tab with another omp on the same session file.
		release := n.recovery.lock(id)
		defer release()
		if r, owner, ok := n.runtimes.rowOf(id); ok && owner != nil && r.Status != proto.StatusOffline {
			res, err, _ := n.runtimeHandle(ctx, owner, "session.send", params)
			return res, err, true
		}
		res, err := n.resumeOmp(ctx, id, params)
		return res, err, true
	case "session.interrupt", "session.modes", "session.queue.edit", "session.queue.send_now", "session.rename", "session.close":
		// A stopped session has no terminal and nothing to rename or stop; a send resumes it.
		return nil, refusal("omp_stopped", ompStoppedRefusal, nil), true
	}
	return nil, nil, false
}
