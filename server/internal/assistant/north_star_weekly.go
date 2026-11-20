package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerconf"
)

type weeklyNorthStar struct {
	ReadingDay string            `json:"reading_day"`
	TimeZone   string            `json:"time_zone"`
	Weeks      []northStarWeek   `json:"weeks"`
	Rows       []northStarRowOut `json:"rows"`
}

type northStarWeek struct {
	Start string `json:"start"`
	End   string `json:"end"`

	since time.Time
	until time.Time // exclusive: the reading day's midnight
}

type northStarRowOut struct {
	ID       string `json:"id"`
	Cells    []any  `json:"cells,omitempty"`
	Snapshot any    `json:"snapshot,omitempty"`
}

func northStarWeeklyCommand(ctx context.Context, manager, hub *db.DB, date, tz string, now time.Time) (weeklyNorthStar, error) {
	var stored struct {
		Timezone string `json:"timezone"`
	}
	if _, err := manager.Setting(ctx, settingsKey, &stored); err != nil {
		return weeklyNorthStar{}, err
	}
	loc := zoneOf(stored.Timezone)
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return weeklyNorthStar{}, err
		}
		loc = l
	}
	// Keep calendar arithmetic independent of the local clock. Midnight can be missing.
	y, m, d := now.In(loc).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	reading := today
	if date != "" {
		d, err := time.Parse(time.DateOnly, date)
		if err != nil {
			return weeklyNorthStar{}, err
		}
		reading = d
	}
	if reading.After(today) {
		return weeklyNorthStar{}, errors.New("date in the future")
	}
	out := weeklyNorthStar{
		ReadingDay: reading.Format(time.DateOnly),
		TimeZone:   loc.String(),
		Weeks:      make([]northStarWeek, 4),
		Rows:       make([]northStarRowOut, 0),
	}
	first := reading.AddDate(0, 0, -28)
	localStart := func(day time.Time) time.Time {
		y, m, d := day.Date()
		return dayStart(time.Date(y, m, d, 12, 0, 0, 0, loc))
	}
	days := make([][]NorthStar, len(out.Weeks))
	for i := range out.Weeks {
		since := first.AddDate(0, 0, i*7)
		until := since.AddDate(0, 0, 7)
		out.Weeks[i] = northStarWeek{
			Start: since.Format(time.DateOnly),
			End:   until.AddDate(0, 0, -1).Format(time.DateOnly),
			since: localStart(since),
			until: localStart(until),
		}
		days[i] = make([]NorthStar, 7)
		for day := range days[i] {
			start := localStart(since.AddDate(0, 0, day))
			end := localStart(since.AddDate(0, 0, day+1)).Add(-time.Nanosecond)
			ns, err := northStarCommandWindow(ctx, manager, hub, start, end, time.Time{}, loc)
			if err != nil {
				return weeklyNorthStar{}, err
			}
			days[i][day] = ns
		}
	}
	finished := make([][]string, len(out.Weeks))
	finishedReady := true
	for i, week := range out.Weeks {
		ids, ready, err := manager.NorthStarWeeklyFinishedTodos(ctx, week.since, week.until)
		if err != nil {
			return weeklyNorthStar{}, err
		}
		if !ready {
			finishedReady = false
			break
		}
		finished[i] = ids
	}
	plans, plansReady, err := manager.NorthStarWeeklyTaskPlans(ctx)
	if err != nil {
		return weeklyNorthStar{}, err
	}
	own, ownErr := readOwnConfig(managerconf.DefaultFile(identity.HomeDir()))
	reviewOn, dispatchOn := false, false
	if own.Agents != nil {
		reviewOn = own.Agents.Review.On()
		dispatchOn = own.Agents.Dispatch.On()
	}
	reviews, reviewsReady, err := manager.NorthStarWeeklyAutoReviews(ctx)
	if err != nil {
		return weeklyNorthStar{}, err
	}
	agents, agentsReady, err := manager.NorthStarWeeklyTodoAgents(ctx)
	if err != nil {
		return weeklyNorthStar{}, err
	}
	var dispatched []dispatchTask
	if _, err := manager.Setting(ctx, dispatchSettingKey, &dispatched); err != nil {
		return weeklyNorthStar{}, err
	}
	continuations, continuationsReady, err := manager.NorthStarWeeklyContinuations(ctx)
	if err != nil {
		return weeklyNorthStar{}, err
	}
	archives := map[string]agentArchive{}
	if _, err := manager.Setting(ctx, agentArchivesKey, &archives); err != nil {
		return weeklyNorthStar{}, err
	}
	out.Rows = []northStarRowOut{
		weeklyInterruptionsPerFinished(ctx, manager, out.Weeks, days, finished, finishedReady, now),
		weeklyWorkspacesOfClosed(finished, plans, finishedReady && plansReady),
		weeklyIndependentReview(out.Weeks, reviews, agents, ownErr == nil && reviewsReady && agentsReady, reviewOn, ownErr != nil),
		weeklyDispatchedTasks(out.Weeks, dispatched, dispatchOn, ownErr != nil),
		weeklyConversationNotFound(days, now),
		weeklyContinuations(out.Weeks, continuations, continuationsReady),
		weeklySessionsThatMayBeGone(agents, archives, agentsReady, now),
		weeklyWorkersAgentDifference(out.Weeks, agents, agentsReady),
	}
	return out, nil
}

