package assistant

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- checking ----

// probeLauncherHealth runs launchers.check for launcher on machine; false when the machine could not be asked (no result).
func (s *Service) probeLauncherHealth(ctx context.Context, machine, launcher string) (healthResult, bool) {
	if proto.IsExternalLauncher(launcher) {
		return healthResult{}, false // no automatic calls to an opt-in, main-chat-only source
	}
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	observed, _ := s.launcherHealth(ctx, machine, launcher)
	params := map[string]any{"name": launcher}
	if observed.Kind == errKindUsageLimit && observed.unusable() {
		if models := observed.limitModels(); len(models) > 0 {
			params["model"] = models[0] // one family per probe; a pass clears it and the next probe asks the next
		}
	}
	var res proto.LauncherCheck
	if err := s.call(ctx, machine, assistantwire.DeviceLaunchersCheck, params, &res); err != nil {
		s.logf("launcher health: check %s on %s: %v", launcher, machine, err)
		return healthResult{}, false
	}
	return healthResult{ok: res.OK, agent: res.Agent, model: res.Model, millis: res.Millis, err: res.Error, source: healthSourceCheck, observed: &observed}, true
}

// healthLaunchers are the launchers to check on machine: those it can start (its ~/.zshrc's, the managed ones) and
// the default one when every machine has it (plain claude: a default only some have never starts where it isn't,
// launcher_fit.go); not an API provider's. A runtime's launcher only when the main chat or Agents run on it (#10369):
// those are in whileFailing, checked only while their record on machine says they do not work, through the runtime's
// own launchers.check as the on-demand probe does. So a failure is rechecked and a pass clears it, but no runtime is
// asked to spend a request each interval, and the record a pass leaves stays while the machine lists the launcher.
func (s *Service) healthLaunchers(ctx context.Context, machine string) (names []string, whileFailing map[string]bool, err error) {
	list, err := s.machineLaunchers(ctx, machine)
	if err != nil {
		return nil, nil, err
	}
	var runtimes []string
	for _, l := range list {
		switch {
		case startableLauncher(l):
			names = append(names, l.Name)
		case runtimeLauncher(l):
			runtimes = append(runtimes, l.Name)
		}
	}
	d := s.startLauncher(ctx, "")
	if !slices.Contains(names, d) && s.launcherEverywhere(ctx, d) {
		names = append(names, d)
	}
	for _, name := range []string{s.launcherNow(ctx), d} {
		if slices.Contains(runtimes, name) && !slices.Contains(names, name) {
			names = append(names, name)
			if whileFailing == nil {
				whileFailing = map[string]bool{}
			}
			whileFailing[name] = true
		}
	}
	return names, whileFailing, nil
}

// runtimeLauncher: l is a runtime's launcher (DSH, Paseo, agy…: its source is the runtime's id), which the node checks
// through the runtime. Not the desktop apps' (their check runs no prompt and always fails) nor an external source's.
func runtimeLauncher(l proto.Launcher) bool {
	source := compat.Source(l.Source)
	return runtimeSource(l.Source) && source != proto.SourceClaudeDesktop && source != codexAppSource &&
		source != proto.ExternalLauncherSource && !proto.IsExternalLauncher(l.Name)
}

// codexAppSource is the source of codex-app, a new chat in the Codex App (node/codexapp_new.go).
const codexAppSource = "codex_app"

// startLauncher is the launcher a start with launcher runs: itself, else model settings › Agents', else claude.
func (s *Service) startLauncher(ctx context.Context, launcher string) string {
	if launcher != "" {
		return launcher
	}
	if b, err := s.resolveModel(ctx, "agent.new"); err == nil {
		if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok && name != "" {
			return name
		}
	}
	return "claude"
}

// healthRound is one look at the machines: the launchers due on each reachable one are checked (each machine's one
// after another, the machines side by side), then repaired; the repair agents at work are followed. wait: return
// when it is all done (tests, a forced check), else it goes on in the background.
func (s *Service) healthRound(ctx context.Context, wait bool) {
	cfg := s.healthConfig(ctx)
	devices := s.devices(ctx)
	b := &s.health
	var wg sync.WaitGroup
	round := time.Now()
	var scheduled time.Duration // machines started so far, times healthStagger: see healthStagger
	for _, d := range devices {
		b.mu.Lock()
		s.healthLoadLocked(ctx)
		cameOnline := d.Reachable && !b.online[d.ID]
		b.online[d.ID] = d.Reachable
		busy := b.checking[d.ID]
		if d.Reachable && cfg.on && !busy {
			b.checking[d.ID] = true
		}
		b.mu.Unlock()
		if !d.Reachable || !cfg.on || busy {
			continue
		}
		wg.Add(1)
		due := round.Add(scheduled) // an absolute time, so a goroutine that is scheduled late does not slip into the one before
		scheduled += healthStagger
		go func() {
			defer wg.Done()
			defer func() {
				b.mu.Lock()
				delete(b.checking, d.ID)
				b.mu.Unlock()
			}()
			if left := time.Until(due); left > 0 { // not healthRound's wait: this goroutine only
				select {
				case <-ctx.Done():
					return
				case <-time.After(left):
				}
			}
			s.checkMachine(ctx, d, cfg, cameOnline, "")
			s.repairMachine(ctx, d, cfg)
		}()
	}
	if wait {
		wg.Wait()
	}
	s.followRepairs(ctx, cfg)
}

