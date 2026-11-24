package botcard

import (
	"encoding/json"
	"strings"
)

// Keep the encoded post below the recommended Feishu message size, with room for the reply
// envelope. Longer answers are attached whole instead of being clipped or split into replies.
const inlineAnswerMax = 24 << 10

// InlineAnswer encodes literal text. The rich-text text tag preserves the body without
// activating Feishu's mention syntax or Markdown extensions from an Agent's answer.
func InlineAnswer(text string) (string, bool) {
	if len(text) > inlineAnswerMax {
		return "", false
	}
	var lines [][]map[string]string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line == "" {
			line = "\u00a0" // keep a blank paragraph with a nonempty text element
		}
		lines = append(lines, []map[string]string{{"tag": "text", "text": line}})
	}
	content, _ := json.Marshal(map[string]any{"zh_cn": map[string]any{"content": lines}})
	wire, _ := json.Marshal(string(content))
	return string(content), len(wire) <= inlineAnswerMax
}
