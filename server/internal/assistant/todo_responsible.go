package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
)

// TodoResponsible is an external responsible party on a to-do (#12712, docs/assistant/todos.md): either a long-running
// external session ("session") or a person ("person"). Name is a stable label; MachineID and SessionID are optional
// hints. The hub records and displays it without starting or messaging the external session.
type TodoResponsible = db.TodoResponsible

const (
	responsibleNameMax = 200
	responsibleHintMax = 256
)

func cloneResponsible(r *TodoResponsible) *TodoResponsible {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// cleanResponsible validates and normalizes an external responsible party: kind must be "session" or "person", name
// must be non-empty after trimming, and optional machine_id / session_id hints are trimmed and length-bounded.
func cleanResponsible(r TodoResponsible) (TodoResponsible, error) {
	r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
	if r.Kind != db.ResponsibleSession && r.Kind != db.ResponsiblePerson {
		return TodoResponsible{}, fmt.Errorf("responsible.kind must be %q or %q", db.ResponsibleSession, db.ResponsiblePerson)
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return TodoResponsible{}, errors.New("responsible.name cannot be empty")
	}
	if utf8.RuneCountInString(r.Name) > responsibleNameMax {
		return TodoResponsible{}, fmt.Errorf("responsible.name is longer than %d characters", responsibleNameMax)
	}
	r.MachineID = strings.TrimSpace(r.MachineID)
	if utf8.RuneCountInString(r.MachineID) > responsibleHintMax {
		return TodoResponsible{}, fmt.Errorf("responsible.machine_id is longer than %d characters", responsibleHintMax)
	}
	r.SessionID = strings.TrimSpace(r.SessionID)
	if utf8.RuneCountInString(r.SessionID) > responsibleHintMax {
		return TodoResponsible{}, fmt.Errorf("responsible.session_id is longer than %d characters", responsibleHintMax)
	}
	return r, nil
}

// parseResponsiblePatch parses an optional responsible field from a POST or PATCH body:
// omitted -> (false, nil, nil); null -> (true, nil, nil) to clear; object -> validated (true, &r, nil).
func parseResponsiblePatch(raw json.RawMessage) (set bool, val *TodoResponsible, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false, nil, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return true, nil, nil
	}
	if trimmed[0] != '{' {
		return false, nil, errors.New("responsible must be an object or null")
	}
	var in TodoResponsible
	if err := json.Unmarshal(trimmed, &in); err != nil {
		return false, nil, fmt.Errorf("responsible: %w", err)
	}
	clean, err := cleanResponsible(in)
	if err != nil {
		return false, nil, err
	}
	return true, &clean, nil
}

// formatResponsibleHints renders the optional machine_id / session_id hints for plain-text tool output without stray
// slashes when only one hint is set.
func formatResponsibleHints(r *TodoResponsible) string {
	if r == nil {
		return ""
	}
	switch {
	case r.MachineID != "" && r.SessionID != "":
		return r.MachineID + "/" + r.SessionID
	case r.MachineID != "":
		return "machine " + r.MachineID
	case r.SessionID != "":
		return "session " + r.SessionID
	default:
		return ""
	}
}
