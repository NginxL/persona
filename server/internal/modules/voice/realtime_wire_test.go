package voice

import (
	"encoding/json"
	"io"
	"log"
	"testing"
	"testing/synctest"
)

// The realtime dialect's session.update is the GA shape a Realtime endpoint takes (#4011): type realtime, the formats
// under audio.input and audio.output, the voice only in audio.output. session.audio.format and session.voice are
// refused as unknown parameters, which leaves the session without instructions, tools or transcription.
func TestRealtimeWireStartIsTheGAShape(t *testing.T) {
	tools := []toolDecl{{Type: "function", Name: "delegate_task", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}
	raw, _ := json.Marshal(realtimeWire{}.start(session{Model: "gpt-realtime-1.5", Voice: "alloy", Instructions: "be brief", InputRate: 24000, Tools: tools}))
	var f struct {
		Type    string         `json:"type"`
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	s := f.Session
	if f.Type != "session.update" || s["type"] != "realtime" || s["instructions"] != "be brief" {
		t.Fatalf("start: %s", raw)
	}
	if _, ok := s["voice"]; ok {
		t.Fatalf("start sends session.voice, which the service refuses: %s", raw)
	}
	audio := s["audio"].(map[string]any)
	if _, ok := audio["format"]; ok {
		t.Fatalf("start sends session.audio.format, which the service refuses: %s", raw)
	}
	in, out := audio["input"].(map[string]any), audio["output"].(map[string]any)
	if in["format"].(map[string]any)["rate"] != float64(24000) || in["transcription"].(map[string]any)["model"] == nil {
		t.Fatalf("start: audio.input %v", in)
	}
	if out["voice"] != "alloy" || out["format"].(map[string]any)["type"] != "audio/pcm" {
		t.Fatalf("start: audio.output %v", out)
	}
	if ts, _ := s["tools"].([]any); len(ts) != 1 {
		t.Fatalf("start: tools %v", s["tools"])
	}

	raw, _ = json.Marshal(realtimeWire{}.start(session{InputRate: 24000}))
	var bare struct {
		Session struct {
			Audio struct {
				Output map[string]any `json:"output"`
			} `json:"audio"`
		} `json:"session"`
	}
	_ = json.Unmarshal(raw, &bare)
	if _, ok := bare.Session.Audio.Output["voice"]; ok {
		t.Fatalf("start without a voice names one: %s", raw)
	}
}

// What the realtime dialect makes of the frames a Realtime endpoint sends (measured, #4011): a response's id is in
// response.id, a function call comes once (its arguments' done; response.output_item.done repeats it), and the
// service's VAD hearing the user cuts the model off.
func TestRealtimeWireDecodesMeasuredFrames(t *testing.T) {
	w := realtimeWire{}
	for _, tc := range []struct {
		frame string
		want  []event
	}{
		{`{"type":"response.created","event_id":"e1","response":{"object":"realtime.response","id":"resp_1","status":"in_progress"}}`,
			[]event{{kind: evReply, id: "resp_1"}}},
		{`{"type":"response.done","event_id":"e2","response":{"object":"realtime.response","id":"resp_1","status":"cancelled","status_details":{"type":"cancelled","reason":"turn_detected"}}}`,
			[]event{{kind: evTurnEnd, id: "resp_1"}}},
		{`{"type":"response.output_item.done","response_id":"resp_2","output_index":0,"item":{"id":"item_1","type":"function_call","status":"completed","name":"delegate_task","call_id":"call_1","arguments":"{\"task\":\"x\"}"}}`,
			nil},
		{`{"type":"response.function_call_arguments.done","response_id":"resp_2","item_id":"item_1","output_index":0,"call_id":"call_1","name":"delegate_task","arguments":"{\"task\":\"x\"}"}`,
			[]event{{kind: evDelegation, id: "call_1"}, {kind: evToolCall, id: "call_1", callID: "call_1", name: "delegate_task", args: `{"task":"x"}`}}},
		{`{"type":"input_audio_buffer.speech_started","event_id":"e3","audio_start_ms":4756,"item_id":"item_2"}`,
			[]event{{kind: evInterrupted}}},
		{`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_2","content_index":0,"transcript":"等一下"}`,
			[]event{{kind: evUser, text: "等一下", replace: true, final: true}}},
		{`{"type":"error","event_id":"e4","error":{"type":"invalid_request_error","code":"conversation_already_has_active_response","message":"busy","param":null}}`,
			[]event{{kind: evError, text: "busy", code: "conversation_already_has_active_response"}}},
	} {
		got := w.decode([]byte(tc.frame))
		if len(got) != len(tc.want) {
			t.Errorf("decode %s = %+v, want %+v", tc.frame, got, tc.want)
			continue
		}
		for i := range got {
			if got[i].kind != tc.want[i].kind || got[i].id != tc.want[i].id || got[i].callID != tc.want[i].callID ||
				got[i].name != tc.want[i].name || got[i].args != tc.want[i].args || got[i].text != tc.want[i].text ||
				got[i].code != tc.want[i].code || got[i].final != tc.want[i].final {
				t.Errorf("decode %s [%d] = %+v, want %+v", tc.frame, i, got[i], tc.want[i])
			}
		}
	}
}

// A response.create sent while a reply is under way is refused by the service (conversation_already_has_active_response):
// it is held back and sent once that reply is over, once however many asked for it meanwhile.
//
// It runs in a synctest bubble: the held response.create goes out on a goroutine of its own. synctest.Wait returns
// once every goroutine the turn's end started has finished or waits on a timer, so the count after it is final rather
// than whatever arrived within a fixed window, and a send still waiting when the test ends fails it (the bubble's time
// stops then). recLink is in memory, so nothing blocks outside the bubble.
func TestResponseCreateWaitsForTheReplyUnderWay(t *testing.T) {
	synctest.Test(t, responseCreateWaitsForTheReplyUnderWay)
}

func responseCreateWaitsForTheReplyUnderWay(t *testing.T) {
	up := &recLink{left: -1}
	c := captionConversation(t)
	c.pl, c.w, c.ready, c.up = &Plugin{logger: log.New(io.Discard, "", 0)}, realtimeWire{}, true, up
	types := func() []string {
		up.mu.Lock()
		defer up.mu.Unlock()
		var out []string
		for _, f := range up.frames {
			out = append(out, f["type"].(string))
		}
		return out
	}

	c.mu.Lock()
	c.handle(0, event{kind: evReply, id: "resp_1"})
	c.mu.Unlock()
	if !c.sendUp(-1, realtimeWire{}.say("one", "")...) || !c.sendUp(-1, realtimeWire{}.say("two", "")...) {
		t.Fatal("sendUp failed")
	}
	if got := types(); len(got) != 2 || got[0] != "conversation.item.create" || got[1] != "conversation.item.create" {
		t.Fatalf("while the reply is under way: %v", got)
	}

	c.mu.Lock()
	c.handle(0, event{kind: evTurnEnd, id: "resp_1"})
	c.mu.Unlock()
	synctest.Wait() // the held response.create has gone out, and nothing else is still on its way
	if got := types(); len(got) != 3 || got[2] != "response.create" {
		t.Fatalf("after the reply: %v", got)
	}

	// No reply under way: it goes at once.
	c.sendUp(-1, realtimeWire{}.say("three", "")...)
	if got := types(); len(got) != 5 || got[4] != "response.create" {
		t.Fatalf("with no reply under way: %v", got)
	}
}
