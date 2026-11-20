package assistant

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The To-dos view (#10226): what the manager works out for each to-do and for the list, by the pure projection in
// todo/projection (its rules: spec/fixtures/todo-view and spec/fixtures/todo-counts). This file is the adapter: it reads
// the to-dos as GET /api/v1/todos lists them, their runs' sessions and the owner's zone once per read, and hands them
// over as projection.Facts; the places also read the knowledge base's project titles. The routes call it once per read
// (todo_view_routes.go). The legacy triage (todo_triage.go) keeps placing to-dos for the direction tree and leader
// reports until they read the view.

// todoProjection is a list's facts and their views, each ranked within its section; facts[i] and views[i] are the
// list's i-th row.
type todoProjection struct {
	facts []projection.Facts
	views []assistantwire.TodoSummary
}

// todoProjectionOf works out the view of every to-do of list at now. list holds the rows as GET /api/v1/todos lists
// them (formatTodo, with ask, decision_review and orphaned). The sessions read are those of the active runs of the
// to-dos the projection reads as open (a failed start included, whose runs still date its last activity), each
// computer asked once (todoRunSessions), and with them the pending cards of those runs (todoFactsOf).
func (s *Service) todoProjectionOf(ctx context.Context, list []listedTodo, now time.Time) todoProjection {
	facts := s.todoFactsOf(ctx, list)
	return todoProjection{facts: facts, views: projection.Views(facts, now)}
}

// hasParkInteractionLink says whether this park's projection must retain its workers' pending cards, even after release.
func hasParkInteractionLink(v *Supervision) bool {
	return v != nil && v.State == reviewParked && v.Parked != nil && v.Parked.Evidence != nil && v.Parked.Evidence.Kind == parkExternalEvent && v.Parked.Evidence.InteractionID != ""
}

// todoFactsOf are the rows of list as the projection reads them, with their active runs' sessions and those runs'
// pending cards (todoRunCards), which say what a run that asks is asking (todoProjectionOf; todo_view_push.go works
// out the views of a part of the list from them).
func (s *Service) todoFactsOf(ctx context.Context, list []listedTodo) []projection.Facts {
	want := map[string]map[string]bool{} // by computer, the session ids of the open to-dos' active runs
	for i := range list {
		t := &list[i]
		linked := hasParkInteractionLink(t.Supervision)
		if t.DoneAt != nil && t.StartError == "" {
			continue
		}
		for _, a := range t.Agents {
			if a.ReleasedAt != nil && !linked || a.DismissedAt != nil {
				continue
			}
			if want[a.MachineID] == nil {
				want[a.MachineID] = map[string]bool{}
			}
			want[a.MachineID][a.SessionID] = true
		}
	}
	sessions, failed := map[[2]string]proto.Session{}, map[string]bool{}
	for machine, ids := range want {
		l, err := s.todoRunSessions(ctx, machine, ids)
		if err != nil {
			failed[machine] = true
			continue
		}
		for _, c := range l {
			c.MachineID = machine
			sessions[[2]string{machine, c.SessionID}] = c
		}
	}
	cards := s.todoRunCards(want)
	loc := s.location(ctx)
	facts := make([]projection.Facts, len(list))
	for i := range list {
		t := &list[i]
		linked := hasParkInteractionLink(t.Supervision)
		facts[i] = projectionFacts(*t, sessions, loc)
		if t.DoneAt != nil && t.StartError == "" {
			continue
		}
		for _, a := range t.Agents {
			if a.ReleasedAt != nil && !linked || a.DismissedAt != nil {
				continue
			}
			// A computer that did not answer lists nothing: its runs read as offline, and the to-do gets no Inactive
			// mark.
			facts[i].Unreached = facts[i].Unreached || failed[a.MachineID]
			facts[i].Interactions = append(facts[i].Interactions, cards[[2]string{a.MachineID, a.SessionID}]...)
		}
	}
	return facts
}

// todoRunCards are the pending cards of the sessions in want (by computer, the session ids), by computer and
// session, oldest first. A health conversation's card is marked Private on this copy, as the hub's push treats it
// (hub/push_payload.go askQuestion), so the view lets neither be answered from a row or a lock screen.
func (s *Service) todoRunCards(want map[string]map[string]bool) map[[2]string][]proto.Interaction {
	if len(want) == 0 {
		return nil
	}
	cards := s.pendingOf(want)
	for _, l := range cards {
		for i := range l {
			l[i].Private = l[i].Private || identity.InHealthSpace(l[i].Cwd)
		}
		slices.SortFunc(l, func(a, b proto.Interaction) int {
			if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
	}
	return cards
}

// todoRunSessions are those of machine's sessions whose id is in want, each as the apps get it: the node's rows read
// as listedSession reads one, and for one the node no longer lists, the archive's row (archivedRow), which reads as
// offline as in the machine's whole list. Neither the computer's other sessions nor its archive's months of records
// are copied: a list read is on every GET /api/v1/todos. Without the fleet in this process, from the node's whole list
// (sessions). An error when the computer is not there or does not answer.
func (s *Service) todoRunSessions(ctx context.Context, machine string, want map[string]bool) ([]proto.Session, error) {
	if !s.fleetOn() || machine == DeviceAll {
		list, err := s.sessions(ctx, machine)
		if err != nil {
			return nil, err
		}
		kept := make([]proto.Session, 0, len(want))
		for i := range list {
			if want[list[i].SessionID] {
				kept = append(kept, list[i])
			}
		}
		return kept, nil
	}
	m, ok := s.fleetMachine(machine)
	if !ok {
		return nil, ErrNoDevice // as sessions.list says
	}
	mark := s.archiveMark()
	rows, _ := s.nodeSessionsWhere(m.ID, func(c *proto.Session) bool { return want[c.SessionID] })
	list, err := s.archivedListed(ctx, m.ID, s.decorateInventory(m.ID, rows, false), mark)
	if err != nil {
		s.logf("load agent archives: %v", err)
	}
	if len(list) < len(want) {
		listed := make(map[string]bool, len(list))
		for i := range list {
			listed[list[i].SessionID] = true
		}
		for sid := range want {
			if listed[sid] {
				continue
			}
			row, found, err := s.archivedRow(ctx, m.ID, sid)
			if err != nil {
				s.logf("load agent archives: %v", err)
			}
			if found {
				list = append(list, row)
			}
		}
	}
	return s.deletionRows(ctx, machine, list, false), nil
}

// byID is the view of each to-do, by id.
func (p todoProjection) byID() map[string]assistantwire.TodoSummary {
	out := make(map[string]assistantwire.TodoSummary, len(p.views))
	for i, f := range p.facts {
		out[f.ID] = p.views[i]
	}
	return out
}

// counts are the list's numbers.
func (p todoProjection) counts() assistantwire.TodoCounts { return projection.Counts(p.facts, p.views) }

// todoPlaces are the list's places, each project's titled by the knowledge base (todoProjectTitles).
func (s *Service) todoPlaces(ctx context.Context, p todoProjection) assistantwire.TodoPlaces {
	return projection.Places(p.facts, p.views, s.todoProjectTitles(ctx, p.facts))
}

// todoProjectTitles are the titles of the live projects the to-dos are in, by slug, as ownProject finds them; a
// project it does not find, or a knowledge base that cannot be read, leaves the place titled by its slug.
func (s *Service) todoProjectTitles(ctx context.Context, facts []projection.Facts) map[string]string {
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil
	}
	out, looked := map[string]string{}, map[string]bool{}
	for _, f := range facts {
		if f.Project == "" || looked[f.Project] {
			continue
		}
		looked[f.Project] = true
		if p := ownProject(e, Todo{Project: f.Project}); p != nil && p.Name != "" {
			out[f.Project] = p.Name
		}
	}
	return out
}

// projectionFacts is listed row t as the projection reads it: the fields a decoder of the row's JSON would fill (the
// test TestProjectionFactsMatchTheList holds the two together), the sessions of its active runs, and the owner's zone.
func projectionFacts(t listedTodo, sessions map[[2]string]proto.Session, loc *time.Location) projection.Facts {
	linked := hasParkInteractionLink(t.Supervision)
	f := projection.Facts{ID: t.ID, Title: t.Title, DoneAt: t.DoneAt, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Origin: t.Origin, PlanID: t.PlanID, StartError: t.StartError, DoneBy: t.DoneBy, ReopenedAt: t.ReopenedAt,
		CancelledAt: t.CancelledAt, CancelledReason: t.CancelledReason, Quiet: t.Quiet, PRURL: t.PRURL,
		DueAt: t.DueAt, DueDate: t.DueDate, Priority: t.Priority, Project: t.Project, Kind: t.Kind, SeenAt: t.SeenAt,
		PartOf: t.PartOf, Loc: loc}
	f.RestoredAt, f.SomedayUntil = t.RestoredAt, t.SomedayUntil // the owner's marks on the hub (todo_marks.go)
	if t.Agents != nil {
		f.Agents = make([]projection.Agent, len(t.Agents))
	}
	for i, a := range t.Agents {
		f.Agents[i] = projection.Agent{MachineID: a.MachineID, SessionID: a.SessionID, ReleasedAt: a.ReleasedAt,
			Title: a.Title, StartError: a.StartError, DismissedAt: a.DismissedAt}
		if c, ok := sessions[[2]string{a.MachineID, a.SessionID}]; ok && (a.ReleasedAt == nil || linked) && a.DismissedAt == nil {
			f.Sessions = append(f.Sessions, c)
		}
	}
	if v := t.Supervision; v != nil {
		f.Supervision = &projection.Supervision{State: v.State, PendingSince: v.PendingSince, UpdatedAt: v.UpdatedAt}
		if p := v.Parked; p != nil {
			parked := &projection.Parked{Reason: p.Reason, Until: p.Until, WaitingOn: p.WaitingOn}
			if p.Evidence != nil && p.Evidence.InteractionID != "" {
				parked.Evidence = &projection.ParkEvidence{Kind: p.Evidence.Kind, InteractionID: p.Evidence.InteractionID}
			}
			f.Supervision.Parked = parked
		}
		if v.Reviews != nil {
			f.Supervision.Reviews = make([]projection.Review, len(v.Reviews))
		}
		for i, r := range v.Reviews {
			f.Supervision.Reviews[i] = projection.Review{At: r.At, Verdict: r.Verdict, Note: r.Note, Reason: r.Reason,
				Recommend: r.Recommend}
		}
		if v.Progress != nil {
			f.Supervision.Progress = &projection.Progress{At: v.Progress.At}
		}
	}
	if p := t.PRStatus; p != nil {
		f.PRStatus = &projection.PRStatus{URL: p.URL, State: p.State, Draft: p.Draft, CheckedAt: p.CheckedAt}
	}
	for _, p := range t.OtherPRs {
		f.OtherPRs = append(f.OtherPRs, projection.PRStatus{URL: p.URL, State: p.State, Draft: p.Draft, CheckedAt: p.CheckedAt})
	}
	if d := t.Delivery; d != nil {
		f.Delivery = &projection.Delivery{State: d.State, At: d.At}
	}
	if a := t.Ask; a != nil {
		f.Ask = &projection.Ask{Outcome: a.Outcome, Verdict: a.Verdict, Recommend: a.Recommend, AskedAt: a.AskedAt,
			Risk: a.Risk, Question: a.Question, Deadline: a.Deadline, Default: a.Default, ForBriefing: a.ForBriefing}
		for _, o := range a.Options {
			f.Ask.Options = append(f.Ask.Options, assistantwire.OwnerAskOption{Label: o.Label, LeadsTo: o.LeadsTo})
		}
	}
	if r := t.DecisionReview; r != nil {
		f.DecisionReview = &projection.DecisionReview{ReviewAt: r.ReviewAt, DismissalID: r.DismissalID}
	}
	return f
}
