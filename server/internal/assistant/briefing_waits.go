package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// BriefingWaits tunes the deterministic wait lists, not the supervisor's own signals. The defaults deliberately
// reuse the existing supervision floors: a working agent is stalled after 20 minutes, any other silent session after
// two hours, an offline agent is orphaned after ten minutes, and an unanswered card waits three minutes. MaxRows is a
// display bound per direction; owner-wait rows are always kept in full.
type BriefingWaits struct {
	StalledMinutes int `json:"stalled_minutes"`
	SilentMinutes  int `json:"silent_minutes"`
	OrphanMinutes  int `json:"orphan_minutes"`
	CardMinutes    int `json:"card_minutes"`
	MaxRows        int `json:"max_rows"`
}

func defaultBriefingWaits() BriefingWaits {
	return BriefingWaits{StalledMinutes: int(superviseStallMin / time.Minute), SilentMinutes: int(superviseSilentMin / time.Minute),
		OrphanMinutes: int(orphanOfflineAfter / time.Minute), CardMinutes: int(superviseNeedsAfter / time.Minute), MaxRows: 15}
}

func briefingWaitsOf(cfg Settings) BriefingWaits {
	out := defaultBriefingWaits()
	if cfg.BriefingWaits != nil {
		out = *cfg.BriefingWaits
	}
	return out
}

func (w BriefingWaits) validate() error {
	for _, n := range []int{w.StalledMinutes, w.SilentMinutes, w.OrphanMinutes, w.CardMinutes} {
		if n < 1 || n > 7*24*60 {
			return errors.New("minute thresholds must be whole numbers, 1–10080")
		}
	}
	if w.MaxRows < 1 || w.MaxRows > 100 {
		return errors.New("max_rows must be a whole number, 1–100")
	}
	return nil
}

func (w BriefingWaits) stallAfter(expect int) time.Duration {
	return max(time.Duration(w.StalledMinutes)*time.Minute, time.Duration(expect)*time.Minute*3/2)
}

func (w BriefingWaits) silentAfter(expect int) time.Duration {
	return max(time.Duration(w.SilentMinutes)*time.Minute, time.Duration(expect)*time.Minute*3/2)
}

func (s *Service) getBriefingWaits(ctx context.Context) (string, error) {
	raw, err := json.Marshal(briefingWaitsOf(s.settings(ctx)))
	return string(raw), err
}

// setBriefingWaits is an ordinary persona_settings change: the tool only prepares a before/after card, and the
// existing approval path calls apply. null restores the defaults documented in ownSettings.
func (s *Service) setBriefingWaits(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var next *BriefingWaits
	if err := json.Unmarshal(raw, &next); err != nil {
		return nil, errors.New("value must be the whole BriefingWaits object, or null for the defaults")
	}
	if next != nil {
		if err := next.validate(); err != nil {
			return nil, err
		}
	}
	before := briefingWaitsOf(s.settings(ctx))
	after := defaultBriefingWaits()
	if next != nil {
		after = *next
	}
	if reflect.DeepEqual(before, after) {
		return &ownChange{summary: "assistant.briefing_waits is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	words := func(w BriefingWaits) string {
		return fmt.Sprintf("stalled=%dm, silent=%dm, orphan=%dm, cards=%dm, max_rows=%d", w.StalledMinutes, w.SilentMinutes, w.OrphanMinutes, w.CardMinutes, w.MaxRows)
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_briefing_waits", i18n.Args{"before": words(before), "after": words(after)})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.BriefingWaits = next; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return "assistant.briefing_waits updated; the next briefing's deterministic wait lists use it.", nil
	}}, nil
}

