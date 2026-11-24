package assistant

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The manager owns intake; the hub only authenticates and forwards these routes.
type healthIntakeStore interface {
	HealthIntakeAdd(context.Context, db.HealthIntakeRow) (db.HealthIntakeRow, bool, error)
	HealthIntakeUndo(context.Context, string) (*db.HealthIntakeRow, bool, error)
	HealthIntakeRange(context.Context, time.Time, time.Time) ([]db.HealthIntakeRow, error)
	HealthIntakeChanges(context.Context, int64, int) ([]db.HealthIntakeRow, error)
}

type healthIntakeRequest struct {
	ID     string     `json:"id"`
	Kind   string     `json:"kind"`
	Amount *float64   `json:"amount"`
	At     *time.Time `json:"at"`
	Note   string     `json:"note"`
}

func (v healthIntakeRequest) entry(settings healthIntakeSettings, now time.Time) (db.HealthIntakeRow, error) {
	r := db.HealthIntakeRow{ID: strings.ToLower(v.ID), Kind: v.Kind, At: now.UTC(), Note: v.Note}
	if v.At != nil {
		r.At = v.At.UTC()
	}
	if v.Amount != nil {
		r.Amount = *v.Amount
	} else if v.Kind == "water" {
		r.Amount = float64(settings.Water.Cup)
	}
	rounded := math.Round(r.Amount*10) / 10
	if !workoutUUID.MatchString(r.ID) ||
		len(r.Note) > 4096 || strings.ContainsRune(r.Note, '\x00') ||
		math.IsNaN(r.Amount) || math.IsInf(r.Amount, 0) || rounded <= 0 ||
		math.Abs(r.Amount*10-math.Round(r.Amount*10)) > 1e-7 ||
		(r.Kind != "water" && r.Kind != "protein") ||
		(r.Kind == "water" && (r.Amount < 1 || r.Amount > 3000)) || (r.Kind == "protein" && (r.Amount < 0.1 || r.Amount > 200)) {
		return r, i18n.Err("assistant-health_intake.invalid_entry")
	}
	r.Amount = rounded
	return r, nil
}

type healthIntakeKind struct {
	Today   float64              `json:"today"`
	Goal    int                  `json:"goal"`
	Entries []db.HealthIntakeRow `json:"entries"`
}

type healthIntakeDay struct {
	Date    string  `json:"date"`
	Water   float64 `json:"water"`
	Protein float64 `json:"protein"`
}

type healthIntakeView struct {
	WaterExtension string            `json:"water_extension,omitempty"`
	Water          healthIntakeKind  `json:"water"`
	Protein        healthIntakeKind  `json:"protein"`
	Days           []healthIntakeDay `json:"days"`
	Zone           string            `json:"zone"`
}

type healthIntakeTotals struct {
	Water   float64 `json:"water"`
	Protein float64 `json:"protein"`
}

type healthIntakeMutation struct {
	Entry *db.HealthIntakeRow `json:"entry,omitempty"`
	Today healthIntakeTotals  `json:"today"`
	Zone  string              `json:"zone"`
}

// sumHealthIntake includes empty dates and today's entries. Tenths avoid accumulating floating
// point error from the database's numeric(7,1) measurements. AddDate keeps DST days intact.
func sumHealthIntake(rows []db.HealthIntakeRow, settings healthIntakeSettings, now time.Time, loc *time.Location, days int) healthIntakeView {
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	first := today.AddDate(0, 0, 1-days)
	v := healthIntakeView{
		Water:   healthIntakeKind{Goal: settings.Water.Goal, Entries: []db.HealthIntakeRow{}},
		Protein: healthIntakeKind{Goal: settings.Protein.Goal, Entries: []db.HealthIntakeRow{}},
		Days:    make([]healthIntakeDay, days), Zone: loc.String(),
	}
	byDate := map[string]int{}
	water, protein := make([]int64, days), make([]int64, days)
	for i := range v.Days {
		date := first.AddDate(0, 0, i).Format(time.DateOnly)
		v.Days[i].Date, byDate[date] = date, i
	}
	for _, r := range rows {
		i, ok := byDate[r.At.In(loc).Format(time.DateOnly)]
		if !ok || r.DeletedAt != nil {
			continue
		}
		switch r.Kind {
		case "water":
			water[i] += int64(math.Round(r.Amount * 10))
			if i == days-1 {
				v.Water.Entries = append(v.Water.Entries, r)
			}
		case "protein":
			protein[i] += int64(math.Round(r.Amount * 10))
			if i == days-1 {
				v.Protein.Entries = append(v.Protein.Entries, r)
			}
		}
	}
	for i := range v.Days {
		v.Days[i].Water, v.Days[i].Protein = float64(water[i])/10, float64(protein[i])/10
	}
	v.Water.Today, v.Protein.Today = v.Days[days-1].Water, v.Days[days-1].Protein
	return v
}

