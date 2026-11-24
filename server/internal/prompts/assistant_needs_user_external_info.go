package prompts

// NeedsUserExternalInfo is the localized wake context delivered to a task's supervisor when new external information
// arrives while a needs_user decision is still pending (#13112). The existing needs_user card stays untouched until
// supervision either keeps it silently (supervision_finish visibility="silent") or supersedes it via task_review.
type NeedsUserExternalInfo struct {
	Language         string // answerLanguage: "" Chinese, "English", "Japanese" / "日本語"
	Source           string // concise human-readable source label (PR URL, inbox item, thread, prerequisite)
	Summary          string // natural-language summary of the newly arrived external information
	PendingQuestion  string // concise summary of the currently open needs_user ask
	PendingRecommend string // optional recommended option on the pending ask
}

// RenderNeedsUserExternalInfo renders the localized wake prompt for external info arriving during needs_user.
func RenderNeedsUserExternalInfo(p NeedsUserExternalInfo) string {
	return render("assistant/needs_user_external_info", p)
}
