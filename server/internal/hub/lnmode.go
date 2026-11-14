package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Low-distraction ("ln") mode: when its switch is on, the hub delivers pushes by urgency instead of pushing
// everything. P0 still interrupts, P1 is held for the "Needs you" list / briefings, P2 stays silent, and the same
// related key may interrupt at most once in 12 hours. Every push the switch holds back is recorded with its reason,
// in memory and in hub_settings, behind a read-only API for extensions and the nightly review. With the switch off
// (the default) every push goes out exactly as before; the filter is a no-op.

const (
	lnModeSettingKey       = "hub.ln_mode"
	lnInterceptsSettingKey = "hub.ln_intercepts"

	// lnDedupWindow: how long the same related key stays quiet after it pushed once.
	lnDedupWindow = 12 * time.Hour
	// lnConfigTTL bounds how often one Pusher asks the database for the switch while pushes keep coming.
	lnConfigTTL = 15 * time.Second
	// lnInterceptMax bounds how many held pushes are kept; the oldest go first.
	lnInterceptMax = 500
	// lnInterceptAge: held pushes older than this are not worth showing any more.
	lnInterceptAge      = 14 * 24 * time.Hour
	lnInterceptLimitMax = 200
)

// Why a push was held back.
const (
	interceptReasonP1    = "ln_p1_held"   // P1 while ln mode is on: kept for the list / briefing
	interceptReasonP2    = "ln_p2_silent" // P2 while ln mode is on: silent
	interceptReasonDedup = "ln_dedup_12h" // the same related key pushed within the last 12 hours
)

// pushIntercept is one push ln mode held back, as the read-only API returns it.
type pushIntercept struct {
	At         time.Time `json:"at"`
	Level      string    `json:"level"`
	Reason     string    `json:"reason"`
	Title      string    `json:"title,omitempty"`
	Body       string    `json:"body,omitempty"`
	RelatedKey string    `json:"related_key,omitempty"`
	Manager    string    `json:"manager,omitempty"`
	// Also holds the titles of P1 conclusion pushes that merged into this one: several conclusion cards arriving in
	// the same minute become one intercepted entry instead of a stack.
	Also []string `json:"also,omitempty"`
}

// pushPolicy says how urgent one alert push is and what it belongs to. The zero value is P1 with no related key.
type pushPolicy struct {
	level      string
	relatedKey string
	title      string
	body       string
	manager    string
}

func (p pushPolicy) levelOr(level string) pushPolicy {
	if p.level == "" {
		p.level = level
	}
	return p
}

// levelFor derives the urgency of a manager notification that named no level itself, from its kind.
func levelFor(kind, given string) string {
	if given != "" {
		return given
	}
	switch kind {
	case assistantwire.NotifyInput, assistantwire.NotifyDecide, assistantwire.NotifyReply:
		// A card or a decision: something only the owner can answer now.
		return assistantwire.LevelP0
	case assistantwire.NotifyDone:
		// A turn that finished: background news.
		return assistantwire.LevelP2
	default:
		// notify_user and the assistant's own alerts: worth knowing, not urgent.
		return assistantwire.LevelP1
	}
}

// lnModeOn says whether the switch is on. It never waits on the database: the switch is read once at startup
// (loadLnState), written through setLnMode, and refreshed in the background when the cached answer is older than
// lnConfigTTL (another hub process may have changed it). A database that does not answer keeps the last known answer,
// off before the first successful read, so an outage never turns filtering on by itself.
func (p *Pusher) lnModeOn() bool {
	if p == nil {
		return false
	}
	p.lnMu.Lock()
	defer p.lnMu.Unlock()
	if p.store != nil && !p.lnRefreshing && (p.lnCached == nil || p.now().Sub(p.lnCachedAt) >= lnConfigTTL) {
		p.lnRefreshing = true
		go p.refreshLnMode(p.lnVersion)
	}
	return p.lnCached != nil && *p.lnCached
}

// refreshLnMode reads the switch from hub_settings into the cache (lnModeOn starts it, one at a time). version is
// lnVersion when it started: a setLnMode meanwhile wins over what this read saw.
func (p *Pusher) refreshLnMode(version uint64) {
	enabled, ok := p.readLnMode()
	p.lnMu.Lock()
	defer p.lnMu.Unlock()
	p.lnRefreshing = false
	if version != p.lnVersion {
		return
	}
	if ok {
		p.lnCached, p.lnCachedAt = &enabled, p.now()
	} else if p.lnCached != nil {
		p.lnCachedAt = p.now() // keep the last answer; try again after another TTL
	}
}

