package prompts

// AssistantSpaceClaudeMD is the instructions of a track whose template is space (assistant/space_claude_md, #13472,
// docs/assistant/health.md "Tracks and kinds"): a short baseline, which the node follows with the track's SPACE.md.
type AssistantSpaceClaudeMD struct {
	Owner    string // "" is 用户
	Title    string // the track's title, one line
	StateDir string // "" is ~/.persona
	DirName  string // the track's directory, relative to StateDir (tracks/<slug>)
	Notes    string // Settings › Assistant › About me
	Language string // the answer language's own name; "" is Chinese
}

func RenderAssistantSpaceClaudeMD(d AssistantSpaceClaudeMD) string {
	return render("assistant/space_claude_md", d)
}

// AssistantSpaceMD is a track's first SPACE.md (assistant/space_md), written once in the owner's language and the
// user's from then on: a title, three empty sections and the state block Persona keeps.
type AssistantSpaceMD struct {
	Lang  string // "en" or "ja"; "" is Chinese
	Title string
}

func RenderAssistantSpaceMD(d AssistantSpaceMD) string { return render("assistant/space_md", d) }
