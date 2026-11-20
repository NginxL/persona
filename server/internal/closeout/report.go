package closeout

import (
	"strings"

	"github.com/nwverse/persona/server/internal/textfmt"
)

const reportForm = `

- Status: done|not_done|blocked
- Merged: yes|no|n/a
- Deployed: yes|no|n/a
- Did: …
- Artifact: pr|commit|branch|doc|build|url|file …
- Delivery: live|pr|merged|off|none
- Unblock: user|agent|other
- Verified: …
- Missing: …
- Waiting: …

---`

// HasMarkdownInstructions identifies the form already supplied with the agent's task.
func HasMarkdownInstructions(instructions string) bool {
	for _, heading := range []string{textfmt.WorkReportEnglish, textfmt.WorkReportChinese, textfmt.WorkReportJapanese} {
		if strings.Contains(instructions, heading+reportForm) {
			return true
		}
	}
	return false
}

// MarkdownExample is the terminal-readable form supplied to a node that announced version 1.
func MarkdownExample(language string) string {
	switch language {
	case "日本語", "ja":
		return textfmt.WorkReportJapanese + reportForm
	case "", "简体中文", "zh-Hans":
		return textfmt.WorkReportChinese + reportForm
	default:
		return textfmt.WorkReportEnglish + reportForm
	}
}
