package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The owner's messages that could not go in (#10194, docs/assistant/assistant.md "Messages that could not go in").
//
// The owner writes to one of the assistant's conversations while its process cannot start (a cold start killed on a
// loaded computer, the computer away). Nothing was typed, so the hub used to answer the app with the failure and
// forget the message; a replay of the app's operation then read as accepted and the bubble went. Now the hub hands
// such a message here (assistant.keep_undelivered) and answers the app that it is kept. The manager:
//   - keeps it in its Postgres (the setting undeliveredKey) with the app's own words and pictures, oldest first;
//   - shows it on the conversation's row (proto.Session.Undelivered), so every client draws it as the owner's
//     message marked not delivered, not only the one that sent it;
//   - sends it again by itself (undeliveredPass): with backoff, at once when the conversation is seen running again,
//     in order, under the message's own client_msg_id. The node sends an id once (sendsafe.go sentLog), and a try
//     whose outcome is in doubt is looked for in the conversation's latest turns before another (undeliveredLanded),
//     so it goes in exactly once, across manager restarts too;
//   - gives up after undeliveredGiveUp: the row then says failed and the owner picks Retry or Delete.
// A newer owner message to a conversation with kept ones is kept behind them (CodeUndeliveredAhead), never sent first.

// undeliveredKey is the setting the kept messages live in.
const undeliveredKey = "assistant.undelivered"

// undeliveredGiveUp is how long a kept message is tried before it waits for the owner (Retry / Delete).
var undeliveredGiveUp = 24 * time.Hour

// undeliveredBackoff is the wait before each try after a failed one; the last repeats.
var undeliveredBackoff = []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

// undeliveredTick is how often the kept messages are looked at without anything else waking the loop.
var undeliveredTick = 15 * time.Second

// undeliveredMaxBytes bounds one kept message (its words and pictures as the app sent them): a bigger one is not kept
// and the app gets the failure as before.
const undeliveredMaxBytes = 16 << 20

// keptMessage is one owner message kept for its conversation.
type keptMessage struct {
	ID      string         `json:"id"` // its client_msg_id
	Machine string         `json:"machine"`
	Session string         `json:"session"`
	Params  map[string]any `json:"params"` // the app's message as it came (text, images, launcher, model)
	Human   bool           `json:"human,omitempty"`
	Lang    string         `json:"lang,omitempty"`
	At      time.Time      `json:"at"`
	// State is proto.UndeliveredWaiting or proto.UndeliveredFailed.
	State    string    `json:"state"`
	Attempts int       `json:"attempts,omitempty"`
	NextAt   time.Time `json:"next_at"`
	// TriedAt: when the latest try that may have gone in began (its outcome in doubt, or the manager stopped during it);
	// zero when every try so far surely typed nothing.
	TriedAt time.Time `json:"tried_at,omitempty"`
	Error   string    `json:"error,omitempty"`
}

func (k keptMessage) text() string {
	t, _ := k.Params["text"].(string)
	return t
}

func (k keptMessage) images() int {
	n, _ := k.Params["images"].([]any)
	return len(n)
}

type undeliveredBook struct {
	mu      sync.Mutex
	loaded  bool
	st      map[string][]keptMessage // by conversation, oldest first
	sending map[string]bool          // conversations a pass is sending into
	wake    chan struct{}
	start   sync.Once
}

func (s *Service) undeliveredLocked(ctx context.Context) bool {
	b := &s.undelivered
	if b.loaded {
		return true
	}
	st := map[string][]keptMessage{}
	if store := s.store(); store != nil {
		if _, err := store.Setting(ctx, undeliveredKey, &st); err != nil {
			s.logf("load undelivered messages: %v", err)
			return false
		}
	}
	if st == nil {
		st = map[string][]keptMessage{}
	}
	b.st, b.loaded, b.sending = st, true, map[string]bool{}
	return true
}

