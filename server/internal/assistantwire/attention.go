package assistantwire

import (
	"strings"
	"time"
)

// Attention modes (#13503, docs/server/server.md "Attention modes"): a hub-wide state the user sets for a while
// (fragment, immersive, quiet) that reinterprets the push levels P0 / P1 / P2. The manager keeps it (the user sets it
// through the routes below) and reports it to the hub in its profile (Profile.Attention); the hub reads it when it
// filters a push (hub/push_attention.go). Each mode has a time limit and is back to normal by itself when it ends.
// Normal is today's behaviour: the low-distraction switch (hub.ln_mode) decides. A mode other than normal filters
// whatever that switch says and never writes it, so at the end the switch is what it was.

// attentionRoutes read and set the mode: full credentials only (not open to extension tokens); only the user sets it.
var attentionRoutes = []string{
	"GET /api/v1/assistant/attention",
	"PUT /api/v1/assistant/attention",
}

func init() { Routes = append(Routes, attentionRoutes...) }

// The attention modes.
const (
	AttentionNormal    = "normal"    // today's behaviour: hub.ln_mode decides
	AttentionFragment  = "fragment"  // between things: only what takes seconds to decide interrupts
	AttentionImmersive = "immersive" // a block of deep work: only the matter in focus and urgent calls get through
	AttentionQuiet     = "quiet"     // sleep or rest: nothing makes a sound but an urgent call
)

// AttentionModes are the modes a client may set, normal first.
var AttentionModes = []string{AttentionNormal, AttentionFragment, AttentionImmersive, AttentionQuiet}

// Limits of a mode's time: every mode but normal has one, at least a minute and at most a day.
const (
	AttentionMinDuration = time.Minute
	AttentionMaxDuration = 24 * time.Hour
)

// Push depths a Notification may carry (Notification.Depth): how long the owner needs for it.
const (
	DepthQuick = "quick" // seconds: answerable from the lock screen (the default)
	DepthDeep  = "deep"  // a plan, a long review, a document to read: worth a block of time
)

// Why an attention mode held a push back (the hub's held-push record, hub.ln_intercepts).
const (
	AttentionReasonFragmentBatch = "attn_fragment_batch" // P1 in fragment mode: kept for a batch, not alerted one by one
	AttentionReasonImmersiveHeld = "attn_immersive_held" // in immersive mode, a push about something not in focus
	AttentionReasonQuietHeld     = "attn_quiet_held"     // P1 in quiet mode: kept for the next briefing
	AttentionReasonDeepDeferred  = "attn_deep_deferred"  // a deep P0 in fragment mode: kept for a block of time
	// AttentionReasonSilent is a P2 push or a finished turn in any mode but normal: silent, as with hub.ln_mode on
	// (the hub's own reason for it, interceptReasonP2).
	AttentionReasonSilent = "ln_p2_silent"
)

// Who set the mode (Attention.SetBy).
const (
	AttentionByUser      = "user"      // in an app
	AttentionByAssistant = "assistant" // the assistant, because the user asked it to this turn
	AttentionBySystem    = "system"    // the manager, when the mode ran out
)

// Attention is the attention mode in force: Profile.Attention, and what GET /api/v1/assistant/attention answers.
type Attention struct {
	Mode string `json:"mode"`
	// Until is when the mode ends by itself; zero only for normal.
	Until time.Time `json:"until,omitzero"`
	SetBy string    `json:"set_by,omitempty"`
	SetAt time.Time `json:"set_at,omitzero"`
	// Focus is what immersive mode lets through: the pushes of this track or of this conversation. nil: nothing but
	// urgent calls (and the hub's own alerts).
	Focus *AttentionFocus `json:"focus,omitempty"`
}

// AttentionFocus is the matter an immersive block is about.
type AttentionFocus struct {
	Track     string `json:"track,omitempty"` // a track's slug: its conversations' pushes (Notification.Track)
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"` // one conversation or agent session, with its machine
}

// AttentionSet is PUT /api/v1/assistant/attention's body: a mode and for how many minutes (ignored for normal).
type AttentionSet struct {
	Mode    string          `json:"mode"`
	Minutes int             `json:"minutes,omitempty"`
	Focus   *AttentionFocus `json:"focus,omitempty"`
}

