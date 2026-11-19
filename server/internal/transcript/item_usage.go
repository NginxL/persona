package transcript

import (
	"bytes"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// The tokens of the model requests a chat's items stand for (proto.Item.Usage, #5426), so a client can sum a turn's
// usage, counted by the same rules as the usage meter (tokenuse). A Claude Code request's usage rides on the first item its reply lines made; a Codex turn's total on its
// turn_duration item (or its interrupted notice); a finished subagent's or workflow run's on its report, set by the node.

// replyUsage puts the usage of the request a reply line belongs to on the first item the line made (first; the caller
// passes it only when there is one), as far as the request's earlier lines did not already. A line that made no item
// (a thinking block with nothing shown) leaves it to the request's next line. A subagent's lines in the session's
// transcript (sidechain) carry none: the node reports a subagent's usage on its result. In a subagent's own transcript
// (Meta.Sidechain) they do, but not a fork's copy of its launcher's reply that launched it, before the fork's prompt
// (its first user line): that request is the launcher's.
func (m *Meta) replyUsage(e *entry, first *proto.Item) {
	if e.IsSidechain && !m.Sidechain || m.Sidechain && !m.sawUser {
		return
	}
	t, fast, ok := tokenuse.ClaudeTokens(e.Message.Model, e.Message.Usage, e.IsAPIErrorMessage)
	if !ok {
		return
	}
	if add, ok := m.usage.Add(e.Message.ID, t); ok {
		first.Usage = []proto.TokenUsage{tokenuse.Record{Model: e.Message.Model, Fast: fast, Tokens: add}.Item()}
	}
}

// codexRequest counts a rollout's token_count in the turn's usage the way the usage meter does (tokenuse.CodexParser):
// only a grown running total is a new request (rate-limit updates repeat the last one), its counts in the four kinds.
func (m *Meta) codexRequest(total int64, last tokenuse.CodexCounts) {
	if !m.codexTotal.Grew(total) {
		return
	}
	t := last.Tokens()
	u := &m.codexUsage
	u.Input += t.Input
	u.CacheRead += t.CacheRead
	u.CacheWrite += t.CacheWrite
	u.Output += t.Output
}

// codexTurnUsage takes the usage of the turn that just ended (Item.Usage of the item that ends it): nil when it used
// none.
func (m *Meta) codexTurnUsage() []proto.TokenUsage {
	u := m.codexUsage
	m.codexUsage = proto.TokenUsage{}
	if u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0 {
		return nil
	}
	u.Model = m.codexTurnModel
	return []proto.TokenUsage{u}
}

// AddUsage adds u to sum: into the entry of the same model and speed when u has no ID, as its own entry when it has
// one (a subagent's report, counted once by its ID).
func AddUsage(sum []proto.TokenUsage, u proto.TokenUsage) []proto.TokenUsage {
	if u.ID == "" {
		for i := range sum {
			if s := &sum[i]; s.ID == "" && s.Model == u.Model && s.Fast == u.Fast {
				s.Input += u.Input
				s.Output += u.Output
				s.CacheRead += u.CacheRead
				s.CacheWrite += u.CacheWrite
				s.CacheWrite1h += u.CacheWrite1h
				return sum
			}
		}
	}
	return append(sum, u)
}

// foldUsage is what a folded run's items used (proto.Fold.Usage), nil when they carried none.
func foldUsage(run []proto.Item) []proto.TokenUsage {
	var sum []proto.TokenUsage
	for _, it := range run {
		for _, u := range it.Usage {
			sum = AddUsage(sum, u)
		}
	}
	return sum
}

// seedUsage looks back at most seedUsageLines lines and seedUsageBytes of them for the request a window's first lines
// go on from. A line over seedUsageLine bytes is read only as far as seedUsageHead (a request blob repeats the whole
// conversation; a reply with a huge tool input says its request's id near its start). A rollout's latest token_count
// further back than that is found by a byte search over at most seedCodexSearch bytes.
const (
	seedUsageLines  = 64
	seedUsageBytes  = 2 << 20
	seedUsageLine   = 256 << 10
	seedUsageHead   = 64 << 10
	seedCodexSearch = 64 << 20
)

var (
	usageMark      = []byte(`"usage"`)
	tokenCountMark = []byte(`"token_count"`)
	messageMark    = []byte(`"message":{`)
	sidechainMark  = []byte(`"isSidechain":true`)
	apiRequestHead = []byte(`{"type":"api-request`)
)

// seedLines reads the lines before a window for seedUsage (window.go seeded).
type seedLines struct {
	line func(n int) []byte            // line n whole
	head func(n int, max int64) []byte // its first max bytes
	size func(n int) int64
	// tokenCount is the latest line before line before that mentions a token_count, found by a byte search back at
	// most seedCodexSearch bytes; 0 when there is none.
	tokenCount func(before int) int
}

// seedUsage is the usage state a full read has by line b (window.go seeded), from the lines just before it: a window
// may start among a Claude reply's lines, whose request's usage an earlier one already put on an item; a rollout's
// token_count only counts when its running total grew. A request's reply lines are next to each other (the results of
// its tool calls between them at most), and the latest token_count holds the total: the nearest lines say it.
func (f *File) seedUsage(r seedLines, b int, codex bool) (tokenuse.Request, int64) {
	if codex {
		return tokenuse.Request{}, seedCodexTotal(r, b)
	}
	var req tokenuse.Request
	found := false
	budget := int64(seedUsageBytes)
	for n := b - 1; n >= 1 && n >= b-seedUsageLines; n-- {
		sz := r.size(n)
		if sz > seedUsageLine {
			// A reply this big says its request's id near its start, and put the request's usage on an item: a later
			// line of the request adds nothing (Claude Code repeats a request's usage on every line of it).
			switch id := replyHeadID(r.head(n, seedUsageHead), f.meta.Sidechain); {
			case id == "":
				continue // no reply: further back
			case !found || id == req.ID():
				return tokenuse.Saturated(id), 0
			default:
				return req, 0
			}
		}
		if budget -= sz; budget < 0 {
			break
		}
		line := r.line(n)
		if !bytes.Contains(line, usageMark) {
			continue
		}
		scratch := Meta{Sidechain: f.meta.Sidechain, sawUser: true}
		if items := ParseLine(n, line, &scratch); len(items) == 0 || items[0].Usage == nil {
			continue // no reply that put usage on an item: further back
		}
		switch {
		case !found:
			req, found = scratch.usage, true
			if req.ID() == "" { // a request of its own: nothing goes on from it
				return tokenuse.Request{}, 0
			}
		case scratch.usage.ID() != req.ID(): // another request's: req's lines do not go back past it
			return req, 0
		default: // an earlier line of req's: what the request had given by b is the most of them
			req.Merge(scratch.usage)
		}
	}
	return req, 0
}

// seedCodexTotal is a rollout's running total of tokens by line b: its latest token_count's, among the lines just
// before or, further back, where a byte search finds one.
func seedCodexTotal(r seedLines, b int) int64 {
	total := func(n int) int64 {
		if r.size(n) > seedUsageLine { // a token_count is a short line
			return 0
		}
		line := r.line(n)
		if !bytes.Contains(line, tokenCountMark) {
			return 0
		}
		scratch := Meta{Codex: true}
		ParseLine(n, line, &scratch)
		return scratch.codexTotal.Last()
	}
	budget := int64(seedUsageBytes)
	n := b - 1
	for ; n >= 1 && n >= b-seedUsageLines; n-- {
		if budget -= min(r.size(n), seedUsageLine); budget < 0 {
			break
		}
		if t := total(n); t > 0 {
			return t
		}
	}
	for before := n + 1; r.tokenCount != nil; {
		m := r.tokenCount(before)
		if m == 0 {
			return 0
		}
		if t := total(m); t > 0 {
			return t
		}
		before = m // it only mentioned one (a tool's output quoting it): further back
	}
	return 0
}

// replyHeadID is the request id of a Claude Code reply line read only as far as head, "" when head is not one's: the
// line's message (the first one in it, before any content) says role "assistant" and an id among the keys before its
// content. In a session's transcript a sidechain line is no reply of the session's (sidechain: the file is a
// subagent's own, every line sidechain).
func replyHeadID(head []byte, sidechain bool) string {
	if bytes.HasPrefix(head, apiRequestHead) {
		return ""
	}
	i := bytes.Index(head, messageMark)
	if i < 0 || !sidechain && bytes.Contains(head[:i], sidechainMark) {
		return ""
	}
	var id, role string
	rest := head[i+len(messageMark):]
	for {
		rest = rest[skipJSONSpace(rest, 0):]
		if len(rest) == 0 || rest[0] != '"' {
			break
		}
		end := jsonStringEnd(rest, 0)
		if end < 0 {
			break
		}
		key := rest[1:end]
		rest = rest[skipJSONSpace(rest, end+1):]
		if len(rest) == 0 || rest[0] != ':' {
			break
		}
		rest = rest[skipJSONSpace(rest, 1):]
		if len(rest) == 0 || rest[0] != '"' { // content, usage: the keys with a string value come first
			break
		}
		end = jsonStringEnd(rest, 0)
		if end < 0 {
			break
		}
		switch string(key) {
		case "id":
			id = string(rest[1:end])
		case "role":
			role = string(rest[1:end])
		}
		rest = rest[skipJSONSpace(rest, end+1):]
		if len(rest) > 0 && rest[0] == ',' {
			rest = rest[1:]
		}
	}
	if role != "assistant" || strings.ContainsRune(id, '\\') {
		return ""
	}
	return id
}
