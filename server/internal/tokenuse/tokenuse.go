// Package tokenuse is the one model of an agent's token usage and the parsers of the records agents keep it in
// (docs/server/machines.md "Token usage"). The node's usage meter and backfill (node/usage.go), the usage on a chat's
// replies (transcript/item_usage.go) and the subagent summaries (node/agent_usage.go) all read usage through it, so a
// request counts the same everywhere.
//
// A record is one model request: its time, agent, model, a key that tells a copy of it in another file (a fork) from
// a new request, and its tokens in four kinds that do not overlap: input not read from the cache, output (reasoning
// included), cache reads and cache writes. Their sum is every token the request used, the count the calendar and
// today's tokens show. The 1-hour cache writes and the reasoning are parts of cache writes and output ("of which"),
// never added to the sum again.
package tokenuse

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The agents, as proto.Session.Agent names them (Pi, Omp and Opencode too).
const (
	Claude = "claude"
	Codex  = "codex"
	Traex  = "traex" // a Codex-compatible CLI: Codex's rollouts, in a home of its own
)

// Tokens are a request's tokens. Input, Output, CacheRead and CacheWrite do not overlap; CacheWrite1h is part of
// CacheWrite and Reasoning part of Output.
type Tokens struct {
	Input      int64 // not read from the cache
	Output     int64 // reasoning included
	CacheRead  int64
	CacheWrite int64
	// CacheWrite1h: of CacheWrite, what was written for an hour (priced higher than the 5-minute writes).
	CacheWrite1h int64
	// Reasoning: of Output, the reasoning tokens, when the agent says (Codex); 0 when it does not.
	Reasoning int64
}

// Total is every token: the four kinds.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// Zero says the request used no tokens: nothing to count.
func (t Tokens) Zero() bool {
	return t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheWrite == 0 && t.CacheWrite1h == 0
}

// Plus is t and u added.
func (t Tokens) Plus(u Tokens) Tokens {
	return Tokens{Input: t.Input + u.Input, Output: t.Output + u.Output, CacheRead: t.CacheRead + u.CacheRead,
		CacheWrite: t.CacheWrite + u.CacheWrite, CacheWrite1h: t.CacheWrite1h + u.CacheWrite1h, Reasoning: t.Reasoning + u.Reasoning}
}

// Record is one request's usage, as an agent's record says it.
type Record struct {
	Time  time.Time
	Agent string
	// Session is the session the request belongs to when the record says it (a database's row); "" for a record in
	// a session's own file, whose adapter knows it.
	Session string
	Model   string // "" when the record does not say
	// Key tells the request apart: the same key in another file is a copy of it (a fork copies the history it starts
	// from, timestamps and all), counted once. "<agent>:<the request's id>"; "" when there is no way to tell, counted
	// as it is.
	Key string
	// More: a later record of the same request in the same file, adding to what its earlier ones gave (Claude Code
	// writes a request's usage on each content block of its reply): not another request.
	More bool
	Fast bool // Claude Code's fast mode, priced higher than the standard rate
	Tokens
}

// Item is the record's usage as a chat item carries it (proto.Item.Usage).
func (r Record) Item() proto.TokenUsage {
	return proto.TokenUsage{Model: r.Model, Fast: r.Fast, Input: r.Input, Output: r.Output, CacheRead: r.CacheRead,
		CacheWrite: r.CacheWrite, CacheWrite1h: r.CacheWrite1h}
}

// Parser reads one file of an agent's records line by line, keeping what the format needs between lines (the request
// under way, the model, a running total). A file needs its own.
type Parser interface {
	// Line is the record line holds, ok false for a line that holds none.
	Line(line []byte) (r Record, ok bool)
}
