package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- briefs in the supervision message and the daily briefing (#3822) ----

const (
	// briefDigestKey is when the briefs were last told in a briefing (a setting): what changed or ended since is told
	// in the next one, once.
	briefDigestKey = "assistant.brief_digest_at"
	// briefDigestRows is the most pieces of work each list of a briefing names; briefDigestWords how much of the
	// owner's words a row quotes. Together they bound what the briefs add to the routine's prompt.
	briefDigestRows  = 8
	briefDigestWords = 80
	// briefDigestFirst is how far back the first briefing looks, when none was told before.
	briefDigestFirst = 24 * time.Hour
)

// briefAsked is what a prompt says of who asked for brief's work: the owner's words (clipped to n runes) and their
// day in loc, or who started it and why when it was not the owner ("" by: nothing is known, nothing is said).
func briefAsked(brief TaskBrief, loc *time.Location, n int) (words, date, by, why string) {
	if brief.Request != nil {
		return clip(oneLine(brief.Request.Text), n), brief.Request.At.In(loc).Format("2006-01-02"), "", ""
	}
	date = brief.CreatedAt.In(loc).Format("2006-01-02")
	if brief.StartedBy == briefByUnknown || brief.StartedBy == briefByOwner || brief.StartedBy == "" {
		return "", date, "", ""
	}
	return "", date, brief.StartedBy, firstNonEmpty(brief.Reason, brief.TriggerTitle)
}

// briefEntry adds what task id's brief says to its entry in a supervision message: the owner's words, or who started
// the work, and the criteria scope changes dropped.
func (s *Service) briefEntry(ctx context.Context, id string, e *prompts.SuperviseEntry) {
	brief, ok := s.briefOfTodo(id)
	if !ok {
		return
	}
	e.Asked, e.AskedAt, e.Started, e.StartedWhy = briefAsked(brief, s.location(ctx), 300)
	if e.Asked == "" {
		e.AskedAt = ""
	}
	for _, c := range (&taskBrief{rec: brief.briefRecord, st: briefState{Log: brief.Log}}).dropped() {
		e.Dropped = append(e.Dropped, c.Criterion)
	}
}

// briefDigestSince is when the briefs were last told in a briefing; briefDigestFirst ago when never.
func (s *Service) briefDigestSince(ctx context.Context, now time.Time) time.Time {
	var at time.Time
	if st := s.store(); st != nil {
		if ok, err := st.Setting(ctx, briefDigestKey, &at); err != nil || !ok {
			at = time.Time{}
		}
	} else {
		s.tbriefs.mu.Lock()
		at = s.tbriefs.digestAt
		s.tbriefs.mu.Unlock()
	}
	if at.IsZero() || at.After(now) {
		return now.Add(-briefDigestFirst)
	}
	return at
}

// briefDigestTold notes that a briefing that started at now told the briefs.
func (s *Service) briefDigestTold(ctx context.Context, now time.Time) {
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, briefDigestKey, now.UTC()); err != nil {
			s.logf("brief digest: %v", err)
		}
		return
	}
	s.tbriefs.mu.Lock()
	s.tbriefs.digestAt = now.UTC()
	s.tbriefs.mu.Unlock()
}

