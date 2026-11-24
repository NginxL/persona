package closeout

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestParseAndAskOfIgnoreCodeFencesAndInlineSpans(t *testing.T) {
	const question = "Should I apply this format across the remaining prompts?"

	cases := []struct {
		name  string
		reply string
	}{
		{
			name: "fenced xml example",
			reply: "Here is the closeout format:\n\n```xml\n<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\nverified: untested\n</closeout>\n```\n\n" +
				question,
		},
		{
			name: "four-backtick outer fence with inner triple fence",
			reply: "````markdown\n```xml\n<closeout status=\"done\"></closeout>\n```\n<closeout status=\"done\"></closeout>\n````\n\n" +
				question,
		},
		{
			name: "tilde fence",
			reply: "~~~\n<closeout status=\"done\" merged=\"yes\" deployed=\"yes\"></closeout>\n~~~\n\n" +
				question,
		},
		{
			name: "blockquoted fence",
			reply: "> ```xml\n> <closeout status=\"done\"></closeout>\n> ```\n\n" +
				question,
		},
		{
			name:  "single-backtick inline span",
			reply: "End the turn with `<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\"></closeout>` once verified. " + question,
		},
		{
			name:  "multi-backtick inline span",
			reply: "Wrap `` `<closeout status=\"done\"></closeout>` `` in code when quoting it. " + question,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Has(tc.reply) {
				t.Fatalf("Has(%q) = true, want false for code example", tc.reply)
			}
			if WaitsOnNobody(tc.reply) {
				t.Fatalf("WaitsOnNobody(%q) = true, want false for code example", tc.reply)
			}
			if got := AskOf(tc.reply); got != question {
				t.Fatalf("AskOf = %q, want %q", got, question)
			}
			if got := Strip(tc.reply); got != tc.reply {
				t.Fatalf("Strip modified code example:\n got %q\nwant %q", got, tc.reply)
			}
		})
	}
}

func TestParseAndStripPreserveCodeExamplesAlongsideRealCloseout(t *testing.T) {
	visible := "Mention `<closeout` in prose and keep the sample:\n\n```xml\n<closeout status=\"done\">\ndid: sample only\n</closeout>\n```\n\nAnd inline `<closeout status=\"done\"></closeout>` stays."
	real := "<closeout status=\"blocked\" merged=\"no\" deployed=\"n/a\">\n" +
		"did: Ignore `<closeout status=\"done\"></closeout>` inside code.\n" +
		"waiting: review approval\n" +
		"</closeout>"
	reply := visible + "\n\n" + real + "\n\n```xml\n<closeout status=\"done\"></closeout>\n```"
	wantStripped := visible + "\n\n\n\n```xml\n<closeout status=\"done\"></closeout>\n```"

	c, _, ok := Parse(reply)
	if !ok {
		t.Fatal("Parse did not find real closeout")
	}
	if c.Status != proto.CloseoutBlocked || c.Waiting != "review approval" {
		t.Fatalf("Parse read wrong closeout: %+v", c)
	}
	if len(c.Did) != 1 || c.Did[0] != "Ignore `<closeout status=\"done\"></closeout>` inside code." {
		t.Fatalf("Parse truncated did line at inline closeout tag: %+v", c.Did)
	}
	if got := Strip(reply); got != wantStripped {
		t.Fatalf("Strip:\n got %q\nwant %q", got, wantStripped)
	}
}