// readLnMode reads the switch; ok false when the database did not answer. No row is off.
func (p *Pusher) readLnMode() (enabled, ok bool) {
	if p.store == nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
	defer cancel()
	found, err := p.store.Setting(ctx, lnModeSettingKey, &enabled)
	if err != nil {
		return false, false
	}
	return found && enabled, true
}

// setLnMode writes the switch (hub_settings) and refreshes the cache immediately.
func (p *Pusher) setLnMode(ctx context.Context, enabled bool) error {
	if p == nil || p.store == nil {
		return errors.New("push settings are not available")
	}
	ctx, cancel := context.WithTimeout(ctx, p.storeTimeout)
	defer cancel()
	if err := p.store.PutSetting(ctx, lnModeSettingKey, enabled); err != nil {
		return err
	}
	p.lnMu.Lock()
	p.lnCached, p.lnCachedAt = &enabled, p.now()
	p.lnVersion++
	p.lnMu.Unlock()
	return nil
}

// applyLn decides whether one alert push may go out. With the switch off it allows everything and touches nothing.
// It runs once per push call, never per device: a held push is one intercepted entry however many devices would have
// received it. The 12-hour check and the note of a P0 that goes out happen under one hold of lnMu, so two pushes with
// the same key at the same moment cannot both pass.
func (p *Pusher) applyLn(policy pushPolicy) (allow bool, reason string) {
	if !p.lnModeOn() {
		return true, ""
	}
	level := policy.level
	if level == "" {
		level = assistantwire.LevelP1
	}
	now := p.now()
	key := strings.TrimSpace(policy.relatedKey)
	in := pushIntercept{At: now.UTC(), Level: level, Title: policy.title, Body: policy.body, RelatedKey: key, Manager: policy.manager}
	p.lnMu.Lock()
	if key != "" {
		if last, seen := p.lnLastPush[key]; seen && now.Sub(last) < lnDedupWindow {
			in.Reason = interceptReasonDedup
			p.addInterceptLocked(in, false)
			p.lnMu.Unlock()
			return false, interceptReasonDedup
		}
	}
	switch level {
	case assistantwire.LevelP0:
		// P0 interrupts; its key then stays quiet for the window.
		if key != "" {
			p.noteKeyPushedLocked(key, now)
		}
		p.lnMu.Unlock()
		return true, ""
	case assistantwire.LevelP2:
		in.Reason = interceptReasonP2
		p.addInterceptLocked(in, false)
		p.lnMu.Unlock()
		return false, interceptReasonP2
	default: // P1 and anything unknown: held for the list and the briefing rather than alerted.
		in.Level, in.Reason = assistantwire.LevelP1, interceptReasonP1
		p.addInterceptLocked(in, true)
		p.lnMu.Unlock()
		return false, interceptReasonP1
	}
}

// noteKeyPushedLocked remembers that key alerted at now, and forgets keys whose window is over (card keys are unique,
// so the map would otherwise only grow). lnMu is held.
func (p *Pusher) noteKeyPushedLocked(key string, now time.Time) {
	if p.lnLastPush == nil {
		p.lnLastPush = map[string]time.Time{}
	}
	for k, at := range p.lnLastPush {
		if now.Sub(at) >= lnDedupWindow {
			delete(p.lnLastPush, k)
		}
	}
	p.lnLastPush[key] = now
}

// addInterceptLocked keeps one held push and schedules its save. merge (P1): it folds into a recent P1 entry of the
// same manager opened in the same minute, or about the same related key within the 12-hour window, so several
// conclusion cards arriving together, or three cards about one PR, become one entry. Entries past the age and count
// limits go. lnMu is held.
func (p *Pusher) addInterceptLocked(in pushIntercept, merge bool) {
	if merge {
		minute := in.At.Truncate(time.Minute)
		for i := len(p.intercepts) - 1; i >= 0 && i >= len(p.intercepts)-64; i-- {
			e := &p.intercepts[i]
			if e.Reason != interceptReasonP1 || e.Manager != in.Manager {
				continue
			}
			sameMinute := e.At.Truncate(time.Minute).Equal(minute)
			sameKey := in.RelatedKey != "" && e.RelatedKey == in.RelatedKey && in.At.Sub(e.At) < lnDedupWindow
			if sameMinute || sameKey {
				if in.Title != "" {
					e.Also = append(slices.Clip(e.Also), in.Title)
				}
				p.scheduleSaveLocked()
				return
			}
		}
	}
	cutoff := p.now().Add(-lnInterceptAge)
	kept := p.intercepts[:0]
	for _, e := range p.intercepts {
		if e.At.After(cutoff) {
			kept = append(kept, e)
		}
	}
	p.intercepts = append(kept, in)
	if len(p.intercepts) > lnInterceptMax {
		p.intercepts = slices.Clone(p.intercepts[len(p.intercepts)-lnInterceptMax:])
	}
	p.scheduleSaveLocked()
}

