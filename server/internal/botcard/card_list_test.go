package botcard

import (
	"strings"
	"testing"
)

// A model answer's nested list comes out nested in the card. Lark's Markdown nests a list by four leading spaces per
// level, while CommonMark (and so a model) indents a sub-item by its parent marker's width: two spaces under "- ",
// three under "1. ". Passed through unchanged, those sub-items are siblings to Lark: the hierarchy the answer was
// written with is gone.
func TestCardMarkdownNestedListIndent(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"two spaces are one level", "- top\n  - child\n    - grandchild", "- top\n    - child\n        - grandchild"},
		{"three spaces under an ordered item", "1. step\n   - sub\n2. step", "1. step\n    - sub\n2. step"},
		{"ordered sub-list", "- top\n  1. first\n  2. second", "- top\n    1. first\n    2. second"},
		{"a blank line keeps the list", "- top\n\n  - child", "- top\n\n    - child"},
		{"a paragraph ends it", "- top\n  - child\n\nparagraph\n\n  - next", "- top\n    - child\n\nparagraph\n\n- next"},
		{"a paragraph with trailing spaces ends it", "- top\n  - child\n\ntext \n\n  - next", "- top\n    - child\n\ntext \n\n- next"},
		{"tabs count as a level", "- top\n\t- child", "- top\n    - child"},
		{"a top-level item loses stray indentation", " - top\n - second", "- top\n- second"},
		{"fenced code stays as written", "```\n- top\n  - child\n\t- tab\n```", "```\n- top\n  - child\n\t- tab\n```"},
		{"a list goes on around a code block", "- top\n  - child\n  ```\n  code\n  ```\n  - next", "- top\n    - child\n  ```\n  code\n  ```\n    - next"},
		{"indented code is not a list", "text\n\n    - not a list item", "text\n\n    - not a list item"},
		{"continuation lines stay as written", "- top\n  wrapped words\n  - child", "- top\n  wrapped words\n    - child"},
		{"a code span is not a marker", "- `a`\n  - `b`", "- `a`\n    - `b`"},
		{"a quoted list is left alone", "> - a\n>   - b", "> - a\n>   - b"},
		{"carriage returns stay", "- top\r\n  - child\r\n\r\ntext\r\n\r\n  - next\r\n", "- top\r\n    - child\r\n\r\ntext\r\n\r\n- next\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CardMarkdown(tc.in); got != tc.want {
				t.Fatalf("CardMarkdown(%q):\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// The answer card itself carries the nesting (the same builder the bot's answer uses), and the rich-text fallback
// keeps the answer's own two-space Markdown, which it renders as written.
func TestAnswerCardKeepsListNesting(t *testing.T) {
	const answer = "## Result\n- fixed\n  - spaces\n  - escapes\n- verified"
	_, cards := renderCards(t, answer, 6000, 5)
	var body []string
	for _, e := range cards[0].elements() {
		if s, ok := e["content"].(string); ok {
			body = append(body, s)
		}
	}
	if got := strings.Join(body, "\n"); !strings.Contains(got, "- fixed\n    - spaces\n    - escapes\n- verified") {
		t.Fatalf("card body %q does not nest the sub-items by four spaces", got)
	}
}