// briefDirections reads the same task, review, queue, session and interaction ledgers as supervision. It does not ask
// a model to infer why work stopped. A task with no dispatch attempt is counted only; a queue is an attempt even before
// its first agent exists. Tasks whose supervisor is not a current direction (notably the background supervisor) belong
// to main, so leader-dispatched and main-dispatched work are both covered.
func (s *Service) briefDirections(ctx context.Context, now time.Time, l i18n.Lang, web string) ([]prompts.BriefDirection, []prompts.BriefDisabledRoutine) {
	cfg, waits := s.settings(ctx), briefingWaitsOf(s.settings(ctx))
	ids := make([]string, 0, len(cfg.Leaders)+1)
	if cfg.Main != "" {
		ids = append(ids, cfg.Main)
	}
	for _, id := range cfg.Leaders {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, s.briefDisabledRoutines(ctx, now, l, nil)
	}
	names := s.leaderNames(ctx, ids)
	if cfg.Main != "" {
		names[cfg.Main] = i18n.T(l, "assistant-feishu_cards.main_chat")
	}
	loc := s.location(ctx)
	dirs := make([]prompts.BriefDirection, len(ids))
	byID := make(map[string]int, len(ids))
	for i, id := range ids {
		dirs[i] = prompts.BriefDirection{ID: id, Name: names[id], Owner: id == cfg.Main, Link: sessionLink(web, cfg.MachineID, id, true)}
		byID[id] = i
	}
	directionOf := func(t Todo) (int, bool) {
		id := t.Origin
		if t.Supervision != nil && t.Supervision.Supervisor != "" {
			id = t.Supervision.Supervisor
		}
		if i, ok := byID[id]; ok {
			return i, true
		}
		i, ok := byID[cfg.Main]
		return i, ok && (t.Supervision != nil || t.Origin != "")
	}

	queued := map[string]*TaskQueue{}
	for _, p := range s.queueCandidatePlans(ctx) {
		if p.State != planStarted {
			continue
		}
		for _, pt := range p.Tasks {
			if pt.Queue != nil && pt.TodoID != "" {
				q := *pt.Queue
				queued[pt.TodoID] = &q
			}
		}
	}

	// Cards are mapped only through a real open task/session link. A card in a leader or main chat without such a link
	// stays at direction level; nothing guesses a task from its title. Private cards never contribute title or reason.
	cards := map[string][]proto.Interaction{}
	var loose []proto.Interaction
	for _, ix := range s.pendingInteractions() {
		if s.interactionEnded(ix.ID) || now.Sub(ix.CreatedAt) < time.Duration(waits.CardMinutes)*time.Minute {
			continue
		}
		if t, ok := s.todoOfSession(ctx, ix.MachineID, ix.SessionID); ok {
			cards[t.ID] = append(cards[t.ID], ix)
		} else if _, ok := byID[ix.SessionID]; ok {
			loose = append(loose, ix)
		}
	}

	todos := s.todosOf(ctx, "*")
	lists := machineSessions{}
	reachable := map[string]bool{}
	for _, d := range s.devices(ctx) {
		reachable[d.ID] = d.Reachable
	}
	orphanLists, orphanFailed := map[string]map[string]proto.Session{}, map[string]bool{}
	for _, t := range todos {
		if t.DoneAt != nil {
			continue
		}
		i, ok := directionOf(t)
		if !ok {
			continue
		}
		d := &dirs[i]
		d.Open++
		queue := queued[t.ID]
		if len(t.Agents) == 0 && queue == nil {
			d.Undispatched++
			continue
		}
		agents := s.taskAgentsIn(ctx, t, lists)
		w, stopped := s.briefTaskWait(ctx, t, agents, cards[t.ID], queue, now, waits, reachable, orphanLists, orphanFailed)
		if !stopped {
			d.Active++
			continue
		}
		w.Link = todoLink(web, t.ID)
		if w.Kind == "private" && w.InteractionID != "" {
			// A task-linked ordinary card still opens its to-do. A Private card keeps only its conversation link, never a
			// URL that would reveal the task identity it deliberately omits.
			ix := cards[t.ID][0]
			for _, candidate := range cards[t.ID] {
				if candidate.ID == w.InteractionID {
					ix = candidate
					break
				}
			}
			w.Link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
		}
		w.Line = briefWaitLine(l, w, now, loc)
		d.Waits = append(d.Waits, w)
	}
	for _, ix := range loose {
		i := byID[ix.SessionID]
		w := briefCardWait(ix, l)
		w.Link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
		w.Line = briefWaitLine(l, w, now, loc)
		dirs[i].Waits = append(dirs[i].Waits, w)
	}
	for i := range dirs {
		foldBriefDirection(&dirs[i], waits.MaxRows)
	}
	return dirs, s.briefDisabledRoutines(ctx, now, l, names)
}

func briefOwnerWait(kind string) bool {
	return kind == "needs_user" || kind == "question" || kind == "permission" || kind == "private"
}

func foldBriefDirection(d *prompts.BriefDirection, maxRows int) {
	slices.SortStableFunc(d.Waits, func(a, b prompts.BriefWait) int {
		if ao, bo := briefOwnerWait(a.Kind), briefOwnerWait(b.Kind); ao != bo {
			if ao {
				return -1
			}
			return 1
		}
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return strings.Compare(a.TodoID+a.InteractionID, b.TodoID+b.InteractionID)
	})
	// Owner describes the direction, not its rows: the same non-owner-wait bound applies to main and every leader.
	kept := make([]prompts.BriefWait, 0, len(d.Waits))
	others := 0
	for _, w := range d.Waits {
		if briefOwnerWait(w.Kind) || others < maxRows {
			kept = append(kept, w)
		}
		if !briefOwnerWait(w.Kind) {
			others++
		}
	}
	d.More = max(0, others-maxRows)
	d.Waits = kept
}