func (s *Service) saveUndeliveredLocked() error {
	store := s.store()
	if store == nil {
		return nil
	}
	for id, q := range s.undelivered.st {
		if len(q) == 0 {
			delete(s.undelivered.st, id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return store.PutSetting(ctx, undeliveredKey, s.undelivered.st)
}

// KeepUndelivered is assistant.keep_undelivered: the hub could not deliver the owner's message p (nothing was typed),
// or SendParams kept it behind older ones. It is kept and sent again later; the same message kept twice (the app
// retried the same id) is kept once.
func (s *Service) KeepUndelivered(ctx context.Context, p assistantwire.KeepUndeliveredParams) (proto.UndeliveredMessage, error) {
	id, _ := p.Params["client_msg_id"].(string)
	if id == "" || p.SessionID == "" || p.MachineID == "" {
		return proto.UndeliveredMessage{}, &rpc.Error{Code: CodeBadRequest, Message: "client_msg_id, machine_id and session_id are required"}
	}
	if err := s.conversationAvailable(ctx, p.MachineID, p.SessionID); err != nil {
		return proto.UndeliveredMessage{}, err
	}
	params := map[string]any{}
	for _, k := range []string{"text", "images", "launcher", "model", "effort", "client_msg_id"} {
		if v, ok := p.Params[k]; ok {
			params[k] = v
		}
	}
	if raw, err := json.Marshal(params); err != nil || len(raw) > undeliveredMaxBytes {
		return proto.UndeliveredMessage{}, &rpc.Error{Code: CodeBadRequest, Message: "the message is too big to keep"}
	}
	lang := s.appLang(ctx, p.Lang)
	now := time.Now().UTC()
	b := &s.undelivered
	b.mu.Lock()
	if !s.undeliveredLocked(ctx) {
		b.mu.Unlock()
		return proto.UndeliveredMessage{}, errors.New("the undelivered messages could not be read")
	}
	for _, k := range b.st[p.SessionID] {
		if k.ID == id {
			b.mu.Unlock()
			return k.wire(), nil
		}
	}
	k := keptMessage{ID: id, Machine: p.MachineID, Session: p.SessionID, Params: params, Human: p.Human, Lang: string(lang),
		At: now, State: proto.UndeliveredWaiting, Attempts: 1, NextAt: now.Add(undeliveredBackoff[0]),
		Error: undeliveredReason(lang, p.Code)}
	if p.Code == assistantwire.CodeUndeliveredAhead {
		// never tried: it goes right after the ones ahead of it, and says so
		k.Attempts, k.NextAt, k.Error = 0, now, i18n.T(lang, "assistant-undelivered.behind")
	}
	if p.Code == assistantwire.CodeDeliveryUnknown {
		// The first attempt may have typed it. Always look before sending again, even if an older hub omitted the time.
		k.TriedAt = p.TriedAt
		if k.TriedAt.IsZero() {
			k.TriedAt = now
		}
	}
	b.st[p.SessionID] = append(b.st[p.SessionID], k)
	err := s.saveUndeliveredLocked()
	b.mu.Unlock()
	if err != nil {
		s.undeliveredDrop(p.SessionID, id) // not kept for sure: the app is told it failed, as before
		return proto.UndeliveredMessage{}, err
	}
	s.logf("undelivered: kept a message for %s (%d waiting)", short(p.SessionID), s.undeliveredCount(p.SessionID))
	s.publish(context.WithoutCancel(ctx))
	s.wakeUndelivered()
	return k.wire(), nil
}

// undeliveredReason is the line a kept message shows: why it did not go in.
func undeliveredReason(lang i18n.Lang, code string) string {
	if code == "machine_offline" || code == "node_closed" {
		return i18n.T(lang, "assistant-undelivered.computer_away")
	}
	return i18n.T(lang, "assistant-undelivered.not_started")
}

func (k keptMessage) wire() proto.UndeliveredMessage {
	m := proto.UndeliveredMessage{ID: k.ID, Text: k.text(), Images: k.images(), At: k.At, State: k.State, Attempts: k.Attempts, Error: k.Error}
	if k.State == proto.UndeliveredWaiting && !k.NextAt.IsZero() {
		next := k.NextAt
		m.NextAt = &next
	}
	return m
}

// undeliveredViews are the kept messages by conversation, for the rows (snapshot); false while they could not be read.
func (s *Service) undeliveredViews(ctx context.Context) (map[string][]keptMessage, bool) {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(ctx) {
		return nil, false
	}
	out := make(map[string][]keptMessage, len(b.st))
	for id, q := range b.st {
		out[id] = slices.Clone(q)
	}
	return out, true
}

func (s *Service) undeliveredCount(id string) int {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.st[id])
}

// undeliveredAhead: the owner's message id to conversation sid would overtake kept ones (any not given up on).
func (s *Service) undeliveredAhead(ctx context.Context, sid, id string) bool {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(ctx) {
		return false
	}
	for _, k := range b.st[sid] {
		if k.ID != id && k.State == proto.UndeliveredWaiting {
			return true
		}
	}
	return false
}

// keepsBehind: SendParams' message for p's conversation must wait behind kept ones (an owner's message with words or
// pictures and its own id; the node asking what its own message runs on carries neither).
func (s *Service) keepsBehind(ctx context.Context, p SendParams) bool {
	id, _ := p.Params["client_msg_id"].(string)
	_, text := p.Params["text"]
	_, images := p.Params["images"]
	return id != "" && (text || images) && s.undeliveredAhead(ctx, p.SessionID, id)
}

func (s *Service) undeliveredDrop(sid, id string) bool {
	b := &s.undelivered
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.undeliveredLocked(context.Background()) {
		return false
	}
	q := b.st[sid]
	i := slices.IndexFunc(q, func(k keptMessage) bool { return k.ID == id })
	if i < 0 {
		return false
	}
	b.st[sid] = slices.Delete(slices.Clone(q), i, i+1)
	if err := s.saveUndeliveredLocked(); err != nil {
		s.logf("save undelivered messages: %v", err)
	}
	return true
}

func (s *Service) wakeUndelivered() {
	b := &s.undelivered
	b.mu.Lock()
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	ch := b.wake
	b.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// undeliveredLoop sends the kept messages again until ctx ends.
func (s *Service) undeliveredLoop(ctx context.Context) {
	b := &s.undelivered
	b.mu.Lock()
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	wake := b.wake
	b.mu.Unlock()
	t := time.NewTicker(undeliveredTick)
	defer t.Stop()
	for {
		s.undeliveredPass(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
	}
}

// undeliveredSeen: a session list came in. A conversation with kept messages that runs again (its process is up, or
// it works) gets them now rather than at the next backoff step.
func (s *Service) undeliveredSeen(d DeviceSessions) {
	b := &s.undelivered
	b.mu.Lock()
	if !b.loaded || len(b.st) == 0 {
		b.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	woke := false
	for _, row := range d.Sessions {
		q := b.st[row.SessionID]
		if len(q) == 0 || row.Status == proto.StatusOffline || row.Status == "" {
			continue
		}
		for i := range q {
			if q[i].State == proto.UndeliveredWaiting && q[i].Machine == d.MachineID && q[i].NextAt.After(now) {
				q[i].NextAt, woke = now, true
			}
		}
	}
	b.mu.Unlock()
	if woke {
		s.wakeUndelivered()
	}
}

// undeliveredPass tries, for each conversation, its oldest kept message that is due, and the next ones after it while
// they go in. One pass per conversation at a time.
func (s *Service) undeliveredPass(ctx context.Context, now time.Time) {
	b := &s.undelivered
	b.mu.Lock()
	if !s.undeliveredLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var due []string
	for id, q := range b.st {
		if b.sending[id] {
			continue
		}
		for _, k := range q {
			if k.State != proto.UndeliveredWaiting {
				continue
			}
			if !k.NextAt.After(now) {
				due = append(due, id)
			}
			break // in order: the oldest waiting one decides
		}
	}
	for _, id := range due {
		b.sending[id] = true
	}
	b.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.undeliveredSend(ctx, id)
			b.mu.Lock()
			delete(b.sending, id)
			b.mu.Unlock()
		}()
	}
	wg.Wait()
}

// undeliveredSend sends conversation id's waiting messages in order until one does not go in.
func (s *Service) undeliveredSend(ctx context.Context, id string) {
	changed := false
	defer func() {
		if changed {
			s.publish(context.WithoutCancel(ctx))
		}
	}()
	for ctx.Err() == nil {
		b := &s.undelivered
		b.mu.Lock()
		var k keptMessage
		found := false
		for _, m := range b.st[id] {
			if m.State == proto.UndeliveredWaiting {
				k, found = m, true
				break
			}
		}
		if !found { // the pass found it due; after one goes in, the next follows at once
			b.mu.Unlock()
			return
		}
		if time.Since(k.At) > undeliveredGiveUp {
			s.updateKeptLocked(id, k.ID, func(m *keptMessage) { m.State = proto.UndeliveredFailed })
			b.mu.Unlock()
			changed = true
			continue // the next one has its own day
		}
		tried := time.Now().UTC()
		s.updateKeptLocked(id, k.ID, func(m *keptMessage) { m.TriedAt = tried }) // a restart during the try looks first
		b.mu.Unlock()
		err := s.deliverKept(ctx, k)
		b.mu.Lock()
		switch {
		case err == nil:
			s.dropKeptLocked(id, k.ID)
			s.logf("undelivered: sent a kept message to %s after %d tries", short(id), k.Attempts)
		case undeliveredGone(err):
			s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
				m.State, m.TriedAt, m.Error = proto.UndeliveredFailed, time.Time{}, i18n.T(i18n.Lang(m.Lang), "assistant-undelivered.gone")
			})
		default:
			doubt := relayInDoubt(err)
			s.updateKeptLocked(id, k.ID, func(m *keptMessage) {
				m.Attempts++
				m.NextAt = time.Now().UTC().Add(undeliveredBackoff[min(m.Attempts-1, len(undeliveredBackoff)-1)])
				m.Error = undeliveredReason(i18n.Lang(m.Lang), keptErrCode(err))
				if !doubt {
					m.TriedAt = time.Time{} // nothing typed: the next try need not look first
				}
			})
			s.logf("undelivered: %s still does not take a kept message (try %d): %v", short(id), k.Attempts+1, err)
		}
		b.mu.Unlock()
		changed = true
		if err != nil {
			return
		}
	}
}

func keptErrCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// undeliveredGone: the conversation will never take it (deleted, gone): nothing to try again.
func undeliveredGone(err error) bool {
	code := keptErrCode(err)
	return code == "conversation_deleted" || code == CodeBadRequest || strings.Contains(err.Error(), "is gone")
}

// updateKeptLocked changes kept message mid of conversation sid and saves; b.mu is held.
func (s *Service) updateKeptLocked(sid, mid string, f func(*keptMessage)) {
	q := s.undelivered.st[sid]
	for i := range q {
		if q[i].ID == mid {
			f(&q[i])
			if err := s.saveUndeliveredLocked(); err != nil {
				s.logf("save undelivered messages: %v", err)
			}
			return
		}
	}
}

func (s *Service) dropKeptLocked(sid, mid string) {
	q := s.undelivered.st[sid]
	if i := slices.IndexFunc(q, func(k keptMessage) bool { return k.ID == mid }); i >= 0 {
		s.undelivered.st[sid] = slices.Delete(slices.Clone(q), i, i+1)
		if err := s.saveUndeliveredLocked(); err != nil {
			s.logf("save undelivered messages: %v", err)
		}
	}
}

// deliverKept sends kept message k as the owner sent it: SendParams again (what it runs on now, the live snapshot of
// now), then the node's session.send under its own client_msg_id. A try that may have gone in before is looked for
// first.
func (s *Service) deliverKept(ctx context.Context, k keptMessage) error {
	if !k.TriedAt.IsZero() && s.undeliveredLanded(k) {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // a cold start on a busy computer can take minutes
	defer cancel()
	params, err := s.messageParams(withLauncherNoteDiscarded(sctx), SendParams{MachineID: k.Machine, SessionID: k.Session,
		Params: maps.Clone(k.Params), Lang: k.Lang, Human: k.Human}, true)
	if err != nil {
		return err
	}
	params["session_id"] = k.Session
	params["client_msg_id"] = k.ID
	tried := time.Now()
	err = s.call(sctx, k.Machine, DeviceSessionSend, params, nil)
	if err != nil && relayInDoubt(err) && s.undeliveredLandedSince(k, tried) {
		return nil
	}
	return err
}

// withLauncherNoteDiscarded: a resend has no app to tell that it ran on another launcher.
func withLauncherNoteDiscarded(ctx context.Context) context.Context {
	ctx, _ = withLauncherNote(ctx)
	return ctx
}

// undeliveredLanded says whether kept message k is in its conversation's latest turns already: an earlier try went in.
func (s *Service) undeliveredLanded(k keptMessage) bool {
	return s.undeliveredLandedSince(k, k.TriedAt)
}

// undeliveredLandedSince looks for k's line written no earlier than tried: tied to its client_msg_id, or, the node
// having forgotten the tie (it restarted), the owner's line with its words.
func (s *Service) undeliveredLandedSince(k keptMessage, tried time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), relayCheckWait)
	defer cancel()
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, k.Machine, DeviceSessionItems, map[string]any{"session_id": k.Session, "turns": 3, "lazy_images": true}, &res); err != nil {
		return false
	}
	want := relayWords(k.text())
	for _, it := range res.Items {
		if it.Role != "user" {
			continue
		}
		if it.ClientMsgID == k.ID {
			return true
		}
		if want != "" && it.ClientMsgID == "" && !it.TS.IsZero() && !it.TS.Before(tried.Add(-relayClockSlack)) && strings.Contains(relayWords(it.Text), want) {
			return true
		}
	}
	return false
}

