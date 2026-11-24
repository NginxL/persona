package hub

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// The Home Screen widgets (#5664, #5721) draw only what the iPhone app last wrote into its App
// Group. While the app is open it writes on every change; when it is not, the hub wakes it with a silent push
// (wakeDevices "widgets", the push the phone_* tools use) when what the widgets count changes, and the app writes again.
// Background pushes are budgeted by iOS (a few an hour) and never reach an app the user force-quit, so:
//   - only devices that said they have widgets (NotifyPrefs.Widgets) and whose app is not open (phoneOpen);
//   - a change to Needs You goes at once; any other change of the counts at most once every widgetGap a device;
//   - with nothing changed, one wake a widgetHeartbeat, so a widget on a calm day is not shown as out of date (the
//     app's HomeSnapshot.staleAfter is an hour);
//   - a member hub's devices are the center's, as for the Live Activity (mesh_group.go).
const (
	widgetGap       = 15 * time.Minute
	widgetHeartbeat = 55 * time.Minute
)

// widgetCounts combines fleet counts with an intake revision. The revision belongs only to widgets,
// so recording a drink never changes the fleet or causes a Live Activity update.
type widgetCounts struct {
	needsYou, working, unread int
	intake                    uint64
}

func widgetCountsOf(st FleetState) widgetCounts {
	return widgetCounts{needsYou: st.NeedsYou, working: st.Working, unread: st.Unread}
}

// widgetWaker remembers, a device, the counts its widgets were last brought up to date with and when.
type widgetWaker struct {
	intake atomic.Uint64
	mu     sync.Mutex
	told   map[string]widgetCounts
	at     map[string]time.Time
}

func (la *liveActivities) widgetCounts(st FleetState) widgetCounts {
	c := widgetCountsOf(st)
	c.intake = la.widgets.intake.Load()
	return c
}

// intakeChanged moves the intake revision and wakes only the widget loop, and returns the new revision. Its existing
// tick retries a revision held by widgetGap; Needs You retains its immediate path and an open phone needs no
// background wake.
func (la *liveActivities) intakeChanged() uint64 {
	rev := la.widgets.intake.Add(1)
	select {
	case la.widgetChanges <- struct{}{}:
	default:
	}
	return rev
}

// intakeSent: the open apps were sent the health_intake event for revision rev. Only from then can an app that leaves
// have written its widgets with it (phoneLeft); the revision moves before the event goes out.
func (la *liveActivities) intakeSent(rev uint64) {
	la.mu.Lock()
	la.shownIntake = max(la.shownIntake, rev)
	la.mu.Unlock()
}

// due: whether the device's widgets need a wake now for counts c (and, when not, nothing to remember).
func (w *widgetWaker) due(device string, c widgetCounts, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	told, ok := w.told[device]
	if !ok {
		return true // since the hub started nothing is known of what it shows: once, to be sure
	}
	since := now.Sub(w.at[device])
	switch {
	case c.needsYou != told.needsYou:
		return true
	case c != told:
		return since >= widgetGap
	default:
		return since >= widgetHeartbeat
	}
}

// wrote: the device's app wrote its widgets with counts c at now (woken by the hub, or as it went to the background).
func (w *widgetWaker) wrote(device string, c widgetCounts, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.told == nil {
		w.told, w.at = map[string]widgetCounts{}, map[string]time.Time{}
	}
	w.told[device], w.at[device] = c, now
}

// forget drops a device that no longer has widgets or was signed out.
func (w *widgetWaker) forget(device string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.told, device)
	delete(w.at, device)
}

// widgetDevices are the phones that have widgets and can be pushed to; not a Mac or a watch (the widgets are the
// iPhone's).
func (p *Pusher) widgetDevices() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for id, d := range p.devices {
		if d.Kind != kindMac && d.Kind != "watch" && p.reachable(d) && devicePrefs(d.Prefs).Widgets {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// wakeWidgets wakes the apps whose widgets show other counts than st's, at the pace above. From the Live Activity's
// run loop: on every change of the fleet and on its tick (for the changes held back and the heartbeat).
func (la *liveActivities) wakeWidgets(st FleetState) {
	if la.h.isMember() || la.h.push == nil {
		return
	}
	now, c := la.now(), la.widgetCounts(st)
	ids := map[string]bool{}
	for _, device := range la.h.push.widgetDevices() {
		if la.h.phoneOpen(device) || !la.widgets.due(device, c, now) {
			continue
		}
		ids[device] = true
		la.widgets.wrote(device, c, now)
	}
	if len(ids) > 0 {
		la.h.push.wakeDevices("widgets", ids)
	}
}
