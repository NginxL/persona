package closeout

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestParse(t *testing.T) {
	reply := "Fixed the retry.\n\n<closeout status=\"done\" merged=\"no\" deployed=\"n/a\">\n" +
		"did: The retry now backs off instead of looping.\n- did：Added a test for the cap.\n" +
		"artifact: pr https://example.com/acme/app/pull/12\nartifact: commit abc1234\nartifact: tweet nope\n" +
		"verified: go test ./retry, 14 passed\nmissing: none\n</closeout>\n"
	c, arts, ok := Parse(reply)
	if !ok {
		t.Fatal("no closeout read")
	}
	want := proto.Closeout{Status: proto.CloseoutDone, Merged: proto.CloseoutNo,
		Did:      []string{"The retry now backs off instead of looping.", "Added a test for the cap."},
		Verified: "go test ./retry, 14 passed"}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("closeout = %+v, want %+v", c, want)
	}
	wantArts := []proto.WorkArtifact{{Kind: "pr", Ref: "https://example.com/acme/app/pull/12"}, {Kind: "commit", Ref: "abc1234"}}
	if !reflect.DeepEqual(arts, wantArts) {
		t.Fatalf("artifacts = %+v, want %+v", arts, wantArts)
	}
}

func TestParseBlockedAndLast(t *testing.T) {
	reply := "<closeout status=\"done\"></closeout> earlier draft\n" +
		"<closeout status=\"blocked\" merged=\"no\" deployed=\"no\">\nwaiting: the release review\nmissing: approval to merge\n</closeout>"
	c, _, ok := Parse(reply)
	if !ok || c.Status != proto.CloseoutBlocked || c.Waiting != "the release review" || c.Missing != "approval to merge" || c.Deployed != proto.CloseoutNo {
		t.Fatalf("closeout = %+v ok=%v: want the last block, blocked", c, ok)
	}
}

// A file a task made for the user is an artifact like any other: the manager copies it to the conversation's computer
// (#10511).
func TestParseFileArtifact(t *testing.T) {
	c, arts, ok := Parse("<closeout status=\"done\">\nartifact: file /home/me/manual.md\n</closeout>")
	if !ok || c.Status != proto.CloseoutDone || len(arts) != 1 || arts[0].Kind != "file" || arts[0].Ref != "/home/me/manual.md" {
		t.Fatalf("closeout = %+v artifacts = %+v ok=%v", c, arts, ok)
	}
}

func TestParseRefuses(t *testing.T) {
	for name, reply := range map[string]string{
		"none":          "All done, merged and deployed.",
		"no status":     "<closeout merged=\"yes\">\ndid: x\n</closeout>",
		"made-up state": "<closeout status=\"almost\">\ndid: x\n</closeout>",
		"unclosed":      "<closeout status=\"done\">\ndid: x",
	} {
		if Has(reply) {
			t.Errorf("%s: read as a closeout", name)
		}
	}
}

func TestParseBounds(t *testing.T) {
	long := strings.Repeat("字", 500)
	reply := "<closeout status=\"not-done\">\n" + strings.Repeat("did: "+long+"\n", 5) + strings.Repeat("artifact: url https://example.com/x\n", 9) + "</closeout>"
	c, arts, ok := Parse(reply)
	if !ok || c.Status != proto.CloseoutNotDone {
		t.Fatalf("closeout = %+v ok=%v", c, ok)
	}
	if len(c.Did) != 3 || len([]rune(c.Did[0])) != 200 || len(arts) != 5 {
		t.Fatalf("did %d lines of %d runes, %d artifacts: want 3 of 200, 5", len(c.Did), len([]rune(c.Did[0])), len(arts))
	}
}

func TestStrip(t *testing.T) {
	if got := Strip("Fixed.\n\n<closeout status=\"done\">\ndid: x\n</closeout>\n"); got != "Fixed." {
		t.Fatalf("Strip = %q", got)
	}
}

