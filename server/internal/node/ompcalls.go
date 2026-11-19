package node

// What a stopped omp session (an omp disk row, ompdisk.go) answers: a notice in place of the conversation, and no
// sending — until the node can resume it in herdr.

import (
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ompOfflineRefusal is a send to it: the node knows its file but does not resume it yet. The text is the node's own
// words (the refusal's detail); clients get rpc-errors.omp_resume_unknown in their language (apierr).
const ompOfflineRefusal = "这个 omp 会话已停止，Persona 还不能恢复它：请在电脑上用 omp --resume 接着它。这次没有发送。"

// ompOfflineMethods are the calls ompOfflineCalls answers. Only these look a session up among the stopped ones, so
// the other calls never wait on a scan of omp's session files.
var ompOfflineMethods = map[string]bool{
	"session.items": true, "session.item": true, "session.image": true, "session.visualization": true,
	"session.watch": true, "session.unwatch": true, "session.send": true,
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
// conversation saying it is stopped, and a send refused (the node does not resume from its file yet). Terminal
// calls are not this branch's: a stopped session has no terminal.
func (n *Node) ompOfflineCalls(method, id string) (any, error, bool) {
	switch method {
	case "session.items":
		return map[string]any{"items": []proto.Item{{ID: "omp#0", Seq: 1, Role: "system", Kind: "notice",
			Text: transcript.OmpStoppedNotice, TS: time.Now().UTC()}}, "has_more": false, "history": "omp-disk:" + id, "to_seq": 1}, nil, true
	case "session.item", "session.image", "session.visualization":
		return nil, fmt.Errorf("no item"), true
	case "session.watch", "session.unwatch":
		return okResult(nil), nil, true
	case "session.send":
		return nil, refusal("omp_resume_unknown", ompOfflineRefusal, nil), true
	}
	return nil, nil, false
}