// checkMachine checks machine d's launchers that are due (only: one by that name, at once); the records of launchers
// it no longer has go.
func (s *Service) checkMachine(ctx context.Context, d Device, cfg healthConfig, cameOnline bool, only string) []LauncherHealth {
	names, whileFailing, err := s.healthLaunchers(ctx, d.ID)
	if err != nil {
		s.logf("launcher health: %s: launchers: %v", d.ID, err)
		return nil
	}
	b := &s.health
	b.mu.Lock()
	for key, r := range b.recs {
		if r.Machine == d.ID && !slices.Contains(names, r.Launcher) {
			delete(b.recs, key)
			delete(b.recheckAt, key)
		}
	}
	b.mu.Unlock()
	var out []LauncherHealth
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		if only != "" && name != only {
			continue
		}
		r, ok := s.launcherHealth(ctx, d.ID, name)
		if whileFailing[name] && !(ok && r.unusable()) {
			continue // a runtime's launcher that works, or was never seen failing here: nothing to recheck (#10369)
		}
		if only == "" {
			since := time.Since(r.CheckedAt)
			if reset, limited := r.limitReset(); limited {
				// A check before the reset is refused too, and spends a request for it; the first round after it
				// checks, whatever the interval. From a minute after, as the manager continues a turn its limit
				// stopped (limitContinueAfter): one right at the reset may still be refused.
				if time.Now().Before(reset.Add(limitContinueAfter)) {
					continue
				}
			} else if ok && since < cfg.interval && !(cameOnline && since >= healthOnlineRecheck) {
				continue
			}
			if ok && r.Kind == errKindNotInTerminal && r.unusable() {
				continue // the login shell's check cannot tell (recordHealth): it waits for a start, or goes stale
			}
		}
		if res, ok := s.probeLauncherWithRetry(ctx, d.ID, name); ok {
			out = append(out, s.recordHealth(ctx, d.ID, name, res))
		}
	}
	return out
}

// probeLauncherWithRetry is probeLauncherHealth, asked once more right away when the first check timed out: only a
// second timeout in a row is recorded. A gateway can leave one request without a first token while the same account
// answers the others in seconds (2026-11-14: one shared account left a few percent of its requests silent for 90
// seconds and longer), and a single silent request would otherwise mark a working launcher failing until the next
// round, half an hour later. A fresh request is a new upstream stream and usually answers.
func (s *Service) probeLauncherWithRetry(ctx context.Context, machine, launcher string) (healthResult, bool) {
	res, ok := s.probeLauncherHealth(ctx, machine, launcher)
	if !ok || res.ok || classifyLauncherError(res.err) != errKindTimeout || ctx.Err() != nil {
		return res, ok
	}
	s.logf("launcher health: %s on %s timed out, checking once more: %s", launcher, machine, clip(oneLine(res.err), 200))
	if again, ok := s.probeLauncherHealth(ctx, machine, launcher); ok {
		return again, true
	}
	return res, true
}

// ---- picking a machine ----

// healthyMachine is another reachable machine that runs launcher and whose check of it did not fail: one where it
// passed first, then one where it was never checked. Optional filters keep candidates that can host the work.
func (s *Service) healthyMachine(ctx context.Context, launcher, except string, suitable ...func(Device) bool) (Device, bool) {
	best, rank := Device{}, 3
	for _, d := range s.devices(ctx) {
		if d.ID == except || !d.Reachable {
			continue
		}
		r, ok := s.launcherHealth(ctx, d.ID, launcher)
		n := 1
		switch {
		case ok && r.unusable():
			continue
		case ok && r.State == healthOK:
			n = 0
		}
		if n >= rank || !s.launcherOn(ctx, d.ID, launcher) {
			continue
		}
		if slices.ContainsFunc(suitable, func(accept func(Device) bool) bool { return !accept(d) }) {
			continue
		}
		best, rank = d, n
	}
	return best, rank < 3
}

