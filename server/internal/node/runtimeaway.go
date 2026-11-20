package node

import (
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A runtime that runs Claude Code through its SDK (Paseo, docs/integrations/paseo.md) has sessions whose conversation
// is a Claude transcript on this machine (runtimetranscript.go). When it leaves the node (Paseo stopped for more than
// half a minute, the computer switched to another engine), those conversations are still here: the rows of its last
// list whose conversation is a transcript here stay listed, under their own ids, as sessions not running, for
// unlistedKeep, as what it named unlisted does (runtimeunlisted.go). Their content is the transcript's, which the node
// reads; what needs the runtime (a message, a rename, closing it) is refused as away (errRuntimeAway: nothing sent).
// The runtime's first list once it is back replaces them. Before, they left with it: the transcripts Paseo writes read
// as a script's one-shot runs (claude_oneshot.go), which the offline list leaves out, and the node refused to read them
// while the runtime was away (#8131). A node that starts while the runtime is away knows none of them until it is back.

// awayOf is what a runtime that left still lists: its rows whose conversation is a transcript here, as not running
// (awayRow), the transcript of each by session id, and when its last connection left.
type awayOf struct {
	rows        []proto.Session
	transcripts map[string]string
	gone        time.Time
}

// leftRows keeps h's rows whose conversation is a Claude transcript here, as rows not running, once runtime id has no
// connection any more. Not for a daemon: its transcripts are not on this machine. Callers hold b.mu.
func (b *runtimeRegistry) leftRows(h *runtimeConn, id string) {
	if id == "" {
		return
	}
	for _, o := range b.conns {
		if o.id() == id {
			return // another connection of the same runtime (it reconnected before the old one was gone) lists them
		}
	}
	h.mu.Lock()
	var a awayOf
	if h.daemon == "" {
		for _, s := range h.sessions {
			if t := h.transcripts[s.SessionID]; t != "" {
				if a.transcripts == nil {
					a.transcripts = map[string]string{}
				}
				a.rows, a.transcripts[s.SessionID] = append(a.rows, awayRow(s)), t
			}
		}
	}
	h.mu.Unlock()
	if len(a.rows) == 0 {
		delete(b.away, id)
		return
	}
	a.gone = time.Now()
	if b.away == nil {
		b.away = map[string]awayOf{}
	}
	b.away[id] = a
}

// awayRow is a row of a runtime that left as the node keeps listing it: not running, without the work the runtime
// said it ran (queued messages, the agents it created) or a takeover it had under way (starting, or failed: the
// conversation is the transcript's, #8130), and with none of its controls but reading and sending (a message is
// refused as away, which says why); a child that waited for the user is under its creator again.
func awayRow(s proto.Session) proto.Session {
	s.Status, s.Queued, s.Background = proto.StatusOffline, nil, nil
	s.Starting, s.StartError = false, ""
	if p := s.AskingParentSessionID(); p != "" {
		s.SetAskingParentSessionID("")
		s.IsSubagent, s.ParentSessionID = true, p
	}
	if c := s.Capabilities; c != nil {
		cp := *c
		cp.SupervisionPolicy = false
		cp.Fork = false
		cp.Interrupt, cp.Close, cp.Rename, cp.Modes, cp.Model = false, proto.CanModes(false), proto.CanModes(false), proto.CanModes(false), proto.CanModes(false)
		cp.Rewind, cp.RewindCode = proto.CanModes(false), proto.CanModes(false)
		cp.QueueEdit, cp.QueueSendNow = cp.Close, cp.Close
		s.Capabilities = &cp
	}
	return s
}

// backAgain drops what runtime id kept listing while it was away: its list, now that it is back, says what it has.
// Callers hold b.mu.
func (b *runtimeRegistry) backAgain(id string) {
	delete(b.away, id)
}

// awayNow is the runtimes that left within unlistedKeep, by id, in order; those that left longer ago are forgotten.
// Callers hold b.mu.
func (b *runtimeRegistry) awayNow() []string {
	ids := make([]string, 0, len(b.away))
	for id, a := range b.away {
		if time.Since(a.gone) > unlistedKeep {
			delete(b.away, id)
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// appendAway appends the rows of the runtimes that left (awayNow) that no connection lists, each id once. Callers
// hold b.mu.
func (b *runtimeRegistry) appendAway(dst []proto.Session) []proto.Session {
	if len(b.away) == 0 {
		return dst
	}
	var seen map[string]bool
	for _, id := range b.awayNow() {
		for i := range b.away[id].rows {
			sid := b.away[id].rows[i].SessionID
			if b.owner[sid] != nil || seen[sid] {
				continue
			}
			if seen == nil {
				seen = map[string]bool{}
			}
			seen[sid] = true
			dst = append(dst, b.away[id].rows[i])
		}
	}
	return dst
}

// awayTranscript is the transcript of session id when it is a row of a runtime that left (appendAway), "" for any
// other. Callers hold b.mu.
func (b *runtimeRegistry) awayTranscript(id string) string {
	for _, a := range b.away { // rarely more than one: the order matters only for an id two runtimes listed
		if t := a.transcripts[id]; t != "" && time.Since(a.gone) <= unlistedKeep {
			return t
		}
	}
	return ""
}