func northStarWeeklyContains(week northStarWeek, at time.Time) bool {
	return !at.Before(week.since) && at.Before(week.until)
}

func weeklyWorkersAgentDifference(weeks []northStarWeek, agents []db.TodoAgentRow, ready bool) northStarRowOut {
	row := northStarRowOut{ID: "workers_agent_difference", Cells: make([]any, len(weeks))}
	type leader struct {
		agent string
		at    time.Time
	}
	leaders := map[string]leader{}
	for _, agent := range agents {
		if agent.Source != "leader" {
			continue
		}
		if current, ok := leaders[agent.TodoID]; !ok || agent.LinkedAt.After(current.at) {
			leaders[agent.TodoID] = leader{agent: agent.Agent, at: agent.LinkedAt}
		}
	}
	for i, week := range weeks {
		if !ready {
			row.Cells[i] = "unknown"
			continue
		}
		counts := map[string]int{"same": 0, "different": 0, "not_recorded": 0}
		workers := 0
		for _, worker := range agents {
			if worker.Source != "worker" || !northStarWeeklyContains(week, worker.LinkedAt) {
				continue
			}
			workers++
			leader, ok := leaders[worker.Via]
			switch {
			case !ok || worker.Agent == "" || leader.agent == "":
				counts["not_recorded"]++
			case worker.Agent == leader.agent:
				counts["same"]++
			default:
				counts["different"]++
			}
		}
		if workers == 0 {
			row.Cells[i] = "not recorded"
		} else {
			row.Cells[i] = map[string]any{"same": counts["same"], "different": counts["different"], "not_recorded": counts["not_recorded"], "n": workers}
		}
	}
	return row
}

func weeklySessionsThatMayBeGone(agents []db.TodoAgentRow, archives map[string]agentArchive, ready bool, now time.Time) northStarRowOut {
	row := northStarRowOut{ID: "sessions_that_may_be_gone"}
	if !ready {
		row.Snapshot = "unknown"
		return row
	}
	cutoff := now.Add(-30 * 24 * time.Hour)
	counts := map[string]int{"claude": 0, "agent_empty": 0, "reviewers": 0, "archived": 0}
	for _, agent := range agents {
		if !agent.LinkedAt.Before(cutoff) {
			continue
		}
		switch {
		case agent.Source == sourceReview:
			counts["reviewers"]++
		case agent.Agent == "claude":
			counts["claude"]++
		case agent.Agent == "":
			counts["agent_empty"]++
		}
	}
	for _, record := range archives {
		if record.Archived && record.Session.UpdatedAt.Before(cutoff) {
			counts["archived"]++
		}
	}
	row.Snapshot = map[string]any{
		"claude": counts["claude"], "agent_empty": counts["agent_empty"], "reviewers": counts["reviewers"],
		"archived": counts["archived"], "upper_bound": true,
	}
	return row
}

