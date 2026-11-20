package closeout

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/textfmt"
)

const blockedReport = `### Persona work report

- Status: blocked
- Merged: no
- Deployed: n/a
- Did: Wrote the fixture.
- Artifact: pr #24
- Delivery: pr
- Unblock: other
- Verified: Ran four checks.
- Missing: A review.
- Waiting: The reviewer.

---`

func TestSharedWorkReportSamples(t *testing.T) {
	data, err := os.ReadFile("../../../spec/fixtures/work-reports/terminal.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name, Text, Display string
			Markdown            bool
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatalf("invalid fixture: %v", err)
	}
	for _, sample := range fixture.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			report, ok := textfmt.FinalWorkReport(sample.Text)
			if ok != sample.Markdown {
				t.Fatalf("recognized = %v, want %v", ok, sample.Markdown)
			}
			if ok {
				c, artifacts, _ := Parse(sample.Text)
				legacy := report.LegacyText(sample.Text)
				if sample.Name == "reserved-next" && (!strings.Contains(legacy, "- Next: Check the next sample.") || !WaitsOnNobody(sample.Text)) {
					t.Fatal("reserved field must survive transport and leave a completed turn complete")
				}
				projected, refs, valid := Parse(legacy)
				if !valid || !reflect.DeepEqual(c, projected) || !reflect.DeepEqual(artifacts, refs) {
					t.Fatalf("older client's form changed the fields: %+v / %+v, %+v / %+v", c, projected, artifacts, refs)
				}
			}
		})
	}
}

func TestReportedNextStepSurvivesLegacyProjection(t *testing.T) {
	const next = "Compare the two drafts."
	for _, heading := range []string{textfmt.WorkReportEnglish, textfmt.WorkReportChinese, textfmt.WorkReportJapanese} {
		report := heading + "\n\n- Status: done\n- Merged: n/a\n- Deployed: n/a\n- Next: " + next + "\n\n---"
		decoded, ok := textfmt.FinalWorkReport(report)
		if !ok {
			t.Fatalf("unrecognized report %q", heading)
		}
		for _, text := range []string{report, decoded.LegacyText(report)} {
			got, _, ok := Parse(text)
			if !ok || got.Next != next || !WaitsOnNobody(text) {
				t.Errorf("%q did not offer its optional reply: %+v", heading, got)
			}
		}
		for _, invalid := range []string{
			strings.Replace(report, "- Status: done", "- Status: blocked", 1),
			strings.Replace(report, "\n\n---", "\n- Missing: Decide on the format.\n\n---", 1),
			strings.Replace(report, "\n\n---", "\n- Next: A second reply.\n\n---", 1),
		} {
			got, _, _ := Parse(invalid)
			if got.Next != "" {
				t.Fatalf("unexpected next step %q", got.Next)
			}
		}
	}
}

func TestTerminalWorkReport(t *testing.T) {
	old := "<closeout status=\"blocked\" merged=\"no\" deployed=\"n/a\">\ndid: Wrote the fixture.\nartifact: pr #24\ndelivery: pr\nunblock: other\nverified: Ran four checks.\nmissing: A review.\nwaiting: The reviewer.\n</closeout>"
	want, wantArtifacts, _ := Parse(old)
	for _, heading := range []string{"### Persona work report", "### Persona 工作报告", "### Persona 作業報告"} {
		report := strings.Replace(blockedReport, "### Persona work report", heading, 1)
		for _, ending := range []string{"\n", "\r\n"} {
			reply := strings.ReplaceAll("My reply.\n\n"+report, "\n", ending)
			got, artifacts, ok := Parse(reply)
			if !ok || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(artifacts, wantArtifacts) {
				t.Errorf("parse %s, %q: %#v %#v, ok=%v", heading, ending, got, artifacts, ok)
			}
			if got := Strip(reply); got != "My reply." {
				t.Errorf("strip %s, %q left %q", heading, ending, got)
			}
		}
	}
	done := strings.Replace(blockedReport, "Status: blocked", "Status: done", 1)
	done = strings.Replace(done, "- Missing: A review.\n- Waiting: The reviewer.\n", "", 1)
	if !WaitsOnNobody(done) {
		t.Error("a completed report with no unanswered item should not reopen the turn")
	}
	if c, _, ok := Parse(old + "\n\n" + done); !ok || c.Status != "done" {
		t.Errorf("new final report did not supersede prior archived XML: %#v %v", c, ok)
	}
}

func TestOrdinaryPartialAndQuotedMarkdownStayVisible(t *testing.T) {
	fence := strings.Repeat("`", 3)
	for name, text := range map[string]string{
		"ordinary list":    strings.Replace(blockedReport, "### Persona work report", "### My next task", 1),
		"quotation":        "> " + strings.ReplaceAll(blockedReport, "\n", "\n> "),
		"indented example": "    " + strings.ReplaceAll(blockedReport, "\n", "\n    "),
		"fenced example":   fence + "md\n" + blockedReport + "\n" + fence,
		"open tilde fence": "~~~~markdown\n" + blockedReport,
		"partial head":     blockedReport[:20],
		"missing end":      strings.TrimSuffix(blockedReport, "---"),
		"missing status":   strings.Replace(blockedReport, "- Status: blocked\n", "", 1),
		"invalid status":   strings.Replace(blockedReport, "Status: blocked", "Status: pending", 1),
		"missing deployed": strings.Replace(blockedReport, "- Deployed: n/a\n", "", 1),
		"duplicate status": strings.Replace(blockedReport, "- Status: blocked", "- Status: blocked\n- Status: done", 1),
		"unexpected text":  strings.Replace(blockedReport, "- Waiting: The reviewer.", "More prose.", 1),
		"trailing answer":  blockedReport + "\n\nHere is what I think.",
	} {
		t.Run(name, func(t *testing.T) {
			if Has(text) || Strip(text) != strings.TrimSpace(text) {
				t.Fatalf("non-report was consumed: %q", text)
			}
		})
	}
}