func (s *Service) briefTaskWait(ctx context.Context, t Todo, agents []proto.Session, cards []proto.Interaction, queue *TaskQueue,
	now time.Time, waits BriefingWaits, reachable map[string]bool, lists map[string]map[string]proto.Session, failed map[string]bool) (prompts.BriefWait, bool) {
	base := prompts.BriefWait{TodoID: t.ID, Title: clip(oneLine(t.Title), 80)}
	v := t.Supervision
	if v != nil {
		switch v.state(t) {
		case reviewNeedsUser:
			base.Kind, base.WaitingOn, base.Since = "needs_user", "owner", v.UpdatedAt
			if len(v.Reviews) > 0 {
				r := v.Reviews[len(v.Reviews)-1]
				base.Reason = clip(oneLine(r.Reason), 120)
				if base.Reason == "" {
					base.Reason = clip(oneLine(r.Note), 120)
				}
			}
			return base, true
		case reviewPending:
			base.Kind, base.WaitingOn, base.Since = "review", "supervisor", v.UpdatedAt
			if v.PendingSince != nil {
				base.Since = *v.PendingSince
			}
			return base, true
		case reviewParked:
			if p := v.parking(); p != nil {
				base.Kind, base.WaitingOn, base.Since, base.Until, base.Reason = "parked", p.WaitingOn, p.At, p.Until, clip(oneLine(p.Reason), 120)
				return base, true
			}
		}
	}
	if len(cards) > 0 {
		slices.SortFunc(cards, func(a, b proto.Interaction) int { return a.CreatedAt.Compare(b.CreatedAt) })
		w := briefCardWait(cards[0], s.botLang(ctx))
		if w.Kind != "private" {
			w.TodoID, w.Title = base.TodoID, base.Title
		}
		return w, true
	}
	if queue != nil {
		base.Kind, base.WaitingOn, base.Since, base.Until, base.Reason = "capacity", "capacity", queue.QueuedAt, queue.NextAt, clip(oneLine(queue.Reason), 120)
		return base, true
	}
	if len(t.Agents) > 0 {
		if oa, known := s.orphanAgents(ctx, t, reachable, lists, failed); known {
			if o, orphan := taskOrphan(t, oa, now); orphan {
				since := now
				if old, ok := s.orphanOf(t.ID); ok && !old.Since.IsZero() {
					since = old.Since
				} else {
					s.orphans.mu.Lock()
					if at := s.orphans.seen[t.ID]; !at.IsZero() {
						since = at
					}
					s.orphans.mu.Unlock()
				}
				if o.Reason != orphanOffline {
					if since.Equal(now) {
						// A closed/error session already has a real last-activity timestamp. On first discovery there
						// is no earlier orphan mark, so do not rewrite a long-finished run as "stopped just now".
						var last time.Time
						for _, c := range agents {
							at := c.UpdatedAt
							if c.ActivityAt != nil && c.ActivityAt.After(at) {
								at = *c.ActivityAt
							}
							if at.After(last) {
								last = at
							}
						}
						if !last.IsZero() && last.Before(since) {
							since = last
						}
					}
					base.Kind, base.WaitingOn, base.Since, base.Reason = "orphaned", "technical", since, o.Reason
					return base, true
				}
				if now.Sub(since) >= time.Duration(waits.OrphanMinutes)*time.Minute {
					base.Kind, base.WaitingOn, base.Since, base.Reason = "orphaned", "technical", since, o.Reason
					return base, true
				}
			}
		}
	}
	if brief, ok := s.briefOfTodo(t.ID); ok && s.briefTip(brief.ID) == t.ID && !brief.DueAt.IsZero() && !now.Before(brief.DueAt) {
		base.Kind, base.WaitingOn, base.Since = "overdue", "supervisor", brief.DueAt
		return base, true
	}
	expect := 0
	if v != nil {
		expect = v.ExpectMinutes
	}
	var latest proto.Session
	var moved time.Time
	for _, c := range agents {
		at := c.UpdatedAt
		if c.ActivityAt != nil && c.ActivityAt.After(at) {
			at = *c.ActivityAt
		}
		if at.After(moved) {
			latest, moved = c, at
		}
	}
	// The task is as live as its most recently active agent. An old stopped session must not make a task with another
	// active agent look stalled; the threshold (working vs. silent) comes from that latest session's state.
	if !moved.IsZero() {
		after := waits.silentAfter(expect)
		if latest.State == proto.StateWorking {
			after = waits.stallAfter(expect)
		}
		if now.Sub(moved) >= after {
			base.Kind, base.WaitingOn, base.Since = "stalled", "technical", moved
			base.Reason = clip(oneLine(latest.Activity), 120)
			return base, true
		}
	}
	// Every agent was released or dismissed: this was dispatched, but nobody reopened it. The ordinary orphan pass
	// sees no active agent, so keep the historical stop visible instead of counting it as active work.
	if len(agents) == 0 && len(t.Agents) > 0 {
		var stoppedAt time.Time
		for _, a := range t.Agents {
			at := a.LinkedAt
			if a.ReleasedAt != nil {
				at = *a.ReleasedAt
			} else if a.DismissedAt != nil {
				at = *a.DismissedAt
			}
			if at.After(stoppedAt) {
				stoppedAt = at
			}
		}
		if !stoppedAt.IsZero() && now.Sub(stoppedAt) >= waits.silentAfter(expect) {
			base.Kind, base.WaitingOn, base.Since, base.Reason = "stalled", "technical", stoppedAt, orphanClosed
			return base, true
		}
	}
	return prompts.BriefWait{}, false
}

