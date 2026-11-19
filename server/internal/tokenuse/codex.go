package tokenuse

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
)

// Codex and the CLIs that write its rollouts (traex): each model request ends with an event_msg token_count whose
// info has the request's usage (last_token_usage) and the session's running total (total_token_usage); the model is
// the latest turn_context's. Codex repeats a token_count with a rate-limit update (same totals): only a grown total
// is a new request.

// CodexCounts are a token_count's usage as Codex writes it. Its input includes what was read from and written to the
// cache (Codex takes both out for its own count of fresh input); its output includes the reasoning.
type CodexCounts struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

// Tokens are the counts in the four kinds: input without the cached and cache-written parts, cached input as cache
// reads, cache writes, output (reasoning included).
func (c CodexCounts) Tokens() Tokens {
	return Tokens{Input: max(c.Input-c.Cached-c.CacheWrite, 0), CacheRead: max(c.Cached, 0), CacheWrite: max(c.CacheWrite, 0),
		Output: max(c.Output, 0), Reasoning: max(c.Reasoning, 0)}
}

// CodexInfo is a token_count's info.
type CodexInfo struct {
	Total              CodexCounts `json:"total_token_usage"`
	Last               CodexCounts `json:"last_token_usage"`
	ModelContextWindow int         `json:"model_context_window"`
}

// CodexTotal keeps a rollout's running total: Grew says whether a token_count's total is a new request's.
type CodexTotal struct{ last int64 }

// Grew takes a token_count's running total: true when it grew, a new request (and it is the latest now).
func (c *CodexTotal) Grew(total int64) bool {
	if total <= c.last {
		return false
	}
	c.last = total
	return true
}

// CodexTotalFrom is a rollout's running total known to be last so far (read back from the lines before a window).
func CodexTotalFrom(last int64) CodexTotal { return CodexTotal{last: last} }

// Last is the latest running total.
func (c CodexTotal) Last() int64 { return c.last }

var (
	tokenCountMark  = []byte(`"token_count"`)
	turnContextMark = []byte(`"turn_context"`)
)

// CodexParser reads a rollout of agent's (Codex, or a CLI that writes its rollouts): a record for each request.
func CodexParser(agent string) Parser { return &codexParser{agent: agent} }

type codexParser struct {
	agent string
	model string // the latest turn_context's
	total CodexTotal
}

func (p *codexParser) Line(line []byte) (Record, bool) {
	if !bytes.Contains(line, tokenCountMark) && !bytes.Contains(line, turnContextMark) {
		return Record{}, false
	}
	var l struct {
		Timestamp time.Time `json:"timestamp"`
		Type      string    `json:"type"`
		Payload   struct {
			Type  string     `json:"type"`
			Model string     `json:"model"`
			Info  *CodexInfo `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil {
		return Record{}, false
	}
	if l.Type == "turn_context" {
		if l.Payload.Model != "" {
			p.model = l.Payload.Model
		}
		return Record{}, false
	}
	if l.Payload.Type != "token_count" || l.Payload.Info == nil || !p.total.Grew(l.Payload.Info.Total.Total) {
		return Record{}, false
	}
	model := p.model
	if model == "" {
		model = "codex"
	}
	// The time and the running total name the request: a fork's copy of it has both.
	key := "codex:" + l.Timestamp.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(l.Payload.Info.Total.Total, 10)
	return Record{Time: l.Timestamp, Agent: p.agent, Model: model, Key: key, Tokens: l.Payload.Info.Last.Tokens()}, true
}
