package db

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type assistantEntriesCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func encodeAssistantEntriesCursor(e proto.AssistantEntry) string {
	b, _ := json.Marshal(assistantEntriesCursor{At: e.At, ID: e.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeAssistantEntriesCursor(s string) (assistantEntriesCursor, error) {
	var c assistantEntriesCursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > 1024 || json.Unmarshal(b, &c) != nil || c.At.IsZero() || c.ID == "" {
		return c, errors.New("invalid after cursor")
	}
	return c, nil
}

// ValidateAssistantEntriesParams checks filters and fills the default page size.
func ValidateAssistantEntriesParams(p *proto.AssistantEntriesParams) error {
	if p.Limit < 0 || p.Limit > 200 {
		return errors.New("limit must be between 0 and 200")
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	if _, err := decodeAssistantEntriesCursor(p.After); err != nil {
		return err
	}
	for _, v := range []struct {
		value   string
		allowed []string
	}{
		{p.By, []string{"user", "agent", "enkeep", "import"}},
		{p.Trust, []string{"user_verbatim", "user_relayed", "repo", "agent", "inferred"}},
		{p.Status, []string{"active", "superseded", "expired", "wrong", "all"}},
		{p.Type, []string{"fact", "preference", "rule", "decision"}},
	} {
		if v.value != "" && !slices.Contains(v.allowed, v.value) {
			return errors.New("invalid ledger filter")
		}
	}
	if p.Scope != nil && p.Scope.Executor != "" && !slices.Contains([]string{"system", "assistant", "any"}, p.Scope.Executor) {
		return errors.New("invalid scope executor")
	}
	return nil
}

// NormalizeAssistantEntry gives stored entries stable empty-array and UTC representations.
func NormalizeAssistantEntry(e *proto.AssistantEntry) {
	if e.ExpiresAt != nil {
		at := e.ExpiresAt.UTC()
		e.ExpiresAt = &at
	}
	if e.Category == nil {
		e.Category = []string{}
	}
	if e.RelayedVia == nil {
		e.RelayedVia = []string{}
	}
	if e.Tag == nil {
		e.Tag = []string{}
	}
	if e.Scope.Machine == nil {
		e.Scope.Machine = []string{}
	}
	if e.Scope.Project == nil {
		e.Scope.Project = []string{}
	}
	if e.Decision != nil {
		if e.Decision.Options == nil {
			e.Decision.Options = []string{}
		}
		if e.Decision.Selected == nil {
			e.Decision.Selected = []string{}
		}
		if e.Decision.Rejected == nil {
			e.Decision.Rejected = []string{}
		}
	}
}

// ValidateAssistantEntry checks immutable content without asserting quote verification.
func ValidateAssistantEntry(e proto.AssistantEntry) error {
	p := proto.AssistantEntriesParams{By: e.By, Trust: e.Trust, Status: e.Status, Type: e.Type}
	if ValidateAssistantEntriesParams(&p) != nil || e.ID == "" || len(e.ID) > 200 || len(e.Text) > 16000 || len(e.Quote) > 64000 || strings.TrimSpace(e.Text) == "" || e.Type == "" || e.By == "" || e.Trust == "" || !slices.Contains([]string{"active", "superseded", "wrong"}, e.Status) {
		return errors.New("invalid ledger entry")
	}
	if !slices.Contains([]string{"system", "assistant", "any"}, e.Scope.Executor) || e.Scope.Executor == "any" && !e.Scope.Personal {
		return errors.New("invalid entry scope")
	}
	if e.Supersedes == e.ID {
		return errors.New("an entry cannot supersede itself")
	}
	if e.By == "user" && (e.Quote == "" || e.Source.Machine == "" || e.Source.Message == "" && e.Source.Card == "" && e.Source.File == "") {
		return errors.New("user entries require a quote and source")
	}
	if (e.Trust == "user_verbatim" || e.Trust == "user_relayed") && (e.By != "user" || e.Quote == "") {
		return errors.New("user trust requires user provenance")
	}
	if e.Type == "decision" {
		if e.Decision == nil || len(e.Decision.Selected) == 0 {
			return errors.New("decisions require structured choices")
		}
		for _, v := range e.Decision.Selected {
			if !slices.Contains(e.Decision.Options, v) {
				return errors.New("selected choice is not an option")
			}
		}
		for _, v := range e.Decision.Rejected {
			if !slices.Contains(e.Decision.Options, v) || slices.Contains(e.Decision.Selected, v) {
				return errors.New("invalid rejected choice")
			}
		}
	} else if e.Decision != nil {
		return errors.New("decision fields require type decision")
	}
	return nil
}
