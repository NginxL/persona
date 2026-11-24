package assistant

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Attention modes (#13503, docs/server/server.md "Attention modes"): the user sets a mode for a while (fragment,
// immersive, quiet), and it reinterprets the push levels until it ends by itself and is normal again. This manager
// keeps the mode (assistant.attention) and reports it to the hub in its profile (assistantwire.Profile.Attention); the
// hub's push filter reads it there (hub/push_attention.go, assistantwire.DecideAttention). Here, a mode other than
// normal counts as the low-distraction switch being on (push_quiet.go quietModeOn), immersive mode takes every
// question an agent reports to its supervisor as one that can wait (agent_control.go). A track's push_cap is not the
// mode's business: track_attention.go holds every push to it, once, in pushResult and deliver. The mode never touches
// hub.ln_mode, so when it ends the switch is what it was.

const attentionSettingsKey = "assistant.attention"

// attentionCacheTTL bounds how often the mode is read from the database while something keeps asking (quietModeOn
// runs on every card's tick); a change made here refreshes it at once.
const attentionCacheTTL = 15 * time.Second

// attentionIdle is how long attentionLoop sleeps with no mode to end.
const attentionIdle = time.Hour

// attentionClock is the mode as last read, and what wakes attentionLoop.
type attentionClock struct {
	mu       sync.Mutex
	cached   *assistantwire.Attention
	cachedAt time.Time
	// gen counts putAttention's writes: a read that began before one does not cache what it read (it may be older
	// than the write).
	gen  uint64
	wake chan struct{}
	// now replaces time.Now (tests).
	now func() time.Time
}

func (s *Service) attentionTime() time.Time {
	c := &s.attention
	c.mu.Lock()
	now := c.now
	c.mu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

// attentionWake is the channel that wakes attentionLoop (made on first use).
func (s *Service) attentionWake() chan struct{} {
	c := &s.attention
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wake == nil {
		c.wake = make(chan struct{}, 1)
	}
	return c.wake
}

// attentionStored is the mode as stored, normal before the user ever set one or when the store cannot be read (a
// mode is never turned on by an outage).
func (s *Service) attentionStored(ctx context.Context) assistantwire.Attention {
	c := &s.attention
	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < attentionCacheTTL {
		a := *c.cached
		c.mu.Unlock()
		return a
	}
	gen := c.gen
	c.mu.Unlock()
	a := assistantwire.Attention{Mode: assistantwire.AttentionNormal}
	if s.store == nil {
		return a
	}
	st := s.store()
	if st == nil {
		return a
	}
	var stored assistantwire.Attention
	found, err := st.Setting(ctx, attentionSettingsKey, &stored)
	if err != nil {
		return a
	}
	if found && assistantwire.KnownAttentionMode(stored.Mode) {
		a = stored
	}
	c.remember(gen, a)
	return a
}

// remember caches a, read from the store when the write count was gen, unless putAttention wrote since: then what
// was read may be older than the cache, which the write already refreshed.
func (c *attentionClock) remember(gen uint64, a assistantwire.Attention) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.cached, c.cachedAt = &a, time.Now()
	}
}

// attentionNow is the mode in force now: normal once the stored one's time is over.
func (s *Service) attentionNow(ctx context.Context) assistantwire.Attention {
	a := s.attentionStored(ctx)
	return a.Effective(s.attentionTime())
}

// attentionProfile is the mode for the profile the hub reads: the stored one while it is in force, nil when normal.
func (s *Service) attentionProfile(ctx context.Context) *assistantwire.Attention {
	a := s.attentionNow(ctx)
	if !a.Active() {
		return nil
	}
	return &a
}

// attentionDefersQuestions: immersive mode is in force, so a question an agent reports to its supervisor may wait for
// the briefing as one it said can wait (later.go), unless it is about the matter in focus (attentionDefersQuestion).
func (s *Service) attentionDefersQuestions(ctx context.Context) bool {
	return s.attentionNow(ctx).Mode == assistantwire.AttentionImmersive
}

// attentionDefersQuestion: immersive mode is in force and task t's question, reported from machine/session, is not
// about the matter the user focuses on, so it waits as one that can wait. In focus (never deferred): the focus names
// the track t was opened from, the reporting session, or t's supervising conversation (the way
// assistantwire.Attention.inFocus reads a push).
func (s *Service) attentionDefersQuestion(ctx context.Context, t Todo, machine, session string) bool {
	a := s.attentionNow(ctx)
	if a.Mode != assistantwire.AttentionImmersive {
		return false
	}
	return !questionInFocus(a.Focus, s.todoTrack(ctx, t), s.settings(ctx).MachineID, t, machine, session)
}

// todoTrack is the track (space slug) task t was opened from: its origin conversation's; "" for the main space or none.
func (s *Service) todoTrack(ctx context.Context, t Todo) string {
	if t.Origin == "" {
		return ""
	}
	space, _ := s.selfSpace(ctx, t.Origin)
	return space
}

// questionInFocus: focus f names track (t's), the session machine/session that reports, or t's supervising
// conversation on the assistant's machine self.
func questionInFocus(f *assistantwire.AttentionFocus, track, self string, t Todo, machine, session string) bool {
	if f == nil {
		return false
	}
	if ft := strings.TrimSpace(f.Track); ft != "" && ft == track {
		return true
	}
	if f.SessionID == "" {
		return false
	}
	if f.SessionID == session && (f.MachineID == "" || f.MachineID == machine) {
		return true
	}
	sup := ""
	if t.Supervision != nil {
		sup = t.Supervision.Supervisor
	}
	return sup != "" && f.SessionID == sup && (f.MachineID == "" || f.MachineID == self)
}

