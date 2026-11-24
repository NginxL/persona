package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

const waterImportKey = "health.intake.water_import"

type healthWaterImportStore interface {
	HealthIntakeImport(context.Context, []db.HealthIntakeRow) (int, error)
	HealthIntakeFinishWaterImport(ctx context.Context, settings, reminder, receipt any) error
}

type waterImportReceipt struct {
	At      time.Time `json:"at"`
	Records int       `json:"records"`
	Version string    `json:"version"`
}

type waterImportResult struct {
	Imported          int                  `json:"imported"`
	ExtensionDisabled bool                 `json:"extension_disabled"`
	Settings          healthIntakeSettings `json:"settings"`
}

// Decode the whole file, including timestamps in auxiliary fields. Unlike the
// extension's loader this never prunes, renames, repairs or writes its input.
type legacyWaterState struct {
	Zone     string `json:"zone"`
	Sequence uint64 `json:"sequence"`
	Records  []struct {
		ID string    `json:"id"`
		At time.Time `json:"at"`
		ML int       `json:"ml"`
	} `json:"records"`
	LastReminder time.Time            `json:"last_reminder"`
	Snooze       time.Time            `json:"snooze"`
	Seen         map[string]time.Time `json:"seen"`
	Reason       string               `json:"reason"`
	Pending      *struct {
		At       time.Time `json:"at"`
		RetryAt  time.Time `json:"retry_at"`
		Attempts int       `json:"attempts"`
		Notice   struct {
			Title   string              `json:"title"`
			Body    string              `json:"body"`
			UI      string              `json:"ui"`
			ID      string              `json:"id"`
			Level   string              `json:"level"`
			State   string              `json:"state"`
			Actions []wire.NotifyAction `json:"actions"`
		} `json:"notice"`
	} `json:"pending"`
}

func readLegacyWater(v *assistantwire.WaterExtension) ([]db.HealthIntakeRow, waterReminderState, error) {
	var state legacyWaterState
	if raw := bytes.TrimSpace(v.State); len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &state) != nil {
		return nil, waterReminderState{}, errors.New("water.json must be a valid water state object, with valid field types and timestamps")
	}
	if p := state.Pending; p != nil && (p.At.IsZero() || p.RetryAt.IsZero() || p.Attempts < 1 || p.Attempts > 3 || p.Notice.UI != "water" || p.Notice.ID != "water-reminder") {
		return nil, waterReminderState{}, errors.New("water.json contains an invalid pending reminder")
	}
	reminder := waterReminderState{Last: state.LastReminder, Snooze: state.Snooze}
	rows := make([]db.HealthIntakeRow, 0, len(state.Records))
	amounts := map[string]int{}
	for i, r := range state.Records {
		if r.ID == "" || r.At.IsZero() || r.ML < 1 || r.ML > 3000 {
			return nil, waterReminderState{}, fmt.Errorf("water.json record %d requires an id, a timestamp and 1–3000 ml", i+1)
		}
		id := healthIntakeUUID("water-ext:" + v.StorageKey + ":" + r.ID + ":" + r.At.UTC().Format(time.RFC3339Nano))
		if old, ok := amounts[id]; ok && old != r.ML {
			return nil, waterReminderState{}, fmt.Errorf("water.json record %d conflicts with another record at the same time", i+1)
		}
		amounts[id] = r.ML
		rows = append(rows, db.HealthIntakeRow{ID: id, Kind: "water", Amount: float64(r.ML), At: r.At.UTC()})
		if r.At.After(reminder.Last) {
			reminder.Last = r.At
		}
	}
	return rows, reminder, nil
}

func legacyWaterSettings(v *assistantwire.WaterExtension) (healthIntakeSettings, error) {
	c := defaultHealthIntakeSettings()
	c.Water.Remind = true // The extension's untouched reminder switch defaults to on.
	if len(v.Config) != 0 {
		raw := bytes.TrimSpace(v.Config)
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &c.Water) != nil {
			return c, errors.New("water extension configuration is invalid")
		}
	}
	c.Water.Remind = c.Water.Remind && v.Enabled
	if err := c.validate(); err != nil {
		return c, errors.New("water extension configuration is outside the supported limits")
	}
	return c, nil
}