func (s *Service) healthIntakeView(ctx context.Context, st healthIntakeStore, settings healthIntakeSettings, days int) (healthIntakeView, error) {
	loc, now := s.healthIntakeLocation(ctx), time.Now()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	rows, err := st.HealthIntakeRange(ctx, today.AddDate(0, 0, 1-days), today.AddDate(0, 0, 1))
	if err != nil {
		return healthIntakeView{}, err
	}
	return sumHealthIntake(rows, settings, now, loc, days), nil
}

// intakeAccess keeps the watch's exception narrow even when called directly on the manager.
// Extension, read-only and scoped credentials never read or write Health intake.
func intakeAccess(w http.ResponseWriter, r *http.Request, watch bool) bool {
	if fullRole(r) || watch && r.Header.Get(RoleHeader) == "answer" {
		return true
	}
	apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-health_intake.forbidden"))
	return false
}

func (s *Service) intakeStore(w http.ResponseWriter) healthIntakeStore {
	st, _ := s.store().(healthIntakeStore)
	if st == nil {
		intakeUnavailable(w)
	}
	return st
}

func intakeUnavailable(w http.ResponseWriter) {
	apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("assistant-health_intake.unavailable"))
}

func intakeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func intakeQueryInt(r *http.Request, name string, fallback, minimum, maximum int64) (int64, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return fallback, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= minimum && n <= maximum
}

func (s *Service) intakeRoutes() {
	s.handle("GET /api/v1/assistant/health/intake", s.getHealthIntake)
	s.handle("POST /api/v1/assistant/health/intake", s.postHealthIntake)
	s.handle("DELETE /api/v1/assistant/health/intake/{id}", s.undoHealthIntake)
	s.handle("GET /api/v1/assistant/health/intake/changes", s.healthIntakeChanges)
	s.handle("POST /api/v1/assistant/health/intake/ui", s.healthIntakeUI)
	s.handle("POST /api/v1/assistant/health/intake/import", s.importHealthWater)
	s.handle("POST /api/v1/assistant/health/water/reminder", s.waterReminderAction)
}

func (s *Service) getHealthIntake(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, true) {
		return
	}
	days, ok := intakeQueryInt(r, "days", 7, 1, 366)
	if !ok {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_query"))
		return
	}
	st := s.intakeStore(w)
	if st == nil {
		return
	}
	settings, err := s.healthIntakeSettings(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	v, err := s.healthIntakeView(r.Context(), st, settings, int(days))
	if err != nil {
		intakeUnavailable(w)
		return
	}
	v.WaterExtension, err = s.pendingWaterExtension(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	intakeJSON(w, v)
}

func (s *Service) postHealthIntake(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, true) {
		return
	}
	var req healthIntakeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&req); err != nil {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_entry"))
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_entry"))
		return
	}
	settings, err := s.healthIntakeSettings(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	v, err := req.entry(settings, time.Now())
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	st := s.intakeStore(w)
	if st == nil {
		return
	}
	saved, _, err := s.addHealthIntake(r.Context(), st, v)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	s.intakeMutationReply(w, r, st, settings, &saved)
}

func (s *Service) undoHealthIntake(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, false) {
		return
	}
	id := r.PathValue("id")
	if !workoutUUID.MatchString(id) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_entry"))
		return
	}
	settings, err := s.healthIntakeSettings(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	st := s.intakeStore(w)
	if st == nil {
		return
	}
	saved, changed, err := st.HealthIntakeUndo(r.Context(), strings.ToLower(id))
	if err != nil {
		intakeUnavailable(w)
		return
	}
	if saved == nil {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-health_intake.not_found"))
		return
	}
	if changed {
		s.event(r.Context(), map[string]any{"type": "health_intake"})
	}
	s.intakeMutationReply(w, r, st, settings, nil)
}

func (s *Service) intakeMutationReply(w http.ResponseWriter, r *http.Request, st healthIntakeStore, settings healthIntakeSettings, entry *db.HealthIntakeRow) {
	v, err := s.healthIntakeView(r.Context(), st, settings, 1)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	intakeJSON(w, healthIntakeMutation{Entry: entry, Today: healthIntakeTotals{v.Water.Today, v.Protein.Today}, Zone: v.Zone})
}

func (s *Service) healthIntakeChanges(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, false) {
		return
	}
	after, a := intakeQueryInt(r, "after", 0, 0, math.MaxInt64)
	limit, b := intakeQueryInt(r, "limit", 100, 1, 1000)
	if !a || !b {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_query"))
		return
	}
	st := s.intakeStore(w)
	if st == nil {
		return
	}
	rows, err := st.HealthIntakeChanges(r.Context(), after, int(limit)+1)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	more := len(rows) > int(limit)
	if more {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []db.HealthIntakeRow{}
	}
	next := after
	if len(rows) > 0 {
		next = rows[len(rows)-1].Seq
	}
	intakeJSON(w, struct {
		Changes []db.HealthIntakeRow `json:"changes"`
		Next    int64                `json:"next"`
		HasMore bool                 `json:"has_more"`
	}{rows, next, more})
}