func briefCardWait(ix proto.Interaction, l i18n.Lang) prompts.BriefWait {
	w := prompts.BriefWait{InteractionID: ix.ID, Since: ix.CreatedAt, WaitingOn: "owner"}
	if ix.Private {
		w.Kind = "private"
		w.Title = i18n.T(l, "assistant-briefing.private_card")
		return w
	}
	w.Kind = "question"
	if ix.Kind == proto.IxPermission {
		w.Kind = "permission"
	}
	w.Title = clip(oneLine(ix.TaskTitle), 80)
	if w.Title == "" {
		w.Title = i18n.T(l, "assistant-feishu_cards.board_from_persona")
	}
	w.Reason = boardSummary(l, ix)
	return w
}

func briefWaitLine(l i18n.Lang, w prompts.BriefWait, now time.Time, loc *time.Location) string {
	line := i18n.T(l, "assistant-briefing.wait_line", i18n.Args{"title": w.Title,
		"kind": i18n.T(l, "assistant-briefing.wait_kind_"+w.Kind), "wait": boardWait(l, boardWaited(now, w.Since))})
	if !w.Until.IsZero() {
		line = i18n.T(l, "assistant-briefing.wait_until", i18n.Args{"line": line, "time": w.Until.In(loc).Format("2006-01-02 15:04")})
	}
	if w.Reason != "" && w.Kind != "private" {
		line += " — " + w.Reason
	}
	return line
}

// briefDisabledRoutines includes disabled Facts/Report routines (the daily reports) and interval/cron patrols. Legacy
// disabled tasks have no DisabledAt: their line says so, never substituting LastRun. Ordinary one-off follow-ups and
// unrelated daily reminders stay out.
func (s *Service) briefDisabledRoutines(ctx context.Context, now time.Time, l i18n.Lang, names map[string]string) []prompts.BriefDisabledRoutine {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return nil
	}
	defer sc.mu.Unlock()
	cfg := s.settings(ctx)
	web := s.webBase(ctx)
	var out []prompts.BriefDisabledRoutine
	for _, t := range sc.tasks {
		if t.Enabled || t.Repeat == repeatOnce {
			continue
		}
		kind := ""
		switch {
		case t.Facts || t.Report:
			kind = "report"
		case t.Repeat == repeatInterval || t.Repeat == repeatCron:
			kind = "patrol"
		default:
			continue
		}
		direction := t.SessionID
		if direction == "" {
			direction = cfg.Main
		}
		name := names[direction]
		if name == "" {
			name = direction
		}
		r := prompts.BriefDisabledRoutine{ID: t.ID, Title: t.labelIn(l), Direction: name, Kind: kind,
			Link: sessionLink(web, t.RunMachineID, t.RunSessionID, true)}
		if !t.DisabledAt.IsZero() {
			at := t.DisabledAt
			r.DisabledAt = &at
			r.Line = i18n.T(l, "assistant-briefing.disabled_line", i18n.Args{"title": r.Title, "direction": r.Direction,
				"wait": boardWait(l, boardWaited(now, at))})
		} else {
			r.Line = i18n.T(l, "assistant-briefing.disabled_unknown", i18n.Args{"title": r.Title, "direction": r.Direction})
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b prompts.BriefDisabledRoutine) int {
		if c := strings.Compare(a.Direction, b.Direction); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// briefFallbackID is stable across run IDs and retries: it names the routine and the one scheduled occurrence.
func briefFallbackID(run ScheduleRun) string {
	sum := sha256.Sum256([]byte(run.RefID + "\x00" + run.Due.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}