func weeklyContinuations(weeks []northStarWeek, linked []time.Time, ready bool) northStarRowOut {
	row := northStarRowOut{ID: "continuations", Cells: make([]any, len(weeks))}
	for i, week := range weeks {
		if !ready {
			row.Cells[i] = "unknown"
			continue
		}
		count := 0
		for _, at := range linked {
			if northStarWeeklyContains(week, at) {
				count++
			}
		}
		if count == 0 {
			row.Cells[i] = "not recorded"
		} else {
			row.Cells[i] = count
		}
	}
	return row
}

func weeklyDispatchedTasks(weeks []northStarWeek, tasks []dispatchTask, enabled, configUnknown bool) northStarRowOut {
	row := northStarRowOut{ID: "dispatched_tasks", Cells: make([]any, len(weeks))}
	oldest, latest := time.Time{}, time.Time{}
	for _, task := range tasks {
		if oldest.IsZero() || task.At.Before(oldest) {
			oldest = task.At
		}
		if latest.IsZero() || task.At.After(latest) {
			latest = task.At
		}
	}
	if len(tasks) < dispatchKeepMost {
		oldest = time.Time{}
	}
	for i, week := range weeks {
		switch {
		case configUnknown:
			row.Cells[i] = "unknown"
		case !enabled:
			row.Cells[i] = "not enabled"
		case !latest.IsZero() && !week.since.After(latest.Add(-dispatchKeep)):
			row.Cells[i] = "unknown"
		case !oldest.IsZero() && oldest.After(week.since):
			row.Cells[i] = "unknown"
		default:
			count := 0
			for _, task := range tasks {
				if northStarWeeklyContains(week, task.At) {
					count++
				}
			}
			row.Cells[i] = count
		}
	}
	return row
}

func weeklyIndependentReview(weeks []northStarWeek, rawReviews []json.RawMessage, agents []db.TodoAgentRow, ready, enabled, configUnknown bool) northStarRowOut {
	row := northStarRowOut{ID: "independent_review", Cells: make([]any, len(weeks))}
	if configUnknown {
		for i := range row.Cells {
			row.Cells[i] = "unknown"
		}
		return row
	}
	if !enabled {
		for i := range row.Cells {
			row.Cells[i] = "not enabled"
		}
		return row
	}
	if !ready {
		for i := range row.Cells {
			row.Cells[i] = "unknown"
		}
		return row
	}
	runs := make([]map[string]bool, len(weeks))
	results := make([]map[string]int, len(weeks))
	starts := map[string]time.Time{}
	for i := range weeks {
		runs[i] = map[string]bool{}
		results[i] = map[string]int{}
	}
	for _, raw := range rawReviews {
		var review AutoReview
		if err := json.Unmarshal(raw, &review); err != nil || review.ID == "" {
			for i := range row.Cells {
				row.Cells[i] = "unknown"
			}
			return row
		}
		if !review.StartedAt.IsZero() {
			starts[review.ID] = review.StartedAt
		}
		for i, week := range weeks {
			if review.EndedAt != nil && northStarWeeklyContains(week, *review.EndedAt) {
				results[i][review.State]++
			}
		}
	}
	for _, agent := range agents {
		if agent.Source != sourceReview || agent.Via == "" {
			continue
		}
		if _, ok := starts[agent.Via]; !ok {
			starts[agent.Via] = agent.LinkedAt
		}
	}
	for id, started := range starts {
		for i, week := range weeks {
			if northStarWeeklyContains(week, started) {
				runs[i][id] = true
			}
		}
	}
	for i := range weeks {
		row.Cells[i] = map[string]any{"by_result": results[i], "runs_started": len(runs[i]), "lower_bound": true}
	}
	return row
}

