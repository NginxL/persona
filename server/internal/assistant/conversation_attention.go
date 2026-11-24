package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Only the current main conversation's latest completed silent supervision
// round changes unread state. The audit transcript and full history stay intact.
const (
	conversationAttentionKey  = "assistant.conversation_attention.v2"
	conversationAttentionScan = 4
)

type conversationAttentionEntry struct {
	Rev        int64     `json:"rev"`
	Silent     bool      `json:"silent"`
	HasVisible bool      `json:"has_visible,omitempty"`
	VisibleAt  time.Time `json:"visible_at,omitzero"`
	Failed     bool      `json:"failed,omitempty"`
}

type conversationAttentionBook struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]conversationAttentionEntry

	routeOnce    sync.Once
	routeMain    string
	routeMachine string
}

func conversationAttentionCacheKey(machine, session string) string { return machine + "\x00" + session }

// mainConversationRoute reads the settings only before the first cached profile.
// Once a profile exists, the caller uses that fresher snapshot instead.
func (s *Service) mainConversationRoute() (string, string) {
	b := &s.convAttention
	b.routeOnce.Do(func() {
		cfg := s.settings(context.Background())
		b.routeMain, b.routeMachine = cfg.Main, cfg.mainRouteMachine()
	})
	return b.routeMain, b.routeMachine
}

func (s *Service) conversationAttentionCached(machine, session string, rev int64) (conversationAttentionEntry, bool) {
	if rev == 0 {
		return conversationAttentionEntry{}, false
	}
	b := &s.convAttention
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		b.entries = map[string]conversationAttentionEntry{}
		if st := s.store(); st != nil {
			if _, err := st.Setting(context.Background(), conversationAttentionKey, &b.entries); err != nil {
				s.logf("load conversation attention: %v", err)
				return conversationAttentionEntry{}, false
			}
		}
		b.loaded = true
	}
	e, ok := b.entries[conversationAttentionCacheKey(machine, session)]
	return e, ok && e.Rev == rev
}

func (s *Service) putConversationAttention(ctx context.Context, machine, session string, entry conversationAttentionEntry) {
	if entry.Rev == 0 {
		return
	}
	b := &s.convAttention
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		b.entries = map[string]conversationAttentionEntry{}
		if st := s.store(); st != nil {
			if _, err := st.Setting(ctx, conversationAttentionKey, &b.entries); err != nil {
				s.logf("load conversation attention: %v", err)
				return
			}
		}
		b.loaded = true
	}
	b.entries[conversationAttentionCacheKey(machine, session)] = entry
	if st := s.store(); st != nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(writeCtx, conversationAttentionKey, b.entries); err != nil {
			s.logf("save conversation attention: %v", err)
		}
	}
}

// failConversationAttention negatively caches one failed revision. The result
// stays unread, but a list refresh does not repeat the same failing RPC.
func (s *Service) failConversationAttention(machine, session string, rev int64) (conversationAttentionEntry, bool) {
	if rev != 0 {
		s.putConversationAttention(context.Background(), machine, session, conversationAttentionEntry{Rev: rev, Failed: true})
	}
	return conversationAttentionEntry{}, false
}

// finishConversationAttention caches only a revision the node still lists as
// the same completed turn. Input or a promotion observed during a paged read
// invalidates that read instead of letting its silent verdict hide new work.
func (s *Service) finishConversationAttention(machine, session string, rev int64, entry conversationAttentionEntry) (conversationAttentionEntry, bool) {
	if s.devs != nil {
		current, ok := s.nodeSession(machine, session)
		if !ok || current.Rev != rev || current.Starting || (current.Status != proto.StatusDone && current.Status != proto.StatusIdle) {
			return s.failConversationAttention(machine, session, rev)
		}
	}
	s.putConversationAttention(context.Background(), machine, session, entry)
	return entry, true
}

