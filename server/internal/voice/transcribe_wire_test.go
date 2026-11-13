package voice

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// A transcription session's start: the live dialect's at 16 kHz, text-only output, the caption extension when asked
// for (every session asks unless the provider is known not to grant it), no voice, no tools, and no language setting
// anywhere.
func TestTranscribeWireStartShape(t *testing.T) {
	for _, tt := range []struct {
		partial, textOnly bool
		want              string
	}{
		{true, true, `{"session":{"audio":{"format":{"rate":16000,"type":"audio/pcm"}},"delegation":{"type":"client"},` +
			`"extensions":{"input_transcript_partial":"v1"},"instructions":"listen","model":"m1","output_modalities":["text"]},"type":"session.start"}`},
		{false, true, `{"session":{"audio":{"format":{"rate":16000,"type":"audio/pcm"}},"delegation":{"type":"client"},` +
			`"instructions":"listen","model":"m1","output_modalities":["text"]},"type":"session.start"}`},
		{false, false, `{"session":{"audio":{"format":{"rate":16000,"type":"audio/pcm"}},"delegation":{"type":"client"},` +
			`"instructions":"listen","model":"m1"},"type":"session.start"}`},
	} {
		b, _ := json.Marshal(transcribeStart("m1", "listen", tt.partial, tt.textOnly))
		if string(b) != tt.want {
			t.Errorf("partial %v, text only %v:\n%s\nwant\n%s", tt.partial, tt.textOnly, b, tt.want)
		}
	}
}

// The frames the measured service sends (docs/assistant/voice.md "Transcription": shapes of the #3862 measurement,
// the service's prefix of the caption frames replaced), read as the adapter needs them.
func TestTranscribeWireDecodesMeasuredFrames(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		want      []tevent
	}{
		{"started, captions granted", `{"type":"session.started","session":{"id":"s1","extensions":{"input_transcript_partial":"v1"},` +
			`"output_modalities":["text"],"expires_at":99999999999}}`,
			[]tevent{{kind: tevStarted, captions: true, expires: time.Unix(99999999999, 0), modalities: []string{"text"}}}},
		{"started, ends in milliseconds", `{"type":"session.started","session":{"extensions":{},"expires_at":1794500000500}}`,
			[]tevent{{kind: tevStarted, expires: time.Unix(1794500000, 5e8)}}},
		{"started, not granted", `{"type":"session.started","session":{"extensions":{},"output_modalities":["text"]}}`,
			[]tevent{{kind: tevStarted, modalities: []string{"text"}}}},
		{"started, an old service", `{"type":"session.started","session":{"audio":{"format":{"rate":16000}}}}`,
			[]tevent{{kind: tevStarted}}},
		// start_ms is not read: the measured service counts it from the session's start and never changes it in an item.
		{"interim caption", `{"type":"svc.input_transcript.partial","item_id":"029f54e8","start_ms":9268,"text":"先说 ios 这边离线编辑基本做完了","is_final":false}`,
			[]tevent{{kind: tevCaption, item: "029f54e8", text: "先说 ios 这边离线编辑基本做完了"}}},
		{"caption withdrawn", `{"type":"svc.input_transcript.partial","item_id":"9b20","start_ms":5000,"text":"","is_final":true}`,
			[]tevent{{kind: tevCaption, item: "9b20", final: true}}},
		{"the standard caption is not used", `{"type":"session.input_transcript.delta","delta":"iOS","start_ms":4714,"end_ms":37921}`, nil},
		{"reply created", `{"type":"response.event","delegation_id":"d1","event":{"type":"response.created","response":{"id":"r1"}}}`,
			[]tevent{{kind: tevReply}}},
		{"reply text", `{"type":"response.event","delegation_id":"d1","event":{"type":"response.output_text.delta","delta":"我们先把 export"}}`,
			[]tevent{{kind: tevText, text: "我们先把 export"}}},
		{"reply text done", `{"type":"response.event","delegation_id":"d1","event":{"type":"response.output_text.done","text":"我们先把 export cap 保持在 4 MB。"}}`,
			[]tevent{{kind: tevTextDone, text: "我们先把 export cap 保持在 4 MB。"}}},
		{"reply text done without text", `{"type":"response.event","event":{"type":"response.output_text.done"}}`,
			[]tevent{{kind: tevTextDone}}},
		{"reply completed", `{"type":"response.event","delegation_id":"d1","event":{"type":"response.completed"}}`,
			[]tevent{{kind: tevReplyEnd}}},
		{"reply failed", `{"type":"response.event","delegation_id":"d1","event":{"type":"response.failed"}}`,
			[]tevent{{kind: tevReplyEnd}}},
		{"usage", `{"type":"session.usage.updated","usage":{"seconds":12.6},"context_window":{"usage_ratio":0.03}}`,
			[]tevent{{kind: tevUsage, seconds: 12.6, hasSeconds: true}}},
		{"usage without seconds", `{"type":"session.usage.updated","context_window":{"usage_ratio":0.03}}`, nil},
		{"closed", `{"type":"session.closed","reason":"close_requested","usage":{"seconds":30.2}}`,
			[]tevent{{kind: tevClosed, reason: "close_requested", seconds: 30.2, hasSeconds: true}}},
		{"closed, lost", `{"type":"session.closed","reason":"connection_lost"}`, []tevent{{kind: tevClosed, reason: "connection_lost"}}},
		{"error", `{"type":"error","error":{"message":"unknown extension","code":"unsupported_extension","param":"session.extensions"}}`,
			[]tevent{{kind: tevError, code: "unsupported_extension", param: "session.extensions", text: "unknown extension"}}},
		{"output audio is nothing", `{"type":"session.output_audio.delta","delta":"AAAA"}`, nil},
		{"not JSON", `{"type":`, nil},
	} {
		if got := decodeTranscribe([]byte(tt.raw)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// Which field of a start the service refused: the caption extension when the start asked for it, the text-only
// output, or neither.
func TestRefusedField(t *testing.T) {
	for _, tt := range []struct {
		r       sessionRefused
		partial bool
		want    string
	}{
		{sessionRefused{code: "unsupported_extension", param: "session.extensions"}, true, "captions"},
		{sessionRefused{code: "unknown_parameter", text: "unknown extension input_transcript_partial"}, true, "captions"},
		{sessionRefused{code: "unsupported_extension"}, true, "captions"},
		{sessionRefused{code: "unknown_parameter", param: "session.output_modalities"}, true, "text_only"},
		{sessionRefused{code: "unknown_parameter", param: "session.output_modalities"}, false, "text_only"},
		{sessionRefused{code: "unknown_parameter"}, false, "text_only"},
		{sessionRefused{code: "unknown_parameter"}, true, "captions"},
		{sessionRefused{code: "invalid_api_key", text: "bad key"}, true, ""},
		{sessionRefused{code: "rate_limited"}, false, ""},
	} {
		if got := refusedField(&tt.r, tt.partial); got != tt.want {
			t.Errorf("%+v partial %v: %q, want %q", tt.r, tt.partial, got, tt.want)
		}
	}
}
