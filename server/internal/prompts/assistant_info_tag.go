package prompts

// InfoTag is the system prompt that tags a text with the information categories it carries (assistant/info_tag,
// #13246, docs/assistant/health.md "Information categories"). It is English whatever the owner's language: its
// answer is category names only, and the text may be in any language.
type InfoTag struct {
	Focus []string // the categories the text's track wants looked for especially (its kind's tag focus), may be empty
}

func RenderInfoTag(t InfoTag) string { return render("assistant/info_tag", t) }