// mainConversationAttention classifies one revision. Older pages are read only
// while the newest round is silent and an earlier visible round has not been
// found. A failed or incomplete scan fails open as unread and is negatively
// cached only until the revision changes, never hidden as a silent success.
func (s *Service) mainConversationAttention(ctx context.Context, machine, session string, rev int64) (conversationAttentionEntry, bool) {
	if rev == 0 { // without a revision, a cached silent verdict could outlive a later genuine input
		return conversationAttentionEntry{}, false
	}
	if cached, ok := s.conversationAttentionCached(machine, session, rev); ok {
		if cached.Failed {
			return conversationAttentionEntry{}, false
		}
		return cached, true
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var all []proto.Item
	earliest := 0
	for range conversationAttentionScan {
		params := map[string]any{"session_id": session, "turns": 8, "lazy_images": true, "fold_work": true}
		if earliest > 0 {
			params["before_seq"] = earliest
		}
		var page struct {
			Items      []proto.Item `json:"items"`
			MoreBefore bool         `json:"more_before"`
			More       bool         `json:"more"`
			HasMore    bool         `json:"has_more"`
		}
		if err := s.call(ctx, machine, DeviceSessionItems, params, &page); err != nil {
			return s.failConversationAttention(machine, session, rev)
		}
		if len(page.Items) == 0 && len(all) == 0 {
			return s.failConversationAttention(machine, session, rev)
		}
		if len(page.Items) > 0 && page.Items[0].Seq == earliest && len(all) > 0 {
			return s.failConversationAttention(machine, session, rev) // no paging progress
		}
		all = append(append([]proto.Item(nil), page.Items...), all...)
		rounds := transcript.SupervisionRoundVisibilities(all)
		silentLatest := false
		if len(rounds) == 0 {
			if len(page.Items) == 0 || !(page.MoreBefore || page.More || page.HasMore || earliest > 0) {
				return s.failConversationAttention(machine, session, rev)
			}
		} else {
			latest := rounds[len(rounds)-1]
			if latest.Silent && !latest.Closed {
				// Open is a state, not a failed RPC: it can become closed on
				// the next list with the same revision and must remain re-read.
				return conversationAttentionEntry{}, false
			}
			if !latest.Silent {
				entry := conversationAttentionEntry{Rev: rev, HasVisible: true, VisibleAt: latest.End}
				return s.finishConversationAttention(machine, session, rev, entry)
			}
			silentLatest = true
			for i := len(rounds) - 2; i >= 0; i-- {
				if !rounds[i].Silent {
					entry := conversationAttentionEntry{Rev: rev, Silent: true, HasVisible: true, VisibleAt: rounds[i].End}
					return s.finishConversationAttention(machine, session, rev, entry)
				}
			}
		}
		firstSeq := 0
		if len(all) > 0 {
			firstSeq = all[0].Seq
		}
		more := page.MoreBefore || page.More || page.HasMore
		if !more || firstSeq <= 1 && firstSeq != 0 {
			if !silentLatest {
				return s.failConversationAttention(machine, session, rev)
			}
			entry := conversationAttentionEntry{Rev: rev, Silent: true}
			return s.finishConversationAttention(machine, session, rev, entry)
		}
		if len(page.Items) == 0 {
			return s.failConversationAttention(machine, session, rev) // the page said more history existed but made no progress
		}
		earliest = page.Items[0].Seq
		if earliest <= 0 {
			return s.failConversationAttention(machine, session, rev)
		}
	}
	return s.failConversationAttention(machine, session, rev)
}

// refreshMainConversationAttention computes one newly seen revision once, not
// once per inventory decoration. It is called from setSessions after a list
// arrives, so the inventory only reads the resulting classification.
func (s *Service) refreshMainConversationAttention(sess proto.Session, mainSession, mainMachine string) {
	if sess.Rev == 0 || sess.Starting || (sess.Status != proto.StatusDone && sess.Status != proto.StatusIdle) {
		return
	}
	if sess.SessionID != mainSession || sess.MachineID != mainMachine {
		return
	}
	if _, ok := s.conversationAttentionCached(sess.MachineID, sess.SessionID, sess.Rev); ok {
		return
	}
	_, _ = s.mainConversationAttention(context.Background(), sess.MachineID, sess.SessionID, sess.Rev)
}

func mainConversationAttentionCandidate(assistant bool, status, state string, starting bool, pending int) bool {
	return assistant && status == proto.StatusDone && !starting &&
		(state == proto.StateUnread || state == proto.StateIdle) && pending == 0
}

// applyMainConversationAttention reads only the classification already computed
// by setSessions. It never invokes the transcript scanner on the inventory's
// hot path; a cache miss stays unread rather than guessing.
func (s *Service) applyMainConversationAttention(row *proto.Session, mainSession, mainMachine string) {
	if row.SessionID != mainSession || row.MachineID != mainMachine {
		return
	}
	entry, ok := s.conversationAttentionCached(row.MachineID, row.SessionID, row.Rev)
	if !ok || !entry.Silent {
		return
	}
	if entry.HasVisible && (row.SeenAt.IsZero() || entry.VisibleAt.IsZero() || row.SeenAt.Before(entry.VisibleAt.Add(-2*time.Second))) {
		row.State = proto.StateUnread
		return
	}
	row.State = proto.StateIdle
}

// decorateMainConversationAttention preserves working and needs-you first. A
// silent round clears only its own unread mark; an earlier visible round remains
// unread until seen_at reaches that round's end.
func (s *Service) decorateMainConversationAttention(row *proto.Session, cfg Settings) {
	s.decorateMainConversationAttentionForRoute(row, cfg.Main, cfg.mainRouteMachine())
}

func (s *Service) decorateMainConversationAttentionForRoute(row *proto.Session, mainSession, mainMachine string) {
	if !mainConversationAttentionCandidate(row.Assistant, row.Status, row.State, row.Starting, row.PendingInteractions) || row.SessionID != mainSession || row.MachineID != mainMachine {
		return
	}
	row.State = s.mainConversationAttentionState(row.MachineID, row.SessionID, row.State, row.Rev, row.SeenAt)
}

// mainConversationAttentionState takes only the fields the classification
// needs. In particular, the fleet inventory's row storage must not escape into
// the scanner's RPC calls: those calls retain their session argument.
func (s *Service) mainConversationAttentionState(machine, session, state string, rev int64, seenAt time.Time) string {
	if rev == 0 {
		// A revision-less row has no durable disposition to classify. Keep its
		// current state; in particular, never resurrect an already-seen row.
		return state
	}
	if state == proto.StateIdle {
		// Only a previously silent verdict needs reclassification while idle:
		// its revision may now carry a promotion. An already-seen ordinary row
		// (or one with no verdict yet) is not reopened by a history read.
		cached, ok := s.conversationAttentionCached(machine, session, rev)
		if !ok || !cached.Silent {
			return state
		}
	}
	entry, ok := s.mainConversationAttention(context.Background(), machine, session, rev)
	if !ok {
		return state // an unread row fails open; an already-seen row is not resurrected on a read error
	}
	// A non-silent entry is the latest visible round. For a silent entry it is
	// the nearest earlier visible round; without one, only the silent round is
	// retired and there is no unread work to preserve.
	if !entry.HasVisible {
		return proto.StateIdle
	}
	if seenAt.IsZero() || entry.VisibleAt.IsZero() || seenAt.Before(entry.VisibleAt.Add(-2*time.Second)) {
		return proto.StateUnread
	}
	return proto.StateIdle
}
