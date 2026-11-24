package assistant

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestHealthIntakeLocalZoneHasPortableName(t *testing.T) {
	for _, tc := range []struct {
		name, tz, target, want string
		set                    bool
	}{
		{"TZ name", "Asia/Tokyo", "", "Asia/Tokyo", true},
		{"TZ colon", ":America/New_York", "", "America/New_York", true},
		{"TZ zoneinfo path", "/usr/share/zoneinfo/Europe/Paris", "", "Europe/Paris", true},
		{"explicit empty TZ", "", "/usr/share/zoneinfo/Asia/Tokyo", "UTC", true},
		{"localtime link", "", "/usr/share/zoneinfo/Asia/Tokyo", "Asia/Tokyo", false},
		{"invalid TZ then localtime", "invalid/zone", "/var/db/timezone/zoneinfo/Europe/Paris", "Europe/Paris", true},
		{"unresolved local", "Local", "", "UTC", true},
		{"no IANA source", "", "", "UTC", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := intakeLocalZone(tc.tz, tc.set, func() (string, error) {
				if tc.target == "" {
					return "", errors.New("synthetic localtime lookup failure")
				}
				return tc.target, nil
			})
			if loc.String() != tc.want || loc.String() == "Local" {
				t.Fatalf("zone %q, want %q", loc.String(), tc.want)
			}
			if _, err := time.LoadLocation(loc.String()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHealthIntakeUnreportedZoneUsesNamedCalendar(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "full")
	var view healthIntakeView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Zone != "Asia/Tokyo" {
		t.Fatalf("unreported phone zone: %d %s", w.Code, w.Body)
	}
	w = intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", `{"id":"`+intakeTestID+`","kind":"water"}`, "answer")
	var mutation healthIntakeMutation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &mutation) != nil || mutation.Zone != "Asia/Tokyo" {
		t.Fatalf("mutation zone: %d %s", w.Code, w.Body)
	}
	setSettings(t, s, func(v *Settings) { v.Timezone = "America/New_York" })
	if got := s.healthIntakeLocation(t.Context()).String(); got != "America/New_York" {
		t.Fatalf("local fallback overrode the reported zone: %s", got)
	}
}
