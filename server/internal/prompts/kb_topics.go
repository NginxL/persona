package prompts

import "github.com/nwverse/persona/server/internal/kbwire"

// The knowledge points curation (docs/assistant/knowledge.md "Topics"): distilling candidate points out of a project's
// timeline or a chat's summary (kb/topic_distill), folding a topic's candidates into its points (kb/topic_merge), and
// the topic's summary.md written from its points (kb/topic_summary).

// KBTopicRef is an existing topic the distill step may file points under.
type KBTopicRef struct {
	Name    string
	Aliases []string
	Note    string
}

// KBTopicDistillInput is one source for the distill step: a stretch of a project's timeline (Project) or a chat's
// compressed context.
type KBTopicDistillInput struct {
	Language string // Instruction language; source fields remain unchanged.
	Project  bool
	Name     string
	Goal     string
	// A project's timeline lines From–To (1-based) of Total.
	Lines           []string
	From, To, Total int
	Summary         string // a chat's
	Topics          []KBTopicRef
}

// KBTopicDistill is the distill step's system prompt: MaxPoints is how many points one call answers with at most.
type KBTopicDistill struct {
	Language  string // Instruction language; source fields remain unchanged.
	Owner     string
	MaxPoints int
}

func RenderKBTopicDistill(d KBTopicDistill) string { return render("kb/topic_distill", d) }
func RenderKBTopicDistillInput(in KBTopicDistillInput) string {
	return render("kb/topic_distill_input", in)
}

// KBTopicMerge is the merge step's system prompt: MaxPoints is how many points a topic keeps at most.
type KBTopicMerge struct {
	Language  string // Instruction language; source fields remain unchanged.
	Owner     string
	MaxPoints int
}

// KBTopicMergeInput is one topic's points and the candidates to fold in (their IDs are c1, c2…).
type KBTopicMergeInput struct {
	Language   string // Instruction language; source fields remain unchanged.
	Name       string
	Aliases    []string
	Note       string
	Pinned     string
	Points     []kbwire.TopicPoint
	Candidates []kbwire.TopicPoint
}

func RenderKBTopicMerge(m KBTopicMerge) string { return render("kb/topic_merge", m) }
func RenderKBTopicMergeInput(in KBTopicMergeInput) string {
	return render("kb/topic_merge_input", in)
}

// KBTopicGroup is the points under one subtopic heading ("## a", "### a / b"; none for the topic's own points).
type KBTopicGroup struct {
	Heading string
	Points  []kbwire.TopicPoint
}

// KBTopicSummary is a topic's summary.md: what search and the assistant read; the points file stays the record.
type KBTopicSummary struct {
	Name   string
	Note   string
	Groups []KBTopicGroup
}

func RenderKBTopicSummary(s KBTopicSummary) string { return render("kb/topic_summary", s) }
