package prompts

// RenderCaptureSort is the system prompt that sorts a quick capture into one of the owner's tracks and tags its
// information categories in one call (assistant/capture_sort, #13502, docs/assistant/health.md "Quick capture"). It
// is English whatever the owner's language: the note, the tracks and the owner's choice come as the user message, a
// JSON object of data, and the answer is a slug, numbers, category names and a title in the note's language.
func RenderCaptureSort() string { return render("assistant/capture_sort", nil) }
