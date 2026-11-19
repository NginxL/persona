package devices

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Token usage history (docs/server/machines.md "Token usage history"): the calendar of tokens a day on the assistant's
// home and its page's last week by the hour, every machine's usage summed in the user's zone. The same sums as
// today's tokens (every kind, cache reads included), from the rows usageLoop keeps: past a week an hour a row, for
// usageHistoryKeep.

const (
	// usageHistoryKeep: 53 weeks of hour rows and a day, so a year's calendar starts full.
	usageHistoryKeep = (53*7 + 1) * 24 * time.Hour
	// usageDaysMax is the most days GET /api/v1/usage/days gives (53 weeks); usageDaysDefault without ?days.
	usageDaysMax     = 53 * 7
	usageDaysDefault = 20 * 7
	// usageHoursMax is the most days GET /api/v1/usage/hours gives by the hour (the ten-minute rows' week).
	usageHoursMax = 7
)

// usageHistoryStore keeps the history: rolls the ten-minute rows past a week up into hours (db.RollupUsageHours) and
// sums spans of time (db.UsageSpans). A usageStore without it drops the rows past a week.
type usageHistoryStore interface {
	RollupUsageHours(ctx context.Context, before time.Time) (int64, error)
	UsageSpans(ctx context.Context, bounds []time.Time, by db.UsageBy) ([]db.UsageSpan, error)
}

// UsageDay is one day of the calendar in the user's zone.
type UsageDay struct {
	Date string `json:"date"` // 2006-01-02
	UsageTokens
}

// UsageDays is GET /api/v1/usage/days: the last days, oldest first; the last is today.
type UsageDays struct {
	Zone string     `json:"zone"`
	Days []UsageDay `json:"days"`
	// Stored: the manager keeps the history (Postgres); false: only what is in memory (the last 26 hours).
	Stored bool `json:"stored"`
}

// UsageHour is one hour on the clock in the user's zone: a day with a clock change has 23 or 25 of them (the hour
// that comes twice is there twice).
type UsageHour struct {
	T    int64  `json:"t"`    // Unix seconds of its start
	Date string `json:"date"` // its day, 2006-01-02
	Hour int    `json:"hour"` // 0–23 on the clock
	UsageTokens
}

// UsageMachineShare is one machine's sums over the hours.
type UsageMachineShare struct {
	ID   string `json:"id"`
	Name string `json:"name"` // "" for a machine no longer here
	UsageTokens
}

// UsageAgentShare is one agent's sums over the hours.
type UsageAgentShare struct {
	// Agent: proto.Session.Agent's names ("claude", "codex", …); "" for usage stored before agents were kept apart, or
	// from an older node.
	Agent string `json:"agent"`
	UsageTokens
}

// UsageHours is GET /api/v1/usage/hours: the last days by the hour, oldest first (the last is the hour now), each
// machine's and each agent's share of them (largest first) and their total.
type UsageHours struct {
	Zone     string              `json:"zone"`
	Hours    []UsageHour         `json:"hours"`
	Machines []UsageMachineShare `json:"machines"`
	Agents   []UsageAgentShare   `json:"agents"`
	Total    UsageTokens         `json:"total"`
	Stored   bool                `json:"stored"`
}

// usageDayBounds are the starts of the last n days in loc and the start of tomorrow (n+1 times).
func usageDayBounds(now time.Time, loc *time.Location, n int) []time.Time {
	y, m, d := now.In(loc).Date()
	out := make([]time.Time, n+1)
	for i := range out {
		out[i] = time.Date(y, m, d-n+1+i, 0, 0, 0, 0, loc)
	}
	return out
}

// usageHourBounds are the starts of the hours from the start of the day n-1 days before now's in loc to the hour
// after now's: an hour of time apart, so a clock change gives its day 23 or 25.
func usageHourBounds(now time.Time, loc *time.Location, n int) []time.Time {
	start := usageDayBounds(now, loc, n)[0]
	end := now.Truncate(time.Hour).Add(time.Hour) // the hour after now's (a zone off by a half hour: its own hours)
	if off := start.Sub(start.Truncate(time.Hour)); off != 0 {
		end = now.Add(-off).Truncate(time.Hour).Add(off + time.Hour)
	}
	var out []time.Time
	for t := start; !t.After(end); t = t.Add(time.Hour) {
		out = append(out, t)
	}
	return out
}

// usageSpans sums every machine's usage into the spans between bounds: from Postgres, or from memory (the last 26
// hours) without it. stored: from Postgres.
func (d *Devices) usageSpans(ctx context.Context, bounds []time.Time, by db.UsageBy) (spans []db.UsageSpan, stored bool, err error) {
	if st, ok := d.opts.Store.(usageHistoryStore); ok {
		spans, err = st.UsageSpans(ctx, bounds, by)
		return spans, true, err
	}
	return d.usage.spans(bounds, by), false, nil
}