// attentionFieldMax bounds a focus's slug and ids.
const attentionFieldMax = 200

// newAttention is the mode set asks for, set by by at now; ok false when it is not valid: an unknown mode, a mode
// other than normal without minutes within the limits, or a focus that is too long or names a track the registry
// does not know (known reports that). A focus is kept for immersive mode only.
func newAttention(set assistantwire.AttentionSet, by string, now time.Time, known func(string) bool) (assistantwire.Attention, bool) {
	mode := strings.TrimSpace(set.Mode)
	if !assistantwire.KnownAttentionMode(mode) {
		return assistantwire.Attention{}, false
	}
	a := assistantwire.Attention{Mode: mode, SetBy: by, SetAt: now.UTC()}
	if mode == assistantwire.AttentionNormal {
		return a, true
	}
	d := time.Duration(set.Minutes) * time.Minute
	if d < assistantwire.AttentionMinDuration || d > assistantwire.AttentionMaxDuration {
		return assistantwire.Attention{}, false
	}
	a.Until = now.Add(d).UTC()
	if f := set.Focus; f != nil && mode == assistantwire.AttentionImmersive {
		focus := assistantwire.AttentionFocus{Track: strings.TrimSpace(f.Track), MachineID: strings.TrimSpace(f.MachineID),
			SessionID: strings.TrimSpace(f.SessionID)}
		if len(focus.Track) > attentionFieldMax || len(focus.MachineID) > attentionFieldMax || len(focus.SessionID) > attentionFieldMax {
			return assistantwire.Attention{}, false
		}
		if focus.Track != "" && (known == nil || !known(focus.Track)) {
			return assistantwire.Attention{}, false
		}
		if focus != (assistantwire.AttentionFocus{}) {
			a.Focus = &focus
		}
	}
	return a, true
}

// putAttention stores a, refreshes the cache, wakes attentionLoop to end it on time, and reports the new profile to
// the hub.
func (s *Service) putAttention(ctx context.Context, a assistantwire.Attention) error {
	if s.store == nil || s.store() == nil {
		return errAttentionStore
	}
	if err := s.store().PutSetting(ctx, attentionSettingsKey, a); err != nil {
		return err
	}
	c := &s.attention
	c.mu.Lock()
	c.gen++
	c.cached, c.cachedAt = &a, time.Now()
	c.mu.Unlock()
	select {
	case s.attentionWake() <- struct{}{}:
	default:
	}
	s.publishSoon() // the hub's push filter reads the mode in the profile
	return nil
}

var errAttentionStore = i18n.Err("assistant-attention.store")

// attentionLoop ends a mode when its time is over: it is stored as normal again (set by the system) and the hub is
// told in the profile. The hub reads the end itself too, so a late wake changes nothing it delivers.
func (s *Service) attentionLoop(ctx context.Context) {
	wake := s.attentionWake()
	for {
		wait := attentionIdle
		if next := s.attentionDue(ctx); !next.IsZero() {
			wait = min(wait, max(time.Until(next), 50*time.Millisecond))
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// attentionDue puts a stored mode whose time is over back to normal, and returns when the one in force ends (zero:
// none in force).
func (s *Service) attentionDue(ctx context.Context) time.Time {
	stored := s.attentionStored(ctx)
	if stored.Mode == assistantwire.AttentionNormal || stored.Mode == "" {
		return time.Time{}
	}
	now := s.attentionTime()
	if stored.Effective(now).Active() {
		return stored.Until
	}
	if err := s.putAttention(ctx, assistantwire.Attention{Mode: assistantwire.AttentionNormal, SetBy: assistantwire.AttentionBySystem,
		SetAt: now.UTC()}); err != nil {
		s.logf("assistant: attention: back to normal: %v", err)
	}
	return time.Time{}
}

// ---- the apps' routes ----

func (s *Service) attentionRoutes() {
	s.handle("GET /api/v1/assistant/attention", s.getAttention)
	s.handle("PUT /api/v1/assistant/attention", s.putAttentionRoute)
}

func attentionErr(w http.ResponseWriter, status int, code string) {
	apierr.WriteCode(w, status, "attention_"+code, i18n.Err("assistant-attention."+code))
}

// writeAttention answers the mode in force, the modes there are, and whether this client may change it.
func writeAttention(w http.ResponseWriter, r *http.Request, a assistantwire.Attention) {
	if !fullRole(r) {
		a.Focus = nil // which conversation the user is deep in is theirs: another client reads the mode only
	}
	apierr.WriteJSON(w, map[string]any{"attention": a, "modes": assistantwire.AttentionModes, "can_write": fullRole(r),
		"max_minutes": int(assistantwire.AttentionMaxDuration / time.Minute)})
}

func (s *Service) getAttention(w http.ResponseWriter, r *http.Request) {
	writeAttention(w, r, s.attentionNow(r.Context()))
}

// putAttentionRoute sets the mode: only the user, in an app (a full-access client).
func (s *Service) putAttentionRoute(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	var set assistantwire.AttentionSet
	if err := decodeBody(w, r, &set, 4<<10); err != nil {
		writeBodyErr(w, err, err)
		return
	}
	ctx := r.Context()
	a, ok := newAttention(set, assistantwire.AttentionByUser, s.attentionTime(), func(slug string) bool { return s.trackKnown(ctx, slug) })
	if !ok {
		attentionErr(w, http.StatusBadRequest, "invalid")
		return
	}
	if err := s.putAttention(ctx, a); err != nil {
		attentionErr(w, http.StatusServiceUnavailable, "store")
		return
	}
	writeAttention(w, r, a.Effective(s.attentionTime()))
}
