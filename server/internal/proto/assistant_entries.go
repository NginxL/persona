package proto

import "time"

// AssistantEntrySource reserves message, card and repository pointers for later quote verification.
type AssistantEntrySource struct {
	Machine string `json:"machine"`
	Session string `json:"session"`
	Message string `json:"msg_uuid,omitempty"`
	Card    string `json:"card_id,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
}

type AssistantEntryScope struct {
	Executor string   `json:"executor"`
	Machine  []string `json:"machine"`
	Project  []string `json:"project"`
	Personal bool     `json:"personal"`
}

// AssistantEntryDecision keeps unknown reversibility distinct from an explicit no.
type AssistantEntryDecision struct {
	Options    []string `json:"options"`
	Selected   []string `json:"selected"`
	Rejected   []string `json:"rejected"`
	Reason     string   `json:"reason"`
	Reversible *bool    `json:"reversible"`
}

type AssistantEntry struct {
	OriginSpace string                  `json:"origin_space"`
	Category    []string                `json:"category"`
	ID          string                  `json:"id"`
	Type        string                  `json:"type"`
	Text        string                  `json:"text"`
	Quote       string                  `json:"quote"`
	Source      AssistantEntrySource    `json:"source"`
	RelayedVia  []string                `json:"relayed_via"`
	Scope       AssistantEntryScope     `json:"scope"`
	At          time.Time               `json:"at"`
	ExpiresAt   *time.Time              `json:"expires_at"`
	Supersedes  string                  `json:"supersedes"`
	Trust       string                  `json:"trust"`
	Status      string                  `json:"status"`
	By          string                  `json:"by"`
	Tag         []string                `json:"tag"`
	Decision    *AssistantEntryDecision `json:"decision,omitempty"`
	// ExtID is reserved for future per-publisher idempotency; H1a exposes no write RPC.
	ExtID string `json:"ext_id,omitempty"`
}

// AssistantEntryScopeFilter matches all specified dimensions; empty machine/project constraints on
// an entry apply everywhere. Personal is a filter, never a permission grant.
type AssistantEntryScopeFilter struct {
	Executor string   `json:"executor,omitempty"`
	Machine  []string `json:"machine,omitempty"`
	Project  []string `json:"project,omitempty"`
	Personal *bool    `json:"personal,omitempty"`
}

type AssistantEntriesParams struct {
	After  string                     `json:"after,omitempty"`
	Limit  int                        `json:"limit,omitempty"`
	By     string                     `json:"by,omitempty"`
	Trust  string                     `json:"trust,omitempty"`
	Status string                     `json:"status,omitempty"`
	Scope  *AssistantEntryScopeFilter `json:"scope,omitempty"`
	Tag    string                     `json:"tag,omitempty"`
	Type   string                     `json:"type,omitempty"`
}

type AssistantEntriesResult struct {
	Entries []AssistantEntry `json:"entries"`
	After   string           `json:"after"`
	HasMore bool             `json:"has_more"`
}

// AssistantEntriesChanged is the public invalidation payload, without internal routing metadata.
type AssistantEntriesChanged struct {
	ID       string `json:"id"`
	RecordID string `json:"record_id"`
	Status   string `json:"status"`
}
