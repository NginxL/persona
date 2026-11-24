package orcart

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// HistoryCursor is Orca's journal cursor. Its epoch prevents a cursor from an older journal incarnation being used.
type HistoryCursor struct {
	Epoch    string `json:"epoch"`
	Sequence int64  `json:"sequence"`
}

// JournalBlock is the safe, known part of a journal block Persona can project into a personal-Agent message.
type JournalBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Name    string          `json:"name,omitempty"`
	State   string          `json:"state,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
}

// JournalBody is a journal item's body. Message bodies carry role and blocks; other kinds remain identifiable.
type JournalBody struct {
	Kind   string         `json:"kind"`
	Role   string         `json:"role,omitempty"`
	Blocks []JournalBlock `json:"blocks,omitempty"`
}

// JournalItem is one stable Orca history row.
type JournalItem struct {
	ItemID        string      `json:"itemId"`
	Revision      int64       `json:"revision"`
	Sequence      int64       `json:"sequence"`
	SequenceIndex int64       `json:"sequenceIndex,omitempty"`
	ObservedAt    int64       `json:"observedAt"`
	Body          JournalBody `json:"body"`
}

// HistoryPage is agentSession.history's journal page, with the older-page cursor encoded for Persona's HTTP API.
type HistoryPage struct {
	SessionID  string        `json:"sessionId"`
	Epoch      string        `json:"epoch"`
	Fence      int64         `json:"fence"`
	Working    bool          `json:"working"`
	Items      []JournalItem `json:"items"`
	HasOlder   bool          `json:"hasOlder"`
	HasNewer   bool          `json:"hasNewer"`
	NextCursor string        `json:"-"`
}

func encodeHistoryCursor(c HistoryCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeHistoryCursor(s string) (HistoryCursor, error) {
	var c HistoryCursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Epoch == "" || c.Sequence < 0 {
		return HistoryCursor{}, fmt.Errorf("orca history: invalid cursor")
	}
	return c, nil
}

// History reads agentSession.history. An empty cursor asks for the tail; a returned cursor asks for the page before it.
func (c *Client) History(ctx context.Context, sessionID, cursor string, limit int) (*HistoryPage, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("orca history: session id is required")
	}
	if limit == 0 {
		limit = 40
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("orca history: limit must be between 1 and 200")
	}
	params := map[string]any{"sessionId": sessionID, "direction": "tail", "limit": limit}
	if cursor != "" {
		cur, err := decodeHistoryCursor(cursor)
		if err != nil {
			return nil, err
		}
		params["direction"], params["cursor"] = "before", cur
	}
	var res struct {
		OK   bool `json:"ok"`
		Page struct {
			SessionID string        `json:"sessionId"`
			Epoch     string        `json:"epoch"`
			Fence     int64         `json:"fence"`
			Working   bool          `json:"working"`
			Items     []JournalItem `json:"items"`
			HasOlder  bool          `json:"hasOlder"`
			HasNewer  bool          `json:"hasNewer"`
			Window    struct {
				NextCursor *HistoryCursor `json:"nextCursor"`
			} `json:"window"`
		} `json:"page"`
	}
	if err := c.Call(ctx, HistoryTimeout, "agentSession.history", params, &res); err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("orca history: unavailable")
	}
	p := &HistoryPage{SessionID: res.Page.SessionID, Epoch: res.Page.Epoch, Fence: res.Page.Fence, Working: res.Page.Working,
		Items: res.Page.Items, HasOlder: res.Page.HasOlder, HasNewer: res.Page.HasNewer}
	if p.Items == nil {
		p.Items = []JournalItem{}
	}
	if p.HasOlder && res.Page.Window.NextCursor != nil {
		p.NextCursor = encodeHistoryCursor(*res.Page.Window.NextCursor)
	}
	return p, nil
}