// undeliveredRoutes serve the owner's Retry and Delete on a kept message.
func (s *Service) undeliveredRoutes() {
	find := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role"))
			return "", "", false
		}
		return r.PathValue("id"), r.PathValue("msg"), true
	}
	s.handle("POST /api/v1/assistant/conversations/{id}/undelivered/{msg}/retry", func(w http.ResponseWriter, r *http.Request) {
		sid, mid, ok := find(w, r)
		if !ok {
			return
		}
		b := &s.undelivered
		b.mu.Lock()
		found := false
		if s.undeliveredLocked(r.Context()) {
			for _, k := range b.st[sid] {
				if k.ID == mid {
					found = true
				}
			}
			now := time.Now().UTC()
			s.updateKeptLocked(sid, mid, func(m *keptMessage) {
				// A new day of tries from now; the line it may have written is still looked for first.
				m.State, m.At, m.NextAt, m.Attempts = proto.UndeliveredWaiting, now, now, 0
			})
		}
		b.mu.Unlock()
		if !found {
			apierr.Write(w, http.StatusNotFound, errors.New("no such message"))
			return
		}
		s.publish(context.WithoutCancel(r.Context()))
		s.wakeUndelivered()
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	s.handle("DELETE /api/v1/assistant/conversations/{id}/undelivered/{msg}", func(w http.ResponseWriter, r *http.Request) {
		sid, mid, ok := find(w, r)
		if !ok {
			return
		}
		b := &s.undelivered
		b.mu.Lock()
		busy := b.sending[sid]
		b.mu.Unlock()
		if busy {
			// It may be going in this moment: deleting it now could not take it back.
			apierr.Write(w, http.StatusConflict, errors.New(i18n.T(s.lang(r.Context()), "assistant-undelivered.sending_now")))
			return
		}
		if !s.undeliveredDrop(sid, mid) {
			apierr.Write(w, http.StatusNotFound, errors.New("no such message"))
			return
		}
		s.publish(context.WithoutCancel(r.Context()))
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
