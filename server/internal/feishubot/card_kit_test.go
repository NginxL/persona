package feishubot

import (
	"encoding/json"
	"strings"
	"testing"
)

// cardMap is c as Feishu reads it.
func cardMap(t *testing.T, c *Card) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(c.JSON()), &m); err != nil {
		t.Fatalf("card JSON: %v", err)
	}
	return m
}

// What the card streams into has an element id; a streaming card says so in its config.
func TestStreamingCard(t *testing.T) {
	c := NewCard("", "", "Working").MarkdownID("live", "").Streaming()
	m := cardMap(t, c)
	cfg := m["config"].(map[string]any)
	if cfg["streaming_mode"] != true || cfg["streaming_config"] == nil {
		t.Fatalf("config = %v", cfg)
	}
	el := m["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)
	if el["element_id"] != "live" || el["tag"] != "markdown" {
		t.Fatalf("element = %v", el)
	}
	c.EndStreaming()
	if _, on := c.Config["streaming_mode"]; on {
		t.Errorf("config after the end = %v", c.Config)
	}
}

// InlineTag is a coloured tag inside Markdown.
func TestInlineTag(t *testing.T) {
	if got := InlineTag("Working", Blue); got != "<text_tag color='blue'>Working</text_tag>" {
		t.Fatalf("got %s", got)
	}
	if got := InlineTag("a<b", Blue); strings.Contains(got, "a<b") {
		t.Fatalf("not escaped: %s", got)
	}
}

// A clone is changed without changing the card it was made from.
func TestClone(t *testing.T) {
	c := NewCard("t", Blue, "s").Markdown("a").Buttons(Btn("b", Primary, map[string]any{"a": "x"}))
	before := c.JSON()
	cp := c.Clone().Streaming().Markdown("more")
	cp.Body.Elements[0].(map[string]any)["content"] = "changed"
	if c.JSON() != before {
		t.Fatalf("the card changed: %s", c.JSON())
	}
	if !strings.Contains(cp.JSON(), "streaming_mode") || !strings.Contains(cp.JSON(), "changed") {
		t.Fatalf("clone %s", cp.JSON())
	}
}
