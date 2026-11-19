package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func heartbeatItem(t *testing.T, m *Meta, seq int, kind, phase, text string) proto.Item {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"` + kind + `","phase":"` + phase + `","content":[{"type":"text","text":` + string(quoted) + `}]}}}`
	items := ParseLine(seq, []byte(line), m)
	if len(items) != 1 {
		t.Fatalf("got %d items for %s: %+v", len(items), kind, items)
	}
	return items[0]
}

const heartbeatStart = `<heartbeat><automation_id>sample-check</automation_id>`
const heartbeatPromptStart = heartbeatStart + `<current_time_iso>2026-01-02T03:04:05.678Z</current_time_iso>`

func TestCodexHeartbeatTurn(t *testing.T) {
	m := &Meta{}
	ParseLine(1, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"example"}}`), m)
	request := heartbeatPromptStart + "\n<instructions>\nCheck build &amp; deploy.\n<![CDATA[Literal <example> & notes]]>\n</instructions>\n</heartbeat>"
	u := heartbeatItem(t, m, 2, "UserMessage", "", request)
	if u.Role != "user" || u.Kind != proto.KindText || u.Source == nil || u.Source.Kind != proto.SourceScheduledTask || u.Text != "Check build & deploy.\nLiteral <example> & notes" || m.FirstUser != "" {
		t.Fatalf("scheduled input: %+v (first user: %q)", u, m.FirstUser)
	}
	a := heartbeatItem(t, m, 3, "AgentMessage", "final_answer", heartbeatStart+`<decision>DONT_NOTIFY</decision><message>Nothing new.</message></heartbeat>`)
	if a.Text != codexHeartbeatQuietText || !a.CodexHeartbeatQuiet || a.Role != "assistant" || a.Narration || m.LastText != a.Text || !m.TurnOpen {
		t.Fatalf("quiet answer: %+v (last text: %q, open: %t)", a, m.LastText, m.TurnOpen)
	}
	end := ParseLine(4, []byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"example","duration_ms":12000}}`), m)
	if len(end) != 1 || end[0].TurnDuration == nil || end[0].TurnDuration.UserItemID != u.ID || end[0].TurnDuration.DurationMS != 12000 || m.TurnOpen || !m.TurnClosed {
		t.Fatalf("finished scheduled turn: %+v (open: %t, closed: %t)", end, m.TurnOpen, m.TurnClosed)
	}
}

func TestCodexHeartbeatAnswer(t *testing.T) {
	quiet := heartbeatStart + `<decision>DONT_NOTIFY</decision></heartbeat>`
	notify := heartbeatStart + `<decision>NOTIFY</decision><message>**Build ready** &amp; queued.

Next: run tests.</message></heartbeat>`
	for _, c := range []struct {
		phase, text, want string
		quiet             bool
	}{
		{"final_answer", quiet, codexHeartbeatQuietText, true},
		{"final_answer", notify, "**Build ready** & queued.\n\nNext: run tests.", false},
		{"final_answer", heartbeatStart + `<decision>NOTIFY</decision><message>` + codexHeartbeatQuietText + `</message></heartbeat>`, codexHeartbeatQuietText, false},
		{"final_answer", codexHeartbeatQuietText, codexHeartbeatQuietText, false},
		{"commentary", quiet, quiet, false},
		{"final_answer", "Example: " + quiet, "Example: " + quiet, false},
		{"final_answer", "```xml\n" + notify + "\n```", "```xml\n" + notify + "\n```", false},
	} {
		a := heartbeatItem(t, &Meta{}, 1, "AgentMessage", c.phase, c.text)
		if a.Text != c.want || a.CodexHeartbeatQuiet != c.quiet {
			t.Errorf("%s: %q → (%q, quiet=%t), want (%q, quiet=%t)", c.phase, c.text, a.Text, a.CodexHeartbeatQuiet, c.want, c.quiet)
		}
	}
}

func TestCodexHeartbeatDoesNotConsumeOrdinaryXML(t *testing.T) {
	prompt := heartbeatPromptStart + `<instructions>Check builds</instructions></heartbeat>`
	for _, text := range []string{
		"```xml\n" + prompt + "\n```",
		prompt + "\nAn example, not an automation.",
		prompt + prompt,
		strings.Replace(prompt, "2026-01-02T03:04:05.678Z", "not a timestamp", 1),
		strings.Replace(prompt, "Check builds", "Check <app>builds</app>", 1),
		strings.Replace(prompt, "</instructions>", "", 1),
		strings.Replace(prompt, "<instructions>", "<instructions debug=\"true\">", 1),
		strings.Replace(prompt, "</instructions>", "</instructions><instructions>Duplicate</instructions>", 1),
		`<heartbeat><title>Other XML</title></heartbeat>`,
	} {
		u := heartbeatItem(t, &Meta{}, 1, "UserMessage", "", text)
		if u.Source != nil || u.Text != text {
			t.Errorf("changed ordinary XML %q: %+v", text, u)
		}
	}
}
