package tokenuse

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// pi and its fork omp (Oh My Pi) keep a session as JSONL entries (<agent dir>/sessions/<cwd>/<time>_<id>.jsonl), each
// with an id and a time. A model request's usage is on the assistant message entry that answers it
// (message.usage, message.model); calls outside the conversation have entries of their own: pi's "usage" (cache
// warming), omp's "model_usage", and pi's compaction and branch summaries carry the usage of the call that wrote them
// (no model: the session's latest). Their usage is already in the four kinds: input without the cache, output with
// the reasoning (reasoning / reasoningTokens is part of it), cacheRead, cacheWrite (cacheWrite1h part of it); omp adds
// the provider's orchestration tokens, billed outside the conversation's buckets, counted in the kind they are of.
// A fork copies the entries it starts from as they are, ids and times too: the id and the time name a request.

const (
	Pi  = "pi"
	Omp = "omp"
)

// piUsage is a usage object as pi and omp write it.
type piUsage struct {
	Input           int64 `json:"input"`
	Output          int64 `json:"output"`
	CacheRead       int64 `json:"cacheRead"`
	CacheWrite      int64 `json:"cacheWrite"`
	CacheWrite1h    int64 `json:"cacheWrite1h"`
	Reasoning       int64 `json:"reasoning"`       // pi
	ReasoningTokens int64 `json:"reasoningTokens"` // omp
	Orchestration   *struct {
		Input     int64 `json:"input"`
		CacheRead int64 `json:"cacheRead"`
		Output    int64 `json:"output"`
	} `json:"orchestration"` // omp
}

func (u *piUsage) tokens() Tokens {
	t := Tokens{Input: max(u.Input, 0), Output: max(u.Output, 0), CacheRead: max(u.CacheRead, 0), CacheWrite: max(u.CacheWrite, 0),
		CacheWrite1h: max(u.CacheWrite1h, 0), Reasoning: max(u.Reasoning, u.ReasoningTokens, 0)}
	if o := u.Orchestration; o != nil {
		t.Input += max(o.Input, 0)
		t.CacheRead += max(o.CacheRead, 0)
		t.Output += max(o.Output, 0)
	}
	return t
}

// PiParser reads a session file of agent's (pi, or omp: the same format).
func PiParser(agent string) Parser { return &piParser{agent: agent} }

type piParser struct {
	agent string
	model string // the session's latest, for a summary's usage
}

var (
	piUsageMark       = []byte(`"usage"`)
	piModelChangeMark = []byte(`"model_change"`)
)

func (p *piParser) Line(line []byte) (Record, bool) {
	change := bytes.Contains(line, piModelChangeMark)
	if !change && !bytes.Contains(line, piUsageMark) {
		return Record{}, false
	}
	var e struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Timestamp json.RawMessage `json:"timestamp"`
		Model     string          `json:"model"`   // usage, model_usage; omp's model_change ("provider/model")
		ModelID   string          `json:"modelId"` // pi's model_change
		Usage     *piUsage        `json:"usage"`
		Message   *struct {
			Role  string   `json:"role"`
			Model string   `json:"model"`
			Usage *piUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return Record{}, false
	}
	var u *piUsage
	model := ""
	switch e.Type {
	case "model_change":
		if m := firstNonEmpty(e.ModelID, e.Model); m != "" {
			if i := strings.LastIndexByte(m, '/'); i >= 0 && e.ModelID == "" {
				m = m[i+1:] // omp's "provider/model"
			}
			p.model = m
		}
		return Record{}, false
	case "message":
		if e.Message == nil || e.Message.Role != "assistant" {
			return Record{}, false
		}
		u, model = e.Message.Usage, e.Message.Model
		if model != "" {
			p.model = model
		}
	case "usage", "model_usage":
		u, model = e.Usage, e.Model
	case "compaction", "branch_summary":
		u, model = e.Usage, p.model
	default:
		return Record{}, false
	}
	if u == nil {
		return Record{}, false
	}
	t := u.tokens()
	if t.Zero() { // a failed request's message, or a summary nobody paid for
		return Record{}, false
	}
	ts, raw := piTime(e.Timestamp)
	r := Record{Time: ts, Agent: p.agent, Model: model, Tokens: t}
	if e.ID != "" {
		r.Key = "pi:" + e.ID + ":" + raw
	}
	return r, true
}

// piTime is an entry's time, written as an ISO string or as Unix milliseconds, and the value as written.
func piTime(raw json.RawMessage) (time.Time, string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t, s
	}
	if ms, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64); err == nil {
		return time.UnixMilli(ms), string(bytes.TrimSpace(raw))
	}
	return time.Time{}, ""
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