// launcherOn: machine has launcher (a managed one is on every machine; a failed list says no).
func (s *Service) launcherOn(ctx context.Context, machine, launcher string) bool {
	if proto.IsExternalLauncher(launcher) {
		return false // external targets are never automatic relocation candidates
	}
	if launcher == "claude" || slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == launcher && l.Kind != "api" }) {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.machineLaunchers(cctx, machine)
	return err == nil && slices.ContainsFunc(list, func(l proto.Launcher) bool {
		return l.Name == launcher && l.Source != proto.ExternalLauncherSource && !proto.IsExternalLauncher(l.Name)
	})
}

// rehome moves dir from one machine's home to another's (a path outside the home stays).
func rehome(dir string, from, to Device) string {
	if from.Home == "" || to.Home == "" || from.Home == to.Home {
		return dir
	}
	if dir == from.Home {
		return to.Home
	}
	if rest, ok := strings.CutPrefix(dir, from.Home+"/"); ok {
		return path.Join(to.Home, rest)
	}
	return dir
}

// recheckLauncherNow probes machine's launcher on the spot when its record says the launcher fails, so a fix since
// the last round (a CLI installed, a PATH corrected) clears the record and the start stays on the asked machine.
// A probe already running is joined; a probe within healthRecheckThrottle is not repeated (checked=false), and the
// caller keeps the usual reroute behavior. got=false means the machine could not be asked.
func (s *Service) recheckLauncherNow(ctx context.Context, machine, launcher string) (res healthResult, got, checked bool) {
	rc, run := s.claimHealthRecheck(ctx, machine, launcher)
	if rc == nil {
		return healthResult{}, false, false
	}
	if run {
		s.runHealthRecheck(ctx, machine, launcher, rc, false)
	}
	select {
	case <-rc.done:
		return rc.res, rc.got, true
	case <-ctx.Done():
		return healthResult{}, false, false
	}
}

// claimHealthRecheck reserves one probe for both a failed start and the detour that follows it. A nil result is
// throttled; run means the caller owns the probe, otherwise it joins the already reserved one.
func (s *Service) claimHealthRecheck(ctx context.Context, machine, launcher string) (rc *healthRecheck, run bool) {
	b := &s.health
	key := healthKey(machine, launcher)
	b.mu.Lock()
	defer b.mu.Unlock()
	s.healthLoadLocked(ctx)
	if rc := b.recheck[key]; rc != nil {
		rc.waiters++
		return rc, false
	}
	if at := b.recheckAt[key]; !at.IsZero() && time.Since(at) < healthRecheckThrottle {
		return nil, false
	}
	rc = &healthRecheck{done: make(chan struct{})}
	b.recheck[key] = rc
	b.recheckAt[key] = time.Now()
	return rc, true
}

// runHealthRecheck publishes the health result before releasing waiters. A start's follow-up records failures too;
// an on-demand detour also records usage-limit failures so a later reset is not discarded. Other failures keep the
// periodic repair cadence.
func (s *Service) runHealthRecheck(ctx context.Context, machine, launcher string, rc *healthRecheck, recordFailure bool) {
	defer func() {
		s.health.mu.Lock()
		delete(s.health.recheck, healthKey(machine, launcher))
		close(rc.done)
		s.health.mu.Unlock()
	}()
	rc.res, rc.got = s.probeLauncherHealth(ctx, machine, launcher)
	if rc.got && (rc.res.ok || recordFailure || classifyLauncherError(rc.res.err) == errKindUsageLimit) {
		s.recordHealth(ctx, machine, launcher, rc.res)
	}
}

// launcherUnusableNow is launcherUnusable after one on-the-spot probe of a failing record, for a start about to
// move or be refused because of it: the record can lag behind a fix, so a pass clears it and the start stays on the
// asked machine (#2018). A usage limit can be over before its reset, or with its reset unknown (#9386): it is probed
// too, once its evidence is healthRecheckThrottle old, and the claim's throttle spaces the probes after that. A
// terminal-shell failure cannot be settled by the probe's login shell; only a real start clears it.
func (s *Service) launcherUnusableNow(ctx context.Context, machine, launcher string) (LauncherHealth, bool) {
	r, bad := s.launcherUnusable(ctx, machine, launcher)
	if !bad || r.Kind == errKindNotInTerminal {
		return r, bad
	}
	if r.Kind == errKindUsageLimit && time.Since(r.CheckedAt) < healthRecheckThrottle {
		return r, bad
	}
	s.recheckLauncherNow(ctx, machine, launcher)
	// The shared follow-up may have finished between the first read and the throttled claim. Decide from its
	// published result, not the stale failure that led us here.
	return s.launcherUnusable(ctx, machine, launcher)
}