func (s *Service) pendingWaterExtension(ctx context.Context) (string, error) {
	e, ok := s.hub.(waterExtensions)
	if !ok {
		return "", nil
	}
	v, err := e.WaterExtension(ctx, false)
	if err != nil || v == nil {
		return "", err
	}
	if v.Enabled {
		return "pending", nil
	}
	var receipt waterImportReceipt
	found, err := s.store().Setting(ctx, waterImportKey, &receipt)
	if err != nil {
		return "", err
	}
	if !found {
		return "pending", nil
	}
	return "", nil
}

func (s *Service) importHealthWater(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, false) {
		return
	}
	e, ok := s.hub.(waterExtensions)
	st, hasStore := s.store().(healthWaterImportStore)
	if !ok || !hasStore {
		intakeUnavailable(w)
		return
	}
	// The scheduler and page actions share this lock. No built-in reminder can be
	// planned while the import is between its record, switch and settings steps.
	s.intakeMu.Lock()
	defer s.intakeMu.Unlock()
	ctx := r.Context()
	fail := func(status int, err error) { apierr.WriteCode(w, status, "water_import_failed", err) }
	// 1. Read and validate everything before the first write.
	before, err := e.WaterExtension(ctx, true)
	if err != nil {
		fail(http.StatusServiceUnavailable, err)
		return
	}
	if before == nil {
		fail(http.StatusNotFound, errors.New("water extension is not installed"))
		return
	}
	rows, _, err := readLegacyWater(before)
	if err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	settings, err := legacyWaterSettings(before)
	if err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	var receipt waterImportReceipt
	migrated, err := s.store().Setting(ctx, waterImportKey, &receipt)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	// 2. The first complete file commits in one transaction, without touching reminders.
	count, err := st.HealthIntakeImport(ctx, rows)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	if count > 0 {
		s.event(ctx, map[string]any{"type": "health_intake"})
	}
	// 3. Preserve the existing switch's has_dependents and setByUser behavior.
	if !e.DisableWaterExtension(w, r, before.StorageKey) {
		return
	}
	// 4. Join shutdown via the switch, then pick up writes made just before it.
	after, err := e.WaterExtension(ctx, true)
	if err != nil {
		fail(http.StatusServiceUnavailable, err)
		return
	}
	if after == nil || after.StorageKey != before.StorageKey || after.Version != before.Version || !bytes.Equal(after.Config, before.Config) || after.Enabled {
		fail(http.StatusConflict, errors.New("water extension changed during import; retry from Health"))
		return
	}
	rows, reminder, err := readLegacyWater(after)
	if err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	added, err := st.HealthIntakeImport(ctx, rows)
	if err != nil {
		intakeUnavailable(w)
		return
	}
	count += added
	if added > 0 {
		s.event(ctx, map[string]any{"type": "health_intake"})
	}
	// A completed import retried while still off must retain the user's current
	// Health settings and reminder timing, including a reminder enabled by that import.
	if migrated && !before.Enabled {
		settings, err = s.healthIntakeSettings(ctx)
		if err == nil {
			reminder, err = s.waterReminderState(ctx)
		}
		if err != nil {
			intakeUnavailable(w)
			return
		}
	} else {
		current, err := s.waterReminderState(ctx)
		if err != nil {
			intakeUnavailable(w)
			return
		}
		if current.Last.After(reminder.Last) {
			reminder.Last = current.Last
		}
	}
	// 5–6. Publish settings and timing only after stopping, then the receipt, atomically.
	receipt = waterImportReceipt{At: time.Now().UTC(), Records: count, Version: before.Version}
	if err := st.HealthIntakeFinishWaterImport(ctx, settings, reminder, receipt); err != nil {
		intakeUnavailable(w)
		return
	}
	s.event(ctx, map[string]any{"type": "health_intake"})
	intakeJSON(w, waterImportResult{Imported: count, ExtensionDisabled: true, Settings: settings})
}
