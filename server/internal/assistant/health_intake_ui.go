package assistant

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// As with -subscriptions/pages, no extension may own the reserved -health-intake/pages.
// The initial state selects water or protein; returned state is opaque to the client.
type intakePageState struct {
	Kind   string `json:"kind"`
	Days   int    `json:"days"`
	Offset int    `json:"offset,omitempty"`
	Edit   bool   `json:"edit,omitempty"`
}

func readIntakePage(raw string) (intakePageState, bool) {
	p := intakePageState{Kind: "water", Days: 7}
	if raw != "" && json.Unmarshal([]byte(raw), &p) != nil {
		return p, false
	}
	if p.Days != 30 {
		p.Days = 7
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p, p.Kind == "water" || p.Kind == "protein"
}

func (s *Service) healthIntakeUI(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, false) {
		return
	}
	var req wire.UIRequest
	if !decodeIntakeJSON(w, r, &req) {
		return
	}
	if strings.HasPrefix(req.Action, "undo:") {
		req.Action, req.Values = "undo", map[string]string{"id": strings.TrimPrefix(req.Action, "undo:")}
	}
	p, ok := readIntakePage(req.State)
	if !ok {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_page"))
		return
	}
	s.intakeMu.Lock()
	defer s.intakeMu.Unlock()
	st := s.intakeStore(w)
	if st == nil {
		return
	}
	c, err := s.healthIntakeSettings(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	l := i18n.FromRequest(r).Or(s.lang(r.Context()))
	version := extension.UIVersion
	if req.Context != nil {
		l = i18n.Lang(req.Context.Locale).Or(l)
		if req.Context.UIVersion > 0 {
			version = min(req.Context.UIVersion, extension.UIVersion)
		}
	}
	now, toast := time.Now(), ""
	recordedAmount := 0.0
	bad := func(key string) { apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake."+key)) }
	switch req.Action {
	case "":
	case "settings":
		p.Edit = true
	case "back":
		p.Edit = false
	case "range":
		p.Days = 7
		if req.Values["days"] == "30" {
			p.Days = 30
		}
	case "older":
		// A request may contain arbitrary state; cap before addition to avoid overflow.
		p.Offset = min(p.Offset, math.MaxInt-40) + 40
	case "latest":
		p.Offset = 0
	case "save":
		next, err := intakePageSettings(c, p.Kind, req.Values)
		if err != nil {
			bad("invalid_settings")
			return
		}
		if err := s.putIntakeSetting(r.Context(), healthIntakeKey, next); err != nil {
			intakeUnavailable(w)
			return
		}
		c, p.Edit, toast = next, false, intakeText(l, "toast.saved")
		s.event(r.Context(), map[string]any{"type": "health_intake"})
	case "drink", "log":
		if req.Action == "drink" && p.Kind != "water" {
			bad("invalid_page")
			return
		}
		amount := float64(c.Water.Cup)
		if req.Action == "log" {
			if p.Kind == "water" {
				n, ok := waterAmount(req.Values["amount"])
				if !ok {
					bad("invalid_amount")
					return
				}
				amount = float64(n)
			} else {
				var err error
				amount, err = strconv.ParseFloat(strings.TrimSpace(req.Values["amount"]), 64)
				if err != nil {
					bad("invalid_entry")
					return
				}
			}
		}
		v, err := (healthIntakeRequest{ID: healthIntakeUUID("intake:" + randomHex(16)), Kind: p.Kind, Amount: &amount, Note: req.Values["note"]}).entry(c, now)
		if err != nil {
			bad("invalid_entry")
			return
		}
		_, _, err = s.addHealthIntakeLocked(r.Context(), st, v)
		if err != nil {
			intakeUnavailable(w)
			return
		}
		p.Offset, toast = 0, intakeText(l, "toast.recorded")
		recordedAmount = amount
	case "undo":
		id := req.Values["id"]
		if !workoutUUID.MatchString(id) {
			bad("invalid_entry")
			return
		}
		// The page lists today's entries of this kind. Do not let a stale or forged row
		// remove another page's record; the standalone DELETE route may undo any date.
		v, err := s.healthIntakeView(r.Context(), st, c, 1)
		if err != nil {
			intakeUnavailable(w)
			return
		}
		entries := v.Water.Entries
		if p.Kind == "protein" {
			entries = v.Protein.Entries
		}
		found := false
		for _, e := range entries {
			found = found || strings.EqualFold(e.ID, id)
		}
		toast = intakeText(l, "not_found")
		if found {
			_, changed, err := st.HealthIntakeUndo(r.Context(), strings.ToLower(id))
			if err != nil {
				intakeUnavailable(w)
				return
			}
			if changed {
				s.event(r.Context(), map[string]any{"type": "health_intake"})
			}
			toast = intakeText(l, "toast.undone")
		}
	default:
		bad("invalid_page")
		return
	}
	v, err := s.healthIntakeView(r.Context(), st, c, p.Days)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	if p.Kind == "water" && recordedAmount > 0 {
		toast = intakeWaterToast(l, c.Water, recordedAmount, v.Water.Today)
	}
	reminder, err := s.waterReminderState(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	view := drawIntakePage(p, c, v, reminder, now, s.healthIntakeLocation(r.Context()), l, version, s.hello(r.Context()).QuietPush)
	slot, _ := extension.Slot("extension_page")
	if _, err := extension.Fit(&view, slot, version, l); err != nil {
		s.logf("health intake page: %v", err)
		intakeUnavailable(w)
		return
	}
	intakeJSON(w, wire.ActionResult{View: &view, Toast: toast})
}

func intakePageSettings(c healthIntakeSettings, kind string, values map[string]string) (healthIntakeSettings, error) {
	invalid := i18n.Err("assistant-health_intake.invalid_settings")
	if len(values) == 0 {
		return c, invalid
	}
	for key, value := range values {
		var target *int
		if kind == "protein" {
			if key != "goal_g" {
				return c, invalid
			}
			target = &c.Protein.Goal
		} else {
			switch key {
			case "goal_ml":
				target = &c.Water.Goal
			case "cup_ml":
				target = &c.Water.Cup
			case "interval_minutes":
				target = &c.Water.Interval
			case "start":
				c.Water.Start = value
			case "end":
				c.Water.End = value
			case "remind", "weekends", "stop_when_done":
				if value != "true" && value != "false" {
					return c, invalid
				}
				on := value == "true"
				switch key {
				case "remind":
					c.Water.Remind = on
				case "weekends":
					c.Water.Weekends = on
				default:
					c.Water.Stop = on
				}
			default:
				return c, invalid
			}
		}
		if target != nil {
			n, err := strconv.Atoi(value)
			if err != nil {
				return c, invalid
			}
			*target = n
		}
	}
	return c, c.validate()
}
