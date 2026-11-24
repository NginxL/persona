// Package cachewarm refreshes captured provider requests outside the agent turn loop.
package cachewarm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
)

// Replay changes only top-level output/stream values. In particular RawMessage
// marshaling is not used: it would compact whitespace inside the cached prefix.
// Manual thinking keeps its original budget and needs budget+1 output tokens.
func Replay(body []byte) ([]byte, int, error) {
	type field struct {
		start, end int
		value      json.RawMessage
	}
	fields := map[string]field{}
	d := json.NewDecoder(bytes.NewReader(body))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, 0, errors.New("unsupported request")
	}
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return nil, 0, err
		}
		key, ok := k.(string)
		if !ok {
			return nil, 0, errors.New("invalid key")
		}
		if _, dup := fields[key]; dup {
			return nil, 0, errors.New("duplicate key")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, 0, err
		}
		end := int(d.InputOffset())
		fields[key] = field{end - len(raw), end, raw}
	}
	if _, err := d.Token(); err != nil {
		return nil, 0, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, 0, errors.New("trailing input")
	}
	if _, ok := fields["max_tokens"]; !ok {
		return nil, 0, errors.New("missing output bound")
	}
	// A 55-minute refresh cannot preserve a five-minute-only cache. Never pay to
	// rewrite one. Look only at actual cache-control declarations, not user text.
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil || !hourCache(req) {
		return nil, 0, errors.New("no one-hour cache")
	}
	limit := 1
	if f, ok := fields["thinking"]; ok {
		var thinking struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		}
		if json.Unmarshal(f.value, &thinking) != nil {
			return nil, 0, errors.New("invalid thinking")
		}
		switch thinking.Type {
		case "enabled":
			if thinking.Budget < 1024 || thinking.Budget > 128000 {
				return nil, 0, errors.New("unsupported thinking budget")
			}
			limit = thinking.Budget + 1
		case "adaptive", "disabled":
		default:
			return nil, 0, errors.New("unknown thinking mode")
		}
	}
	type edit struct {
		start, end int
		value      string
	}
	f := fields["max_tokens"]
	edits := []edit{{f.start, f.end, strconv.Itoa(limit)}}
	if f, ok := fields["stream"]; ok {
		edits = append(edits, edit{f.start, f.end, "false"})
	} else {
		end := bytes.LastIndexByte(body, '}')
		edits = append(edits, edit{end, end, `,"stream":false`})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := bytes.Clone(body)
	for _, e := range edits {
		out = append(append(append([]byte{}, out[:e.start]...), e.value...), out[e.end:]...)
	}
	return out, limit, nil
}

func hourCache(req map[string]json.RawMessage) bool {
	hour, short := false, false
	check := func(v any) {
		if cc, ok := v.(map[string]any); ok && cc["type"] == "ephemeral" {
			if cc["ttl"] == "1h" {
				hour = true
			} else {
				short = true
			}
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			check(x["cache_control"])
			// Only provider block structure; never tool schemas or arbitrary metadata.
			if c, ok := x["content"]; ok {
				walk(c)
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	var cc any
	_ = json.Unmarshal(req["cache_control"], &cc)
	check(cc)
	for _, key := range []string{"system", "tools", "messages"} {
		var v any
		_ = json.Unmarshal(req[key], &v)
		walk(v)
	}
	// Mixed TTLs could read the system prefix yet rewrite a large expired history.
	return hour && !short
}