// recordIntercept keeps one held push (tests and callers outside applyLn).
func (p *Pusher) recordIntercept(in pushIntercept) {
	p.lnMu.Lock()
	p.addInterceptLocked(in, false)
	p.lnMu.Unlock()
}

// scheduleSaveLocked marks the list changed and starts the one writer if it is not running. The writer saves the
// latest list, then again while it changed meanwhile, so saves never land out of order and a burst of held pushes is
// one or two writes, off the push path. lnMu is held.
func (p *Pusher) scheduleSaveLocked() {
	if p.store == nil {
		return
	}
	p.lnDirty = true
	if !p.lnWriting {
		p.lnWriting = true
		go p.saveIntercepts()
	}
}

func (p *Pusher) saveIntercepts() {
	for {
		p.lnMu.Lock()
		if !p.lnDirty {
			p.lnWriting = false
			p.lnMu.Unlock()
			return
		}
		p.lnDirty = false
		list := cloneIntercepts(p.intercepts)
		p.lnMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
		if err := p.store.PutSetting(ctx, lnInterceptsSettingKey, list); err != nil {
			p.logf("ln mode: save held pushes: %v", err)
		}
		cancel()
	}
}

// waitInterceptsSaved waits (up to d) for the writer to finish; true when nothing is left to save.
func (p *Pusher) waitInterceptsSaved(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		p.lnMu.Lock()
		idle := !p.lnWriting && !p.lnDirty
		p.lnMu.Unlock()
		if idle {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func cloneIntercepts(in []pushIntercept) []pushIntercept {
	out := make([]pushIntercept, len(in))
	for i, e := range in {
		e.Also = slices.Clone(e.Also)
		out[i] = e
	}
	return out
}

// loadLnState reads the switch and the held-push list once at startup; an absent or unreadable row is off / empty.
func (p *Pusher) loadLnState() {
	if p == nil || p.store == nil {
		return
	}
	if enabled, ok := p.readLnMode(); ok {
		p.lnMu.Lock()
		p.lnCached, p.lnCachedAt = &enabled, p.now()
		p.lnMu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.storeTimeout)
	defer cancel()
	var stored []pushIntercept
	if ok, err := p.store.Setting(ctx, lnInterceptsSettingKey, &stored); err != nil || !ok {
		return
	}
	cutoff := p.now().Add(-lnInterceptAge)
	p.lnMu.Lock()
	p.intercepts = p.intercepts[:0]
	for _, e := range stored {
		if e.At.After(cutoff) {
			p.intercepts = append(p.intercepts, e)
		}
	}
	p.lnMu.Unlock()
}

// lnInterceptsView returns the held pushes newest first, at most limit (clamped to lnInterceptLimitMax).
func (p *Pusher) lnInterceptsView(limit int) []pushIntercept {
	if limit <= 0 || limit > lnInterceptLimitMax {
		limit = lnInterceptLimitMax
	}
	p.lnMu.Lock()
	defer p.lnMu.Unlock()
	out := make([]pushIntercept, 0, min(limit, len(p.intercepts)))
	for i := len(p.intercepts) - 1; i >= 0 && len(out) < limit; i-- {
		e := p.intercepts[i]
		e.Also = slices.Clone(e.Also)
		out = append(out, e)
	}
	return out
}

// lnRoutes serves the switch and the read-only list of held pushes. The routes sit behind the standard API gate; an
// extension token may read the intercepts (the same content its pushes carry) but not change the switch.
func (h *Hub) lnRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/push/ln-mode", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"enabled": h.push.lnModeOn()})
	})
	api.HandleFunc("PUT /api/v1/push/ln-mode", func(w http.ResponseWriter, r *http.Request) {
		if extensionToken(r) {
			writeErr(w, http.StatusForbidden, errors.New("the ln mode switch is for the hub's own apps"))
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSONBody(w, r, &body, 1<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if err := h.push.setLnMode(r.Context(), body.Enabled); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, map[string]any{"enabled": body.Enabled})
	})
	api.HandleFunc("GET /api/v1/push/intercepts", func(w http.ResponseWriter, r *http.Request) {
		view := h.push.lnInterceptsView(0)
		// An extension token reads only its own manager's held pushes; a full credential reads every manager's.
		if em := extensionManager(r); em != "" {
			view = slices.DeleteFunc(slices.Clone(view), func(e pushIntercept) bool { return e.Manager != em })
		}
		writeJSON(w, map[string]any{"intercepts": view})
	})
}
