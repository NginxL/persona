package hub

import (
	"errors"
	"maps"
	"slices"
	"time"
)

// Quiet hours (#5806): a device's nightly window in which agents' pushes stop ringing it. Inside the window,
//   - "Agent finished" is not sent at all (the morning brief and the Today card sum those up);
//   - "Needs you" (cards, reply reminders) still arrives, silently: no sound, interruption level passive on APNs,
//     "quiet": true on the WebSocket notify event;
//   - everything else (reminders the assistant sends because you asked, extension notifications, pairing) is as
//     before: the user asked for those.
// The OS's own Focus can only silence the whole app, not tell the first two apart.

// QuietHours is NotifyPrefs.Quiet: "HH:MM" local times in TZ (an IANA zone; empty: the user's time zone the hub
// knows, usertz.go). The window may cross midnight; Start == End is no window. Never set, it is all empty (off), and
// a client shows its own default times.
type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
	TZ      string `json:"tz,omitempty"`
}

// quietRule is what quiet hours do to one kind of push.
type quietRule int

const (
	quietNone    quietRule = iota // delivered as usual
	quietSkip                     // not delivered inside the window
	quietHush                     // delivered without sound inside the window
	quietHushAll                  // delivered without sound on every device, window or not (quiet attention mode)
)

var errQuietHours = errors.New(`quiet: start and end are "HH:MM", tz an IANA time zone`)

// validate reports a window the hub cannot read. One never set (off, no times) is fine.
func (q QuietHours) validate() error {
	if q == (QuietHours{}) {
		return nil
	}
	if _, ok := clockMinutes(q.Start); !ok {
		return errQuietHours
	}
	if _, ok := clockMinutes(q.End); !ok {
		return errQuietHours
	}
	if q.TZ != "" {
		if _, err := time.LoadLocation(q.TZ); err != nil {
			return errQuietHours
		}
	}
	return nil
}

// activeAt reports whether t falls inside the window, read in q.TZ or else in fallback, or else in UTC: the zone the hub
// runs in, never the machine's (tzguard). The hub always passes the user's zone (Pusher.userLoc); only a pusher made
// without it has none (#5878).
func (q QuietHours) activeAt(t time.Time, fallback *time.Location) bool {
	if !q.Enabled {
		return false
	}
	start, ok1 := clockMinutes(q.Start)
	end, ok2 := clockMinutes(q.End)
	if !ok1 || !ok2 || start == end {
		return false
	}
	loc := fallback
	if q.TZ != "" {
		if l, err := time.LoadLocation(q.TZ); err == nil {
			loc = l
		}
	}
	if loc == nil {
		loc = time.UTC
	}
	lt := t.In(loc)
	now := lt.Hour()*60 + lt.Minute()
	if start < end {
		return now >= start && now < end
	}
	return now >= start || now < end // across midnight
}

// clockMinutes reads "HH:MM" (00:00–23:59) as minutes after midnight.
func clockMinutes(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	d := func(b byte) (int, bool) { return int(b - '0'), b >= '0' && b <= '9' }
	h1, ok1 := d(s[0])
	h2, ok2 := d(s[1])
	m1, ok3 := d(s[3])
	m2, ok4 := d(s[4])
	h, m := h1*10+h2, m1*10+m2
	if !ok1 || !ok2 || !ok3 || !ok4 || h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// quiet is what quiet hours do to a push of this kind.
func (w pushWants) quiet() quietRule {
	switch w.Kind {
	case "done":
		return quietSkip
	case "input":
		return quietHush
	}
	return quietNone
}

// inQuiet reports whether a device with these prefs is inside its quiet hours now.
func (p *Pusher) inQuiet(prefs NotifyPrefs) bool {
	if p == nil || !prefs.Quiet.Enabled {
		return false
	}
	var loc *time.Location
	if p.userLoc != nil {
		loc = p.userLoc()
	}
	return prefs.Quiet.activeAt(p.now(), loc)
}

// quietNow reports whether any device that turned quiet hours on is inside its window now. The Lark bot's nudges
// (#6084) are not tied to a device, so they wait while the user has declared any of theirs quiet: the quiet hours are
// the user's, set on the device they sleep with.
func (p *Pusher) quietNow() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	prefs := make([]NotifyPrefs, 0, len(p.devices))
	for _, d := range p.devices {
		prefs = append(prefs, devicePrefs(d.Prefs))
	}
	p.mu.Unlock()
	return slices.ContainsFunc(prefs, p.inQuiet)
}

// hushed is an APNs alert payload delivered without a sound and without lighting the screen.
func hushed(payload map[string]any) map[string]any {
	aps, ok := payload["aps"].(map[string]any)
	if !ok {
		return payload
	}
	aps = maps.Clone(aps)
	delete(aps, "sound")
	aps["interruption-level"] = "passive"
	payload = maps.Clone(payload)
	payload["aps"] = aps
	return payload
}
