package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const healthIntakeKey = "health.intake"

type healthIntakeSettings struct {
	Water   healthWaterSettings   `json:"water"`
	Protein healthProteinSettings `json:"protein"`
}

type healthWaterSettings struct {
	Goal     int    `json:"goal_ml"`
	Cup      int    `json:"cup_ml"`
	Remind   bool   `json:"remind"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Interval int    `json:"interval_minutes"`
	Weekends bool   `json:"weekends"`
	Stop     bool   `json:"stop_when_done"`
}

type healthProteinSettings struct {
	Goal int `json:"goal_g"`
}

func defaultHealthIntakeSettings() healthIntakeSettings {
	return healthIntakeSettings{
		Water:   healthWaterSettings{Goal: 2000, Cup: 250, Start: "09:00", End: "21:00", Interval: 60, Weekends: true, Stop: true},
		Protein: healthProteinSettings{Goal: 60},
	}
}

func (v healthIntakeSettings) validate() error {
	w := v.Water
	start, a := time.Parse("15:04", w.Start)
	end, b := time.Parse("15:04", w.End)
	if w.Goal < 500 || w.Goal > 6000 || w.Cup < 50 || w.Cup > 1000 || w.Interval < 15 || w.Interval > 240 ||
		v.Protein.Goal < 10 || v.Protein.Goal > 300 || a != nil || b != nil ||
		start.Format("15:04") != w.Start || end.Format("15:04") != w.End || !start.Before(end) {
		return i18n.Err("assistant-health_intake.invalid_settings")
	}
	return nil
}

// Read over defaults so newly introduced fields have defaults too. Never turn a storage error
// into an empty successful read, and do not persist anything merely by opening the Health home.
func (s *Service) healthIntakeSettings(ctx context.Context) (healthIntakeSettings, error) {
	v := defaultHealthIntakeSettings()
	st := s.store()
	if st == nil {
		return v, i18n.Err("assistant-health_intake.unavailable")
	}
	if _, err := st.Setting(ctx, healthIntakeKey, &v); err != nil {
		var syntax *json.SyntaxError
		var valueType *json.UnmarshalTypeError
		if !errors.As(err, &syntax) && !errors.As(err, &valueType) {
			return v, err
		}
		s.logf("health intake: invalid stored settings; using defaults")
		return defaultHealthIntakeSettings(), nil
	}
	if v.validate() != nil {
		s.logf("health intake: invalid stored settings; using defaults")
		return defaultHealthIntakeSettings(), nil
	}
	return v, nil
}
