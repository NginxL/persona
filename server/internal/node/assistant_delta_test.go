package node

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The live text of a reply goes out at most every deltaEvery, and the end of a burst no longer waits for the next
// burst: what came after the last send goes out deltaEvery after it (#6651).
func TestAssistantSendsTheRestOfABurst(t *testing.T) {
	n := assistantNode(t)
	all, pw := hubPushes(n)
	t.Cleanup(func() { pw.Close() })
	p := &assistantProc{id: "11111111-2222-3333-4444-555555555555"}
	ev := func(e string) string {
		return `{"type":"stream_event","event":` + e + `,"parent_tool_use_id":null}` + "\n"
	}
	delta := func(s string) string {
		return ev(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + s + `"}}`)
	}
	start := time.Now()
	// One burst: four deltas at once, then nothing (the model's next burst is far away).
	n.readAssistant(p, strings.NewReader(ev(`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`)+
		delta("先说")+delta("结论：")+delta("三台")+delta("机器")))
	got := liveTexts(t, all, 2, time.Second)
	if len(got) != 2 || got[0].text != "先说" || got[1].text != "先说结论：三台机器" {
		t.Fatalf("live texts %v, want the first delta at once and the rest of the burst after it", got)
	}
	if wait := got[1].at.Sub(start); wait < deltaEvery-10*time.Millisecond || wait > deltaEvery+150*time.Millisecond {
		t.Errorf("the rest of the burst went out after %v, want about %v", wait, deltaEvery)
	}
	// Sent already: nothing more until the text changes.
	if more := liveTexts(t, all, 1, 2*deltaEvery); len(more) != 0 {
		t.Errorf("sent again: %v", more)
	}
}

// A burst cut short by a new message: the armed send finds the text cleared and sends nothing, so nothing old follows
// the clear.
func TestAssistantArmedSendNeverFollowsAClear(t *testing.T) {
	n := assistantNode(t)
	all, pw := hubPushes(n)
	t.Cleanup(func() { pw.Close() })
	p := &assistantProc{id: "11111111-2222-3333-4444-555555555556"}
	ev := func(e string) string {
		return `{"type":"stream_event","event":` + e + `,"parent_tool_use_id":null}` + "\n"
	}
	delta := func(s string) string {
		return ev(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + s + `"}}`)
	}
	n.readAssistant(p, strings.NewReader(ev(`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`)+
		delta("Looking")+delta(" into it")+ev(`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`)))
	got := liveTexts(t, all, 3, 3*deltaEvery)
	if len(got) != 2 || got[0].text != "Looking" || got[1].text != "" {
		t.Fatalf("live texts %v, want the first delta, then the clear, and nothing after it", got)
	}
}

type liveText struct {
	text string
	at   time.Time
}

// liveTexts reads the session.delta pushes until it has want of them or wait has passed.
func liveTexts(t *testing.T, all <-chan hubPush, want int, wait time.Duration) []liveText {
	t.Helper()
	var out []liveText
	deadline := time.After(wait)
	for len(out) < want {
		select {
		case m := <-all:
			if m.Event != "session.delta" {
				continue
			}
			var d struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(m.Params, &d)
			out = append(out, liveText{d.Text, time.Now()})
		case <-deadline:
			return out
		}
	}
	return out
}