// Effective is a at now: normal once its time is over (or for a mode this build does not know, or without a time
// limit), so a hub or a manager that missed the end never keeps a mode on.
func (a *Attention) Effective(now time.Time) Attention {
	if a == nil || !KnownAttentionMode(a.Mode) || a.Mode == AttentionNormal || a.Until.IsZero() || !now.Before(a.Until) {
		return Attention{Mode: AttentionNormal}
	}
	return *a
}

// Active: a mode other than normal is in force (call on Effective's result).
func (a Attention) Active() bool { return a.Mode != "" && a.Mode != AttentionNormal }

// KnownAttentionMode: mode is one of AttentionModes.
func KnownAttentionMode(mode string) bool {
	for _, m := range AttentionModes {
		if m == mode {
			return true
		}
	}
	return false
}

// AttentionPush is what the attention filter knows of one push.
type AttentionPush struct {
	Level string // P0, P1 or P2; "" or unknown is P1
	Depth string // DepthQuick ("" too) or DepthDeep
	// Track is the track the push comes from (Notification.Track); MachineID and SessionID the session it opens.
	Track     string
	MachineID string
	SessionID string
	// Done: a finished turn ("Agent finished"), never pushed in a mode.
	Done bool
	// System: the hub's own alert (pairing, a computer joining), which the owner set off themself.
	System bool
	// UrgentCall: an urgent call's ring (call.ring). It always rings: a mode never holds or hushes it.
	UrgentCall bool
	// QuietHours: the device is inside its own quiet hours for a push they hush. The stricter side wins: quiet hours
	// never release what the mode holds, and the mode never gives back a sound quiet hours took.
	QuietHours bool
}

// AttentionVerdict is what the filter does with one push: hold it back (Reason why), or let it go, without a sound
// when Hush.
type AttentionVerdict struct {
	Hold   bool
	Reason string
	Hush   bool
}

// DecideAttention is what mode a (in force: Effective's result) does to push p. In normal mode it lets everything go
// and the caller applies hub.ln_mode as before; only quiet hours may hush.
//
//	            fragment                 immersive                         quiet
//	P0          goes; deep is deferred   goes only when in focus           goes, without a sound
//	P1          held for a batch         held                              held for the briefing
//	P2 / done   silent                   silent                            silent
//
// An urgent call always rings, and the hub's own alerts are never held (quiet mode still hushes them).
func DecideAttention(a Attention, p AttentionPush) AttentionVerdict {
	if p.UrgentCall {
		return AttentionVerdict{}
	}
	v := AttentionVerdict{Hush: p.QuietHours}
	if !a.Active() {
		return v
	}
	level := p.Level
	switch level {
	case LevelP0, LevelP2:
	default:
		level = LevelP1
	}
	if p.System {
		level = LevelP0
	} else if p.Done {
		level = LevelP2
	}
	hold := func(reason string) AttentionVerdict { return AttentionVerdict{Hold: true, Reason: reason} }
	if level == LevelP2 {
		return hold(AttentionReasonSilent)
	}
	switch a.Mode {
	case AttentionFragment:
		if level == LevelP1 {
			return hold(AttentionReasonFragmentBatch)
		}
		if p.Depth == DepthDeep && !p.System {
			return hold(AttentionReasonDeepDeferred)
		}
	case AttentionImmersive:
		if level == LevelP1 {
			return hold(AttentionReasonImmersiveHeld)
		}
		if !p.System && !a.inFocus(p) {
			return hold(AttentionReasonImmersiveHeld)
		}
	case AttentionQuiet:
		if level == LevelP1 {
			return hold(AttentionReasonQuietHeld)
		}
		v.Hush = true
	}
	return v
}

// inFocus: p is about the matter a's immersive block is about.
func (a Attention) inFocus(p AttentionPush) bool {
	f := a.Focus
	if f == nil {
		return false
	}
	if t := strings.TrimSpace(f.Track); t != "" && t == strings.TrimSpace(p.Track) {
		return true
	}
	return f.SessionID != "" && f.SessionID == p.SessionID && (f.MachineID == "" || f.MachineID == p.MachineID)
}
