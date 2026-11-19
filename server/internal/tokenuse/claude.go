package tokenuse

import (
	"bytes"
	"encoding/json"
	"math"
	"time"
)

// Claude Code: each reply line (type "assistant") carries its request's usage in message.usage, and a reply is a
// line per content block, each repeating the usage (up to 2.1.306 with the output generated so far, more on each later
// line). A request counts once, at the most its lines said; message.id names it.

// ClaudeUsage is a reply line's message.usage as Claude Code writes it.
type ClaudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreation            *struct {
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Speed string `json:"speed"` // "fast": Claude Code's fast mode
}

// ClaudeTokens is a reply line's request usage: ok is false for one that counts none: no usage, all zeros (a
// gateway's on a turn's other replies), or a reply Claude Code wrote itself for a failed request (model "<synthetic>",
// isApiErrorMessage).
func ClaudeTokens(model string, u *ClaudeUsage, apiError bool) (t Tokens, fast, ok bool) {
	if u == nil || apiError || model == "<synthetic>" {
		return Tokens{}, false, false
	}
	t = Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens}
	if u.CacheCreation != nil {
		t.CacheWrite1h = u.CacheCreation.Ephemeral1h
	}
	return t, u.Speed == "fast", t.Input > 0 || t.Output > 0 || t.CacheRead > 0 || t.CacheWrite > 0
}

// ClaudeReply reads a Claude Code transcript line as a reply's: its time, request id, model and usage. ok is false for
// any other line and for a reply that counts none (ClaudeTokens). Sidechain lines read like any other.
func ClaudeReply(line []byte) (ts time.Time, id, model string, t Tokens, fast, ok bool) {
	var e struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		APIError  bool      `json:"isApiErrorMessage"`
		Message   *struct {
			ID    string       `json:"id"`
			Model string       `json:"model"`
			Usage *ClaudeUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message == nil {
		return time.Time{}, "", "", Tokens{}, false, false
	}
	t, fast, ok = ClaudeTokens(e.Message.Model, e.Message.Usage, e.APIError)
	return e.Timestamp, e.Message.ID, e.Message.Model, t, fast, ok
}

// Request follows the request a run of Claude Code reply lines belongs to, so that each request counts once: a later
// line adds what grew, and the request's lines add up to its last (largest) usage.
type Request struct {
	id  string
	got Tokens // the most of each count its lines gave so far
}

// Add returns what a line of request id with usage u adds to what the request's earlier lines gave; ok is false when
// it adds nothing (a repeat). A line without an id is a request of its own.
func (r *Request) Add(id string, u Tokens) (Tokens, bool) {
	if id == "" || id != r.id {
		r.id, r.got = id, Tokens{}
	}
	g := &r.got
	add := Tokens{Input: max(u.Input-g.Input, 0), Output: max(u.Output-g.Output, 0), CacheRead: max(u.CacheRead-g.CacheRead, 0),
		CacheWrite: max(u.CacheWrite-g.CacheWrite, 0), CacheWrite1h: max(u.CacheWrite1h-g.CacheWrite1h, 0)}
	g.raise(u)
	return add, !add.Zero()
}

// ID is the request the lines so far belong to ("" for none, or one without an id).
func (r Request) ID() string { return r.id }

// Got is the most of each count the request's lines gave so far.
func (r Request) Got() Tokens { return r.got }

// Merge raises what r's lines gave to the most of o's: o is what earlier lines of the same request gave.
func (r *Request) Merge(o Request) { r.got.raise(o.got) }

// Saturated is request id after a line no later line of it adds to: the request's usage is already counted.
func Saturated(id string) Request {
	return Request{id: id, got: Tokens{Input: math.MaxInt64, Output: math.MaxInt64, CacheRead: math.MaxInt64,
		CacheWrite: math.MaxInt64, CacheWrite1h: math.MaxInt64}}
}

// raise raises each count of g to u's where u's is larger.
func (g *Tokens) raise(u Tokens) {
	g.Input, g.Output, g.CacheRead = max(g.Input, u.Input), max(g.Output, u.Output), max(g.CacheRead, u.CacheRead)
	g.CacheWrite, g.CacheWrite1h = max(g.CacheWrite, u.CacheWrite), max(g.CacheWrite1h, u.CacheWrite1h)
}

var (
	usageMark     = []byte(`"usage"`)
	assistantMark = []byte(`"assistant"`)
)

// ClaudeParser reads a Claude Code transcript (a session's, a subagent's, a workflow agent's): a record for each reply
// line that adds to its request, the first of a request's with More false.
func ClaudeParser() Parser { return &claudeParser{} }

type claudeParser struct {
	req     Request
	counted bool // a record of req's was given: its later ones are More
}

func (p *claudeParser) Line(line []byte) (Record, bool) {
	if !bytes.Contains(line, usageMark) || !bytes.Contains(line, assistantMark) {
		return Record{}, false
	}
	ts, id, model, t, fast, ok := ClaudeReply(line)
	if !ok {
		return Record{}, false
	}
	if id == "" || id != p.req.ID() {
		p.counted = false
	}
	add, ok := p.req.Add(id, t)
	if !ok {
		return Record{}, false
	}
	r := Record{Time: ts, Agent: Claude, Model: model, More: p.counted, Fast: fast, Tokens: add}
	if id != "" {
		r.Key = "claude:" + id
	}
	p.counted = true
	return r, true
}
