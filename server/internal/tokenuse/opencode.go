package tokenuse

import "time"

// opencode keeps its sessions in a SQLite database (opencode.db): a session_message row of type "assistant" is one
// step, one model request, with its usage in data.tokens and its model in data.model.id; a "compaction" row that
// completed or failed carries the usage of the request that wrote it. A row is written as the step starts and
// rewritten until it ends (data.time.completed, a compaction's status): it counts once finished. Its tokens are input
// without the cache, output without the reasoning (reasoning apart), cache read and cache write.

const Opencode = "opencode"

// OpencodeRow is what the node reads of a session_message row (node/usage_opencode.go's query).
type OpencodeRow struct {
	ID         string `json:"id"`
	Session    string `json:"session_id"`
	Type       string `json:"type"`
	Created    int64  `json:"time_created"` // Unix milliseconds
	Completed  *int64 `json:"completed"`    // data.time.completed (assistant), Unix milliseconds
	Status     string `json:"status"`       // data.status (compaction)
	Model      string `json:"model"`        // data.model.id
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	Reasoning  int64  `json:"reasoning"`
	CacheRead  int64  `json:"cache_read"`
	CacheWrite int64  `json:"cache_write"`
}

// Finished says the row will not change any more: a step that completed, a compaction that completed or failed.
func (r OpencodeRow) Finished() bool {
	switch r.Type {
	case "assistant":
		return r.Completed != nil
	case "compaction":
		return r.Status == "completed" || r.Status == "failed"
	}
	return false
}

// Record is the row's request, ok false for a row that counts none (not finished, or no tokens).
func (r OpencodeRow) Record() (Record, bool) {
	if !r.Finished() {
		return Record{}, false
	}
	t := Tokens{Input: max(r.Input, 0), Output: max(r.Output, 0) + max(r.Reasoning, 0), Reasoning: max(r.Reasoning, 0),
		CacheRead: max(r.CacheRead, 0), CacheWrite: max(r.CacheWrite, 0)}
	if t.Zero() {
		return Record{}, false
	}
	// The time the step began, as the database's index has it (the backfill reads by it); a step takes seconds.
	return Record{Time: time.UnixMilli(r.Created), Agent: Opencode, Session: r.Session, Model: r.Model, Key: "opencode:" + r.ID,
		Tokens: t}, true
}
