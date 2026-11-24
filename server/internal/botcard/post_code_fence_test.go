package botcard

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	mdtext "github.com/yuin/goldmark/text"
)

// Exercise the public event/content parsers with synthetic wire JSON, not richNode.
func TestPostCodeBlockFence(t *testing.T) {
	cases := []struct{ name, language, body, fence string }{
		{"ordinary", "GO", "x := 1\n", "```"},
		{"empty", "", "", "```"},
		{"trailing newlines", "GO", "x := 1\n\n", "```"},
		{"short inline ticks", "", "say `one` and ``two``", "```"},
		{"tilde fence", "MARKDOWN", "~~~go\nx := 1\n~~~", "```"},
		{"nested Markdown", "MARKDOWN", "# Example\n```go\nx := 1\n```\n# Still literal", "````"},
		{"longer fence", "MARKDOWN", "````\n# Still literal", "`````"},
		{"indented closing fence", "", "start\n   ``` \t\n# Still literal", "````"},
		{"long run", "", strings.Repeat("`", 128) + "\n# Still literal", strings.Repeat("`", 129)},
	}
	for _, tc := range cases {
		for _, localized := range []bool{false, true} {
			name := tc.name + "/direct"
			if localized {
				name = tc.name + "/localized"
			}
			t.Run(name, func(t *testing.T) {
				post := map[string]any{"title": "Sample", "content": [][]map[string]string{
					{{"tag": "text", "text": "Before"}},
					{{"tag": "code_block", "language": tc.language, "text": tc.body}},
					{{"tag": "a", "text": "After", "href": "https://example.com/after"}, {"tag": "img", "image_key": "img_example"}},
				}}
				var wirePost any = post
				if localized {
					wirePost = map[string]any{"en_us": post}
				}
				contentJSON, err := json.Marshal(wirePost)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := json.Marshal(map[string]any{
					"schema": "2.0", "header": map[string]string{"event_type": EventMessage},
					"event": map[string]any{"message": map[string]string{"message_type": "post", "content": string(contentJSON)}},
				})
				if err != nil {
					t.Fatal(err)
				}
				env, err := ParseEnvelope(payload)
				if err != nil {
					t.Fatal(err)
				}
				var ev MessageEvent
				if err := json.Unmarshal(env.Event, &ev); err != nil {
					t.Fatal(err)
				}
				got := ParseContent(ev.Message.MessageType, ev.Message.Content, ev.Message.Mentions)
				body := strings.TrimRight(tc.body, "\n")
				want := "**Sample**\nBefore\n\n" + tc.fence + strings.ToLower(tc.language) + "\n" + body + "\n" + tc.fence + "\n\n[After](https://example.com/after)[图片]"
				if got.Text != want {
					t.Errorf("text = %q; want %q", got.Text, want)
				}
				if len(got.Media) != 1 || got.Media[0] != (Media{Kind: "image", Key: "img_example"}) {
					t.Errorf("media = %#v", got.Media)
				}
				if label, rest, ok := got.CutPostLink("[After](https://example.com/after) rest"); !ok || label != "After" || rest != " rest" {
					t.Errorf("link = %q %q %v", label, rest, ok)
				}
				// A real Markdown parser must retain exactly one block containing the entire source.
				source := []byte(got.Text)
				doc := goldmark.DefaultParser().Parse(mdtext.NewReader(source))
				blocks := 0
				err = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
					if block, ok := n.(*ast.FencedCodeBlock); ok && entering {
						blocks++
						var literal strings.Builder
						for i := 0; i < block.Lines().Len(); i++ {
							line := block.Lines().At(i)
							literal.Write(line.Value(source))
						}
						expected := body + "\n"
						if literal.String() != expected {
							t.Errorf("code body = %q; want %q", literal.String(), expected)
						}
						if string(block.Language(source)) != strings.ToLower(tc.language) {
							t.Errorf("language = %q", block.Language(source))
						}
					}
					return ast.WalkContinue, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if blocks != 1 {
					t.Errorf("code blocks = %d; want 1", blocks)
				}
			})
		}
	}
}

func TestPostCodeBlocksWithAdjacentNodes(t *testing.T) {
	const post = `{"content":[[{"tag":"text","text":"Before"},{"tag":"code_block","language":"MARKDOWN","text":"a\n` + "```" + `\nb"},{"tag":"text","text":"Between"},{"tag":"code_block","language":"GO","text":"x := 1\n"},{"tag":"text","text":"After"}]]}`
	const want = "Before\n````markdown\na\n```\nb\n````\nBetween\n```go\nx := 1\n```\nAfter"
	if got := ParseContent("post", post, nil).Text; got != want {
		t.Fatalf("text = %q; want %q", got, want)
	}
}