// briefDigest is what a briefing that starts at now says of the pieces of work handed out, from their briefs. open:
// those still going that need the owner (an ask waits, a decision is theirs), are overdue, or whose scope changed
// since the last briefing; one that needs nothing and has not changed is not listed. done: those that ended since the
// last briefing, each once, with every criterion's result. Both are cut at briefDigestRows, the ones needing the owner
// first.
func (s *Service) briefDigest(ctx context.Context, now, since time.Time, loc *time.Location) (open, done []prompts.BriefTask) {
	if s.ensureTodosLoaded(ctx) != nil {
		return nil, nil
	}
	b := &s.tbriefs
	b.mu.Lock()
	b.init()
	ids := make([]string, 0, len(b.briefs))
	for id := range b.briefs {
		ids = append(ids, id)
	}
	todos := map[string][]string{} // brief → its to-dos, oldest link first
	for todo, l := range b.links {
		todos[l.Brief] = append(todos[l.Brief], todo)
	}
	for _, list := range todos {
		slices.SortFunc(list, func(x, y string) int {
			if c := b.links[x].At.Compare(b.links[y].At); c != 0 {
				return c
			}
			return strings.Compare(x, y)
		})
	}
	b.mu.Unlock()
	slices.Sort(ids)
	asks := s.todoAsks(ctx)
	type row struct {
		task prompts.BriefTask
		rank int
		at   time.Time
	}
	var opens, dones []row
	for _, id := range ids {
		// The to-do that joined last carries the work (briefTip): open, the work goes on; done, it ended with it.
		var carrier, last *Todo
		var brief TaskBrief
		for _, todo := range todos[id] {
			t, ok := s.todoNow(todo)
			if !ok {
				continue
			}
			if v, ok := s.briefOfTodo(todo); ok {
				brief = v
			}
			carrier, last = &t, nil
			if t.DoneAt != nil {
				carrier, last = nil, &t
			}
		}
		if brief.ID == "" || carrier == nil && last == nil {
			continue
		}
		task := prompts.BriefTask{Goal: clip(oneLine(brief.Goal), 60)}
		task.Words, task.Date, task.By, _ = briefAsked(brief, loc, briefDigestWords)
		if task.Words == "" && task.By == "" {
			task.By = briefByUnknown
		}
		if carrier == nil {
			if last.DoneAt.Before(since) || last.StartError != "" && brief.Result == nil {
				continue
			}
			task.Now = clip(oneLine(firstNonEmpty(last.CancelledReason, last.DoneReason)), 60)
			task.Cancelled = last.Cancelled()
			if r := brief.Result; r != nil {
				for _, c := range r.Criteria {
					task.Result = append(task.Result, prompts.BriefCriterion{Criterion: clip(c.Criterion, 60), State: c.State, Change: clip(c.Change, 60)})
				}
				for _, x := range r.AlsoDone {
					task.AlsoDone = append(task.AlsoDone, clip(x, 60))
				}
			}
			dones = append(dones, row{task: task, at: *last.DoneAt})
			continue
		}
		rank := 3
		if a, ok := asks[carrier.ID]; ok {
			task.Need, rank = clip(oneLine(a.Verdict), 80), 0
		} else if v := carrier.Supervision; v != nil && v.state(*carrier) == reviewNeedsUser && !ownerDecisionReturned(v) {
			task.Decide, rank = true, 0
		} else if p := carrier.Supervision.parked(); p != nil && v.state(*carrier) == reviewParked && p.WaitingOn == parkOwnerPriority {
			// Parked for the owner's priority call (parked.go): the one park that concerns them, said here and nowhere else.
			task.Parked, rank = clip(p.Reason, 80), 1
		}
		if brief.Overdue != nil {
			task.Overdue, rank = true, min(rank, 1)
		}
		for _, e := range brief.Log {
			if e.Kind == briefEventScope && e.At.After(since) && len(task.Scope) < 3 {
				task.Scope = append(task.Scope, clip(e.Change, 80))
				rank = min(rank, 2)
			}
		}
		if rank == 3 {
			continue // needs nothing and has not changed
		}
		task.Now, task.Status = s.briefStanding(ctx, *carrier)
		opens = append(opens, row{task: task, rank: rank, at: brief.CreatedAt})
	}
	slices.SortStableFunc(opens, func(x, y row) int {
		if x.rank != y.rank {
			return x.rank - y.rank
		}
		return x.at.Compare(y.at)
	})
	slices.SortStableFunc(dones, func(x, y row) int { return y.at.Compare(x.at) })
	for _, r := range opens {
		if len(open) < briefDigestRows {
			open = append(open, r.task)
		}
	}
	for _, r := range dones {
		if len(done) < briefDigestRows {
			done = append(done, r.task)
		}
	}
	return open, done
}

// briefStanding is where open task t stands in one line: what its latest agent's closeout says it did (with the
// closeout's status), else its state as the snapshot words it.
func (s *Service) briefStanding(ctx context.Context, t Todo) (line, status string) {
	agents := s.taskAgents(ctx, t)
	var latest proto.Session
	for _, c := range agents {
		if c.UpdatedAt.After(latest.UpdatedAt) || latest.SessionID == "" {
			latest = c
		}
	}
	if w := latest.Work; w != nil && w.Closeout != nil {
		c := w.Closeout
		parts := append([]string(nil), c.Did...)
		if c.Missing != "" {
			parts = append(parts, c.Missing)
		}
		if c.Waiting != "" {
			parts = append(parts, c.Waiting)
		}
		if line = clip(oneLine(strings.Join(parts, " ")), 100); line != "" {
			return line, c.Status
		}
	}
	return taskStateLabel(taskState(t, agents)), ""
}
