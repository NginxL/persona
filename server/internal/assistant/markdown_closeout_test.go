package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

type closeoutDevices struct{ *fleetDevs }

func (d closeoutDevices) NodeAnnounced(id, capability string) bool {
	return id == "box" && capability == nodeproto.MarkdownCloseout
}

func TestWorkReportNegotiatedForEachRecipient(t *testing.T) {
	s, f := newTaskService(t)
	s.devs = closeoutDevices{newFleetDevs()}
	closeoutOn(t, s)
	check := func(name, prompt string, markdown bool) {
		t.Helper()
		if strings.Contains(prompt, "### Persona ") != markdown || strings.Contains(prompt, "<closeout status=") == markdown {
			t.Errorf("%s: inconsistent form in %q", name, prompt)
		}
	}
	for _, tc := range []struct{ locale, heading, instruction string }{
		{"en", "Persona work report", "first three required fields"},
		{"ja", "Persona 作業報告", "最初の3つの必須項目"},
		{"zh-Hans", "Persona 工作报告", "前三个必填字段"},
	} {
		setAgentLanguage(f, tc.locale)
		prompt := s.closeoutStart(t.Context(), "box", assistantwire.CloseoutBriefRequest{SessionID: "sample", Cwd: "/work"}).Context
		if !strings.Contains(prompt, "### "+tc.heading) || !strings.Contains(prompt, tc.instruction) || !strings.Contains(prompt, "- Next:") {
			t.Errorf("%s instructions: %q", tc.locale, prompt)
		}
	}
	for _, id := range []string{"box", "other"} {
		check("start hook "+id, s.closeoutStart(t.Context(), id, assistantwire.CloseoutBriefRequest{SessionID: "sample", Cwd: "/work"}).Context, id == "box")
		check("stop hook "+id, s.closeoutCheck(t.Context(), id, assistantwire.CloseoutCheckRequest{SessionID: "sample"}).Reason, id == "box")
	}
	if _, _, err := s.startTaskAgent(t.Context(), &PlanTask{Title: "Sample", Prompt: "Review four examples.", Machine: "box", Cwd: "/code/app"}, false); err != nil {
		t.Fatal(err)
	}
	check("to-do dispatch", f.starts[0]["prompt"].(string), true)
	if _, err := s.runTool(t.Context(), "server", "conv9", "agent_start", json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"Review the example."}`)); err != nil {
		t.Fatal(err)
	}
	check("agent_start", f.starts[len(f.starts)-1]["prompt"].(string), true)
	d, df, _ := newDispatchService(t, true)
	d.devs = closeoutDevices{newFleetDevs()}
	closeoutOn(t, d)
	if r := dispatchCall(d, "mac", "caller-0001", "dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review the example."}`); r.IsError {
		t.Fatal(resultText(r))
	}
	check("dispatch_start", df.starts[0]["prompt"].(string), true)

	ask, af := askService(t)
	askingDevices := newFleetDevs()
	askingDevices.add("box", "box")
	ask.devs = closeoutDevices{askingDevices}
	stopped := justStopped("s1", askReply, time.Minute)
	askingDevices.setSessions("box", []proto.Session{stopped})
	af.setSessions("box", stopped)
	triage(t, ask, "s1")
	askTick(ask)
	texts := af.sentToAgents()
	if len(texts) != 1 {
		t.Fatalf("status requests: %v", texts)
	}
	check("later status request", texts[0], true)
}

func TestTriageOnlyConsumesACompleteTerminalWorkReport(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		size       int
		accepted   bool
	}{
		{"whole", researchMarkdown, 0, true},
		{"indented", "    " + researchMarkdown, 0, false},
		{"clipped", researchMarkdown, 8192, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			f.triage = `{"phase":"blocked","on":"user","line":"Waiting for the last sample."}`
			f.setSessions("box", stopped("s1", tc.text))
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: tc.text, FullSize: tc.size})
			triage(t, s, "s1")
			w, _ := reportOf(t, s, "s1")
			if (w.Closeout != nil) != tc.accepted || (len(f.triageAsked) == 0) != tc.accepted {
				t.Fatalf("closeout=%+v, model calls=%d", w.Closeout, len(f.triageAsked))
			}
		})
	}
}

func TestDeliveredWorkReportIsTheFinalReply(t *testing.T) {
	report := "### Persona work report\n\n- Status: done\n- Merged: no\n- Deployed: n/a\n- Artifact: pr #24\n- Verified: Four local checks passed.\n\n---"
	for name, reply := range map[string]string{"bare": report, "after prose": "The checks passed.\n\n" + report,
		"after closed listing": "~~~\nExample only.\n~~~\n" + report, "permitted citation footer": report + "\n<oai-mem-citation>local note</oai-mem-citation>"} {
		if got := closeoutPRRef(reply); got != "#24" {
			t.Errorf("%s: got %q", name, got)
		}
	}
	for name, reply := range map[string]string{"quoted": "> " + strings.ReplaceAll(report, "\n", "\n> "),
		"fenced": "~~~\n" + report + "\n~~~", "open fence": "~~~markdown\n" + report,
		"truncated": strings.TrimSuffix(report, "---"), "earlier": report + "\n\nThat was a sample.",
		"indented": "    " + strings.ReplaceAll(report, "\n", "\n    "), "ordinary heading": strings.Replace(report, "### Persona work report", "### Weekly notes", 1)} {
		if got := closeoutPRRef(reply); got != "" {
			t.Errorf("%s: treated %q as the delivered PR", name, got)
		}
	}
}