// A closeout that says done and names nothing missing or waited for says the turn waits on nobody (#4110); any other
// closeout, or none, says nothing of it.
func TestWaitsOnNobody(t *testing.T) {
	for _, c := range []struct {
		name, reply string
		want        bool
	}{
		{"done", "Which part matters most to you?\n<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\nverified: untested\n</closeout>", true},
		{"done, waiting no", "<closeout status=\"done\">\nwaiting: no\nmissing: none\n</closeout>", true},
		{"done, one line", "<closeout status=\"done\"></closeout>", true},
		{"done with something missing", "<closeout status=\"done\">\nmissing: your pick of A or B\n</closeout>", false},
		{"done, waiting for someone", "<closeout status=\"done\">\nwaiting: the review\n</closeout>", false},
		{"not done", "<closeout status=\"not_done\">\ndid: x\n</closeout>", false},
		{"blocked", "<closeout status=\"blocked\">\nwaiting: your answer\n</closeout>", false},
		{"no status", "<closeout merged=\"yes\"></closeout>", false},
		{"none", "Which part matters most to you?", false},
		{"a later block wins", "<closeout status=\"done\"></closeout>\n<closeout status=\"blocked\">\nwaiting: you\n</closeout>", false},
	} {
		if got := WaitsOnNobody(c.reply); got != c.want {
			t.Errorf("%s: WaitsOnNobody = %v, want %v", c.name, got, c.want)
		}
	}
}

// A closeout that says the turn is done and needs nobody is the agent's word that its closing question is conversation
// (#4110): the reply asks nothing. Any other closeout, or none, leaves the words to say it as before.
func TestAskOf(t *testing.T) {
	const q = "Which of these matters most to you?"
	done := "<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\"></closeout>"
	for _, c := range []struct{ name, text, want string }{
		{"no closeout", "Here are three options.\n\n" + q, q},
		{"done after the question", "Here are three options.\n\n" + q + "\n\n" + done, ""},
		{"done before the question", "Here are three options.\n\n" + done + "\n\n" + q, ""},
		{"done, waiting no", "Here are three options.\n<closeout status=\"done\">\nwaiting: no\n</closeout>\n" + q, ""},
		{"done, a Chinese question", "三个方向都列在上面了。\n" + done + "\n你最在意哪一点？", ""},
		{"done but something missing", "Here are three options.\n<closeout status=\"done\">\nmissing: your pick\n</closeout>\n" + q, q},
		{"not done", "Here are three options.\n<closeout status=\"not_done\"></closeout>\n" + q, q},
		{"blocked", "Here are three options.\n<closeout status=\"blocked\">\nwaiting: your answer\n</closeout>\n" + q, q},
		{"no status", "Here are three options.\n<closeout merged=\"n/a\"></closeout>\n" + q, q},
	} {
		if got := AskOf(c.text); got != c.want {
			t.Errorf("%s: AskOf = %q, want %q", c.name, got, c.want)
		}
		// The words alone ask: it is the closeout that says otherwise, not the question falling out of the last sentences.
		if c.want == "" && transcript.AskOf(c.text) == "" {
			t.Errorf("%s: the words alone ask nothing, the case shows nothing", c.name)
		}
	}
}

// A reply with no closeout, most of them, costs AskOf nothing on top of its words (transcript.AskOf): no closeout is
// parsed, so a reply that asks nothing allocates nothing (transcript's TestAskOfAllocations), and one that asks only
// what reading its question takes.
func TestAskOfAllocations(t *testing.T) {
	report := strings.Repeat("The tests pass and the branch is pushed. ", 6)
	for _, text := range []string{report, report + "Should I merge it now?"} {
		words := testing.AllocsPerRun(100, func() { transcript.AskOf(text) })
		if n := testing.AllocsPerRun(100, func() { AskOf(text) }); n > words {
			t.Errorf("AskOf allocates %v times for %.40q…, its words alone %v", n, text, words)
		}
	}
}
