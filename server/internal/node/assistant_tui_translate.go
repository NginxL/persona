package node

import (
	"encoding/json"
	"strings"
)

// tuiTranslator turns the transcript an interactive Claude Code writes into the stream-json lines `claude -p` prints,
// the ones readAssistant reads (assistant_tui.go): an init as a turn starts, a stream_event per assistant message, the
// compaction boundary, and a result as the turn ends. The conversation's items still come from the transcript itself;
// these lines only say when it works, what it says live and when it is done.
//
// A turn ends at Claude Code's system/turn_duration line (written after every turn, an API error's too, measured on
// 2.1.325), or at the "[Request interrupted by user]" line an interruption writes instead. Live text arrives a content
// block at a time: an interactive Claude Code writes no partial messages.
type tuiTranslator struct {
	busy     bool
	version  string
	messages map[string]bool // assistant message ids whose message_start went out
	lastText string          // the turn's last assistant text
	apiError bool            // that text is an API error notice (isApiErrorMessage)
	// undelivered says a message sent during the running turn was never typed into Claude Code: the turn ends failed
	// with it, so the message is not lost without a word (undeliverable).
	undelivered string
}

// tuiRow is what the translator reads of a transcript line.
type tuiRow struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Sidechain   bool   `json:"isSidechain"`
	Meta        bool   `json:"isMeta"`
	Version     string `json:"version"`
	APIError    bool   `json:"isApiErrorMessage"`
	Interrupted string `json:"interruptedMessageId"`
	Compact     struct {
		Post int `json:"postTokens"`
	} `json:"compactMetadata"`
	Message struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			Input       int `json:"input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			Output      int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type tuiBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// blocks reads a message's content: a string (what a person typed) is one text block.
func (r *tuiRow) blocks() []tuiBlock {
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		return []tuiBlock{{Type: "text", Text: s}}
	}
	var out []tuiBlock
	_ = json.Unmarshal(r.Message.Content, &out)
	return out
}

func newTUITranslator() *tuiTranslator { return &tuiTranslator{messages: map[string]bool{}} }

// row translates one transcript line into the lines to print (none for most).
func (t *tuiTranslator) row(line []byte) []map[string]any {
	var r tuiRow
	if json.Unmarshal(line, &r) != nil || r.Sidechain {
		return nil
	}
	if r.Version != "" {
		t.version = r.Version
	}
	switch r.Type {
	case "user":
		if r.Meta {
			return nil
		}
		blocks := r.blocks()
		for _, b := range blocks {
			if b.Type == "text" && strings.HasPrefix(strings.TrimSpace(b.Text), "[Request interrupted by user") {
				return t.end("Request interrupted by user", false)
			}
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return nil // within a turn
			}
		}
		return t.start()
	case "assistant":
		out := t.start()
		if id := r.Message.ID; id != "" && !t.messages[id] {
			t.messages[id] = true
			u := r.Message.Usage
			out = append(out, map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start",
				"message": map[string]any{"model": r.Message.Model, "usage": map[string]any{"input_tokens": u.Input,
					"cache_read_input_tokens": u.CacheRead, "cache_creation_input_tokens": u.CacheCreate}}}})
		}
		for _, b := range r.blocks() {
			if b.Type != "text" || b.Text == "" {
				continue
			}
			t.lastText, t.apiError = b.Text, r.APIError
			out = append(out,
				map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta",
					"delta": map[string]any{"type": "text_delta", "text": b.Text}}},
				map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
		}
		return out
	case "system":
		switch r.Subtype {
		case "compact_boundary":
			return []map[string]any{{"type": "system", "subtype": "compact_boundary",
				"compact_metadata": map[string]any{"post_tokens": r.Compact.Post}}}
		case "turn_duration":
			return t.end(t.lastText, t.apiError)
		}
	}
	return nil
}

// start is the init a turn starts with, once.
func (t *tuiTranslator) start() []map[string]any {
	if t.busy {
		return nil
	}
	t.busy, t.lastText, t.apiError = true, "", false
	init := map[string]any{"type": "system", "subtype": "init"}
	if t.version != "" {
		init["claude_code_version"] = t.version
	}
	return []map[string]any{init}
}

// end is the result a turn ends with, once.
func (t *tuiTranslator) end(text string, failed bool) []map[string]any {
	if !t.busy {
		return nil
	}
	t.busy = false
	if t.undelivered != "" {
		text, failed, t.undelivered = t.undelivered, true, ""
	}
	subtype := "success"
	if failed {
		subtype = "error_during_execution"
	}
	return []map[string]any{{"type": "result", "subtype": subtype, "is_error": failed, "result": text}}
}

// idle ends a turn the screen shows over without a line saying so (a local command such as /compact writes none).
func (t *tuiTranslator) idle() []map[string]any { return t.end(t.lastText, t.apiError) }

// undeliverable reports messages the bridge could not type into Claude Code (why: note) as a failed turn, the way
// claude -p reports a message it could not send to the model: at once when no turn runs, or as the end of the running
// turn they were sent into.
func (t *tuiTranslator) undeliverable(note string) []map[string]any {
	if t.busy {
		t.undelivered = note
		return nil
	}
	return append(t.start(), t.end(note, true)...)
}
