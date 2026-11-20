package feishubot

import "encoding/json"

// The parts of a card that CardKit streams into (cardkit.go): an element with an id to write its text into, the
// settings that make a card stream, a copy to change without changing the card, and a coloured tag inside Markdown
// that says a state.

// InlineTag is a colored tag inside Markdown text (a state at the start of a line).
func InlineTag(label, color string) string {
	return "<text_tag color='" + color + "'>" + EscapeMarkdown(label) + "</text_tag>"
}

// MarkdownID adds a block of card Markdown with an element id (what a streaming card writes into), empty or not.
func (c *Card) MarkdownID(id, s string) *Card {
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "markdown", "content": s, "element_id": id})
	return c
}

// Streaming makes the card one that CardKit streams text into (feishubot's CardStream): its text comes in letter by
// letter, and the chat list shows its summary meanwhile.
func (c *Card) Streaming() *Card {
	c.Config["streaming_mode"] = true
	c.Config["streaming_config"] = map[string]any{"print_frequency_ms": map[string]any{"default": 50},
		"print_step": map[string]any{"default": 2}, "print_strategy": "fast"}
	return c
}

// Clone is a copy of c to change without changing c (a card sent two ways: as an entity, else as a message).
func (c *Card) Clone() *Card {
	var cp Card
	_ = json.Unmarshal([]byte(c.JSON()), &cp)
	if cp.Config == nil {
		cp.Config = map[string]any{}
	}
	if cp.Body.Elements == nil {
		cp.Body.Elements = []any{}
	}
	return &cp
}

// EndStreaming takes the streaming settings off again (a card written whole).
func (c *Card) EndStreaming() *Card {
	delete(c.Config, "streaming_mode")
	delete(c.Config, "streaming_config")
	return c
}
