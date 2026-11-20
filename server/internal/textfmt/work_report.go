package textfmt

import (
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// The published Markdown v1 headings and field names are part of the record across upgrades.
const (
	WorkReportEnglish  = "### Persona work report"
	WorkReportChinese  = "### Persona 工作报告"
	WorkReportJapanese = "### Persona 作業報告"
)

// Copied labels are the same across languages. Free text keeps the Agent's language.
var reportFields = lazyre.New(`\A(?:` + WorkReportEnglish + "|" + WorkReportChinese + "|" + WorkReportJapanese + `)\r?\n(?:\r?\n)?- Status: (done|not_done|blocked)\r?\n- Merged: (yes|no|n/a)\r?\n- Deployed: (yes|no|n/a)\r?\n((?:- (?:Did|Artifact|Delivery|Unblock|Verified|Missing|Waiting|Next): [^\r\n]+\r?\n)*)(?:\r?\n)?---\z`)

// WorkReport denotes the bytes and stable fields of the final report as written.
// The runtime's existing citation footer can follow it.
type WorkReport struct {
	Start, End                     int
	Status, Merged, Deployed, Body string
}

// FinalWorkReport reads only a complete Markdown work report at the end of the reply.
func FinalWorkReport(reply string) (WorkReport, bool) {
	if !strings.Contains(reply, "### Persona ") {
		return WorkReport{}, false
	}
	text := strings.TrimRight(reply, " \t\r\n")
	if strings.HasSuffix(text, "</oai-mem-citation>") {
		if footer := strings.LastIndex(text, "\n<oai-mem-citation>"); footer >= 0 {
			text = strings.TrimRight(text[:footer], " \t\r\n")
		}
	}
	if !strings.HasSuffix(text, "\n---") {
		return WorkReport{}, false
	}
	start := strings.LastIndex(text, "\n### Persona ") + 1
	block := text[start:]
	fields := reportFields.FindStringSubmatch(block)
	if fields == nil || insideFence(text[:start]) {
		return WorkReport{}, false
	}
	return WorkReport{Start: start, End: len(text), Status: fields[1], Merged: fields[2], Deployed: fields[3], Body: fields[4]}, true
}

var escapeReportTags = strings.NewReplacer("<closeout", "&lt;closeout", "</closeout", "&lt;/closeout")

// LegacyText rewrites only a validated report for clients that strip the XML form when its card is shown.
// A literal tag in the report's free text cannot close the wrapper before the reported fields end.
func (r WorkReport) LegacyText(reply string) string {
	body := escapeReportTags.Replace(r.Body)
	return reply[:r.Start] + "<closeout status=\"" + r.Status + "\" merged=\"" + r.Merged + "\" deployed=\"" + r.Deployed + "\">\n" +
		body + "</closeout>" + reply[r.End:]
}

// Markdown code fences use at least three backticks or tildes. A closing fence must be at least
// as long as its opener, and may only be followed by whitespace.
func insideFence(before string) bool {
	var open string
	for line := range strings.Lines(before) {
		line = strings.TrimSpace(line)
		if len(line) < 3 || line[0] != '`' && line[0] != '~' {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, line[:1]))
		if n < 3 {
			continue
		}
		if open == "" {
			open = line[:n]
		} else if strings.HasPrefix(line[:n], open) && strings.TrimSpace(line[n:]) == "" {
			open = ""
		}
	}
	return open != ""
}
