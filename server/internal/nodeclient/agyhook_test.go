package nodeclient

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const agyTestConv = "11111111-1111-4111-8111-111111111111"

func TestParseAgyReport(t *testing.T) {
	in := `{"conversationId":"` + strings.ToUpper(agyTestConv) + `","transcriptPath":"/x/transcript.jsonl","other":1}`
	r, ok := ParseAgyReport("PreInvocation", []byte(in))
	if !ok || r.Conversation != agyTestConv || r.TranscriptPath != "/x/transcript.jsonl" || r.Event != "PreInvocation" {
		t.Fatalf("report = %+v %v", r, ok)
	}
	for name, c := range map[string]struct{ ev, in string }{
		"unknown event": {"PostToolUse", in},
		"not json":      {"Stop", "nope"},
		"no id":         {"Stop", `{}`},
		"bad id":        {"Stop", `{"conversationId":"../../x"}`},
	} {
		if _, ok := ParseAgyReport(c.ev, []byte(c.in)); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAgyHookPrintsEmptyObjectWhateverHappens(t *testing.T) {
	t.Setenv("PERSONA_HOME", t.TempDir()) // no node socket
	for name, env := range map[string]map[string]string{
		"no pane":   {},
		"herdr":     {HerdrPaneEnv: "w1:p1"},
		"no socket": {OrcaPaneEnv: "t:l"},
	} {
		var out bytes.Buffer
		RunAgyHook(context.Background(), []string{"PreInvocation"}, strings.NewReader(`{"conversationId":"`+agyTestConv+`"}`), &out, func(k string) string { return env[k] })
		if out.String() != "{}\n" {
			t.Errorf("%s: printed %q", name, out.String())
		}
	}
	var out bytes.Buffer
	RunAgyHook(context.Background(), nil, strings.NewReader(""), &out, func(string) string { return "" })
	if out.String() != "{}\n" {
		t.Errorf("no event: %q", out.String())
	}
}

func TestHookPaneForAgyEngines(t *testing.T) {
	for _, c := range []struct{ env, val, want string }{
		{HerdrPaneEnv, "w1:p1", "w1:p1"},
		{HerdrCorePaneEnv, "t7", "herdr-core:t7"},
		{OrcaPaneEnv, "tab:leaf", "orca:tab:leaf"},
		{TernPaneEnv, "b3", "tern:b3"},
	} {
		if got := HookPane(func(k string) string {
			if k == c.env {
				return c.val
			}
			return ""
		}); got != c.want {
			t.Errorf("%s: pane %q, want %q", c.env, got, c.want)
		}
	}
}