func weeklyWorkspacesOfClosed(finished [][]string, rawPlans []db.TaskPlanRow, ready bool) northStarRowOut {
	row := northStarRowOut{ID: "workspaces_of_closed_todos", Cells: make([]any, len(finished))}
	bindings := map[string]PlanTask{}
	legacyTargets := map[string]PlanTask{}
	for _, raw := range rawPlans {
		plan := planFromRow(raw)
		for _, task := range plan.Tasks {
			if task.TodoID != "" {
				bindings[task.TodoID] = task
			}
			for _, id := range []string{task.ReuseTodo, task.ReservedTodoID} {
				if id != "" {
					legacyTargets[id] = task
				}
			}
		}
	}
	for i, ids := range finished {
		if !ready {
			row.Cells[i] = "unknown"
			continue
		}
		counts := map[string]int{"created": 0, "reused": 0, "shared": 0, "existing": 0, "none": 0, "not_recorded": 0}
		for _, id := range ids {
			task, ok := bindings[id]
			if !ok {
				task, ok = legacyTargets[id]
			}
			if !ok || task.WorkspaceResolved == nil {
				counts["not_recorded"]++
				continue
			}
			resolved := task.WorkspaceResolved
			switch {
			case resolved.Created:
				counts["created"]++
			case resolved.Reused:
				counts["reused"]++
			case resolved.Shared:
				counts["shared"]++
			case task.Workspace != nil && task.Workspace.Mode == workspaceExisting:
				counts["existing"]++
			case task.Workspace != nil && task.Workspace.Mode == workspaceNone:
				counts["none"]++
			default:
				counts["not_recorded"]++
			}
		}
		row.Cells[i] = map[string]any{
			"created": counts["created"], "reused": counts["reused"], "shared": counts["shared"],
			"existing": counts["existing"], "none": counts["none"], "not_recorded": counts["not_recorded"],
			"n": len(ids), "still_there_and_clean": "not probed",
		}
	}
	return row
}

func weeklyInterruptionsPerFinished(ctx context.Context, manager *db.DB, weeks []northStarWeek, days [][]NorthStar, finished [][]string, ready bool, now time.Time) northStarRowOut {
	row := northStarRowOut{ID: "interruptions_per_finished_todo", Cells: make([]any, len(days))}
	for i, week := range days {
		if !ready || weeklyNorthStarWindowUnknown(week, now) {
			row.Cells[i] = "unknown"
			continue
		}
		total, fromTasks, reopened := 0, 0, 0
		for _, day := range week {
			total += day.Interruptions.Total
			for _, category := range day.Interruptions.Categories {
				fromTasks += category.BySource[nsFromTask]
			}
		}
		events, err := manager.WorkEventsBetween(ctx, weeks[i].since, weeks[i].until.Add(-time.Nanosecond), []string{todoReopenedEvent})
		if err != nil {
			row.Cells[i] = "unknown"
			continue
		}
		seen := map[string]bool{}
		for _, event := range events {
			wasFinished, recorded := event.Detail["was_finished"].(bool)
			if (recorded && !wasFinished) || event.Detail["done_by"] == db.DoneByCancelled {
				continue
			}
			seconds, ok := event.Detail["after"].(float64)
			todo, _ := event.Detail["todo"].(string)
			if !ok || todo == "" || seen[todo] || seconds < 0 || seconds > wrongDoneWithin.Seconds() {
				continue
			}
			seen[todo] = true
			reopened++
		}
		n := len(finished[i])
		var ratio any = "too few"
		if n >= 10 {
			ratio = float64(total) / float64(n)
		}
		var share any = "too few"
		if total > 0 {
			share = float64(fromTasks) / float64(total)
		}
		row.Cells[i] = map[string]any{
			"interruptions":       total,
			"ratio":               ratio,
			"from_tasks":          fromTasks,
			"share_from_tasks":    share,
			"reopened_within_24h": reopened,
			"n":                   n,
		}
	}
	return row
}

func weeklyNorthStarWindowUnknown(week []NorthStar, now time.Time) bool {
	if len(week) == 0 {
		return true
	}
	first := week[0]
	if first.RecordingSince == nil || first.RecordingSince.After(first.Since) {
		return true
	}
	return first.Since.Before(now.Add(-30 * 24 * time.Hour))
}

func weeklyConversationNotFound(days [][]NorthStar, now time.Time) northStarRowOut {
	row := northStarRowOut{ID: "conversation_not_found", Cells: make([]any, len(days))}
	for i, week := range days {
		if weeklyNorthStarWindowUnknown(week, now) {
			row.Cells[i] = "unknown"
			continue
		}
		counts := map[string]int{roleMain: 0, roleDot: 0}
		for _, day := range week {
			for role, failures := range day.Availability.FailuresByRole {
				if role == roleMain || role == roleDot {
					counts[role] += failures["conversation_not_found"]
				}
			}
		}
		row.Cells[i] = map[string]any{
			"main":        counts[roleMain],
			"leaders":     counts[roleDot],
			"lower_bound": true,
		}
	}
	return row
}