// healthDetour is where a start with launcher on machine m goes instead: another machine when its last check failed
// there and another has it healthy (moved), with a line saying so for whoever started it; no other: m, and the line
// says it may fail. A usage limit with a known reset says when that is, and a saturated account pool (capacity.go)
// adds the fleet-wide "no schedulable account" fact and its nearest recovery when the data is fresh.
func (s *Service) healthDetour(ctx context.Context, m Device, launcher string, suitable ...func(Device) bool) (Device, string, bool) {
	r, bad := s.launcherUnusableNow(ctx, m.ID, launcher)
	if !bad {
		return m, "", false
	}
	alt, moved := s.healthyMachine(ctx, launcher, m.ID, suitable...)
	// The pool is provider-level: resolve it once from the failed machine. Missing or stale data gives no note, and
	// a pool with any schedulable account stays quiet too. Read only after the on-the-spot recheck, since a passing
	// probe returns above without any detour line.
	poolNote := poolSuffix(s.capacityNote(ctx, s.launcherProvider(ctx, m.ID, launcher)))
	if note := r.resetNote(); note != "" {
		what := fmt.Sprintf("launcher %s is at its subscription usage limit on %s (%s)", launcher, m.ID, note)
		if moved {
			return alt, what + poolNote + fmt.Sprintf(", so this runs on %s instead", alt.ID), true
		}
		return m, what + poolNote + " and no other machine has it healthy; started there anyway", false
	}
	why := fmt.Sprintf("%s: %s", r.Kind, clip(r.Error, 160))
	if moved {
		return alt, fmt.Sprintf("launcher %s failed its health check on %s (%s), so this runs on %s instead%s", launcher, m.ID, why, alt.ID, poolNote), true
	}
	return m, fmt.Sprintf("launcher %s failed its last health check on %s (%s) and no other machine has it healthy; started there anyway%s", launcher, m.ID, why, poolNote), false
}

// healthLines are agents_list's lines about the launchers that do not work (machine: only there).
func (s *Service) healthLines(ctx context.Context, machine string) string {
	var b strings.Builder
	for _, r := range s.healthRecords(ctx, "", machine) {
		if !r.unusable() {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("Launchers that do not work now (agent_start / task_plan move off them):\n")
		}
		fmt.Fprintf(&b, "- machine=%s launcher=%s · %s · %s: %s · checked %s", r.Machine, r.Launcher, r.State, r.Kind, clip(r.Error, 200), ago(r.CheckedAt))
		if note := r.resetNote(); note != "" {
			b.WriteString(" · " + note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// launcherWithHealth is a launcher of the "New Agent" picker with how its last check went on that machine. Its
// display_name is the launcher's own (proto.Launcher.DisplayName): a runtime's name for its provider (#8261), which a
// Persona-managed provider's own name replaces (launcher_auto_name).
type launcherWithHealth struct {
	ConfiguredLauncher
	Health *launcherHealthView `json:"health,omitempty"`
}

type launcherHealthView struct {
	State     string     `json:"state"`
	Kind      string     `json:"kind,omitempty"`
	Error     string     `json:"error,omitempty"`
	CheckedAt time.Time  `json:"checked_at"`
	ResetsAt  *time.Time `json:"resets_at,omitempty"` // a usage limit's reset: unusable until then
}

// withHealth puts machine's health records on its launchers (none: never checked) and leaves out the ones that do not
// work now. The launcher pickers list only what a start can use: a launcher whose latest check failed (failing,
// repairing or blocked within healthStale, a usage limit until its reset) is hidden until a check passes again. A
// launcher never checked stays: no result is not a failure, and launcher health is off unless the manager turns it on.
//
// Two stay listed with their record: a not_in_terminal launcher (its checks pass, only a real start clears it, and
// hiding it would leave no way to make that start) and keep, the launcher the configuration runs with no pick. That one
// will be used by the next start, so the form must be able to say why it may fail (clients mark the Default row and its
// line); hiding it would fail the start with no warning at all.
func (s *Service) withHealth(ctx context.Context, machine string, list []ConfiguredLauncher, keep string) []launcherWithHealth {
	out := make([]launcherWithHealth, 0, len(list))
	for _, l := range list {
		v := launcherWithHealth{ConfiguredLauncher: l}
		if r, ok := s.launcherHealth(ctx, machine, l.Name); ok {
			if r.unusable() && r.Kind != errKindNotInTerminal && l.Name != keep {
				continue
			}
			if r.State == healthOK || r.unusable() { // not a failure too old to count
				v.Health = &launcherHealthView{State: r.State, Kind: r.Kind, Error: r.Error, CheckedAt: r.CheckedAt}
				if reset, limited := r.limitReset(); limited {
					v.Health.ResetsAt = &reset
				}
			}
		}
		out = append(out, v)
	}
	return out
}