// spans is UsageSpans over the buckets in memory.
func (u *usageBook) spans(bounds []time.Time, by db.UsageBy) []db.UsageSpan {
	if len(bounds) < 2 {
		return nil
	}
	type key struct {
		span           int
		machine, agent string
	}
	sums := map[key]*db.UsageSpan{}
	u.mu.Lock()
	for id, m := range u.machines {
		for k, b := range m.buckets {
			t := time.Unix(k.minute, 0)
			if t.Before(bounds[0]) || !t.Before(bounds[len(bounds)-1]) {
				continue
			}
			i := sort.Search(len(bounds), func(i int) bool { return bounds[i].After(t) }) - 1
			kk := key{span: i}
			switch by {
			case db.UsageByMachine:
				kk.machine = id
			case db.UsageByAgent:
				kk.agent = k.agent
			}
			s := sums[kk]
			if s == nil {
				s = &db.UsageSpan{Span: i, Machine: kk.machine, Agent: kk.agent}
				sums[kk] = s
			}
			s.Input += b.Input
			s.CacheWrite += b.CacheWrite
			s.CacheRead += b.CacheRead
			s.Output += b.Output
			s.Requests += b.Requests
		}
	}
	u.mu.Unlock()
	out := make([]db.UsageSpan, 0, len(sums))
	for _, s := range sums {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b db.UsageSpan) int {
		return cmp.Or(cmp.Compare(a.Span, b.Span), cmp.Compare(a.Machine, b.Machine), cmp.Compare(a.Agent, b.Agent))
	})
	return out
}

func spanTokens(s db.UsageSpan) UsageTokens {
	return UsageTokens{Input: s.Input, CacheWrite: s.CacheWrite, CacheRead: s.CacheRead, Output: s.Output, Requests: s.Requests}
}

func (t *UsageTokens) plus(o UsageTokens) {
	t.Input += o.Input
	t.CacheWrite += o.CacheWrite
	t.CacheRead += o.CacheRead
	t.Output += o.Output
	t.Requests += o.Requests
}

// usageDays sums the last n days in the user's zone.
func (d *Devices) usageDays(ctx context.Context, n int, now time.Time) (UsageDays, error) {
	loc := d.location()
	bounds := usageDayBounds(now, loc, n)
	spans, stored, err := d.usageSpans(ctx, bounds, db.UsageSum)
	if err != nil {
		return UsageDays{}, err
	}
	out := UsageDays{Zone: loc.String(), Days: make([]UsageDay, n), Stored: stored}
	for i := range out.Days {
		out.Days[i].Date = bounds[i].Format(time.DateOnly)
	}
	for _, s := range spans {
		if s.Span >= 0 && s.Span < n {
			out.Days[s.Span].plus(spanTokens(s))
		}
	}
	return out, nil
}

// usageHours sums the last n days in the user's zone by the hour, and each machine's and each agent's share of them.
func (d *Devices) usageHours(ctx context.Context, n int, now time.Time) (UsageHours, error) {
	loc := d.location()
	bounds := usageHourBounds(now, loc, n)
	spans, stored, err := d.usageSpans(ctx, bounds, db.UsageByMachine)
	if err != nil {
		return UsageHours{}, err
	}
	byAgent, _, err := d.usageSpans(ctx, bounds, db.UsageByAgent)
	if err != nil {
		return UsageHours{}, err
	}
	out := UsageHours{Zone: loc.String(), Hours: make([]UsageHour, len(bounds)-1), Machines: []UsageMachineShare{},
		Agents: []UsageAgentShare{}, Stored: stored}
	for i := range out.Hours {
		t := bounds[i].In(loc)
		out.Hours[i] = UsageHour{T: t.Unix(), Date: t.Format(time.DateOnly), Hour: t.Hour()}
	}
	shares := map[string]*UsageMachineShare{}
	for _, s := range spans {
		if s.Span < 0 || s.Span >= len(out.Hours) {
			continue
		}
		tok := spanTokens(s)
		out.Hours[s.Span].plus(tok)
		out.Total.plus(tok)
		sh := shares[s.Machine]
		if sh == nil {
			sh = &UsageMachineShare{ID: s.Machine}
			if m := d.machineByID(s.Machine); m != nil {
				sh.Name = m.cfg.Name
			}
			shares[s.Machine] = sh
		}
		sh.plus(tok)
	}
	for _, sh := range shares {
		out.Machines = append(out.Machines, *sh)
	}
	slices.SortFunc(out.Machines, func(a, b UsageMachineShare) int {
		return cmp.Or(cmp.Compare(b.Total(), a.Total()), cmp.Compare(a.ID, b.ID))
	})
	agents := map[string]*UsageAgentShare{}
	for _, s := range byAgent {
		if s.Span < 0 || s.Span >= len(out.Hours) {
			continue
		}
		sh := agents[s.Agent]
		if sh == nil {
			sh = &UsageAgentShare{Agent: s.Agent}
			agents[s.Agent] = sh
		}
		sh.plus(spanTokens(s))
	}
	for _, sh := range agents {
		out.Agents = append(out.Agents, *sh)
	}
	slices.SortFunc(out.Agents, func(a, b UsageAgentShare) int {
		return cmp.Or(cmp.Compare(b.Total(), a.Total()), cmp.Compare(a.Agent, b.Agent))
	})
	return out, nil
}

// usageDaysParam reads ?days= (def without it), 1 to most.
func usageDaysParam(r *http.Request, def, most int) (int, error) {
	v := r.URL.Query().Get("days")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > most {
		return 0, errors.New("days is 1 to " + strconv.Itoa(most))
	}
	return n, nil
}

// usageHistoryRoutes are GET /api/v1/usage/days?days=N and GET /api/v1/usage/hours?days=N.
func (d *Devices) usageHistoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/usage/days", func(w http.ResponseWriter, r *http.Request) {
		n, err := usageDaysParam(r, usageDaysDefault, usageDaysMax)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		out, err := d.usageDays(r.Context(), n, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"usage": out})
	})
	mux.HandleFunc("GET /api/v1/usage/hours", func(w http.ResponseWriter, r *http.Request) {
		n, err := usageDaysParam(r, usageHoursMax, usageHoursMax)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		out, err := d.usageHours(r.Context(), n, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"usage": out})
	})
}
