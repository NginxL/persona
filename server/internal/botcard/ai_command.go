package botcard

import (
	"strings"
	"unicode"
)

// AICommand is the line after "/ai" (or the full-width "／ai", whatever its case) when text starts with it as a word of
// its own ("/ai fix it", "/ai", but not "/aim"); ok is false for any other text. The hub reads it to take the owner's
// "@bot /ai …" from a group that is not opted in, and the assistant to open a to-do from a topic.
func AICommand(text string) (rest string, ok bool) {
	r := []rune(strings.TrimSpace(text))
	if len(r) < 3 || r[0] != '/' && r[0] != '／' || !strings.EqualFold(string(r[1:3]), "ai") { // i18n-ignore: the full-width slash
		return "", false
	}
	if len(r) > 3 && !unicode.IsSpace(r[3]) {
		return "", false
	}
	return strings.TrimSpace(string(r[3:])), true
}

// StripLeadingMentions is text without the mentions it starts with, as the message's own @-keys ("@_user_1") or the
// names they stand for ("@Persona"), keep(m) deciding which mentions are taken off (nil: all of them). A mention is a
// whole word: "@_user_1" is not the start of "@_user_10".
func StripLeadingMentions(text string, mentions []Mention, keep func(Mention) bool) string {
	t := strings.TrimSpace(text)
	for stripped := true; stripped; {
		stripped = false
		for _, m := range mentions {
			if keep != nil && !keep(m) {
				continue
			}
			for _, tok := range []string{m.Key, "@" + m.Name} {
				if tok == "" || tok == "@" {
					continue
				}
				if rest, ok := strings.CutPrefix(t, tok); ok && (rest == "" || unicode.IsSpace([]rune(rest)[0])) {
					t, stripped = strings.TrimSpace(rest), true
				}
			}
		}
	}
	return t
}

// StripLeadingMentions uses a post's actual at elements and their IDs, so an adjacent text
// element needs no literal space. Display names never identify a structured mention.
// Plain text keeps StripLeadingMentions' whole-word rule.
func (c Content) StripLeadingMentions(mentions []Mention, keep func(Mention) bool) string {
	if c.post == nil {
		return StripLeadingMentions(c.Text, mentions, keep)
	}
	p := *c.post
	if p.Title != "" {
		return c.Text
	}
	stripped := false
	for row, nodes := range p.Content {
		for col, node := range nodes {
			if node.Tag == "text" && strings.TrimSpace(node.Text) == "" {
				continue
			}
			if node.Tag == "at" && node.UserID != "" {
				matched := false
				for _, m := range mentions {
					if node.UserID == m.Key || node.UserID == m.ID.OpenID || node.UserID == m.ID.UserID || node.UserID == m.ID.UnionID {
						matched = keep == nil || keep(m)
						break
					}
				}
				if matched {
					stripped = true
					continue
				}
			}
			if !stripped {
				return c.Text
			}
			// Stop at the first real word or a mention the caller did not authorize.
			p.Content = append([][]richNode{nodes[col:]}, p.Content[row+1:]...)
			return strings.TrimSpace(nameMentionKeys(renderPost(p, mentions).Text, mentions))
		}
	}
	return ""
}
