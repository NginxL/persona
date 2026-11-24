package proto

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"
	"time"
)

func TestItemTTFTMSWireRoundTripAndOmission(t *testing.T) {
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)

	// Historical item without TTFT: ttft_ms must be omitted on the wire.
	hist := Item{ID: "h1#0", Seq: 1, TS: now, Role: "assistant", Kind: KindText, Text: "Hello <world> & friends"}
	hb, err := json.Marshal(hist)
	if err != nil {
		t.Fatalf("marshal historical item: %v", err)
	}
	if strings.Contains(string(hb), `"ttft_ms"`) {
		t.Fatalf("historical item without TTFT must omit ttft_ms: %s", hb)
	}
	if d, ok := hist.TTFTDuration(); ok || d != 0 {
		t.Fatalf("expected no TTFT duration, got %v %v", d, ok)
	}

	// Backward compatibility: legacy JSON without ttft_ms decodes cleanly with nil TTFTMS.
	var legacy Item
	if err := json.Unmarshal([]byte(`{"id":"old#0","seq":2,"ts":"2026-11-21T14:00:00Z","role":"assistant","kind":"text","text":"hi"}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy item: %v", err)
	}
	if legacy.TTFTMS != nil {
		t.Fatalf("legacy item must have nil TTFTMS, got %v", *legacy.TTFTMS)
	}

	// Observed positive TTFT round-trips across encoding/json, json/v2, and SetEscapeHTML(false) encoder.
	live := hist
	live.SetTTFTMS(342)
	if d, ok := live.TTFTDuration(); !ok || d != 342*time.Millisecond {
		t.Fatalf("TTFTDuration = %v (%v), want 342ms", d, ok)
	}
	for name, enc := range map[string]func(any) ([]byte, error){
		"encoding/json": json.Marshal,
		"json/v2":       func(v any) ([]byte, error) { return jsonv2.Marshal(v, json.DefaultOptionsV1()) },
		"no-html-escape": func(v any) ([]byte, error) {
			var buf bytes.Buffer
			e := json.NewEncoder(&buf)
			e.SetEscapeHTML(false)
			err := e.Encode(v)
			return buf.Bytes(), err
		},
	} {
		b, err := enc(live)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		if !strings.Contains(string(b), `"ttft_ms":342`) {
			t.Fatalf("%s missing ttft_ms: %s", name, b)
		}
		if name == "no-html-escape" && !strings.Contains(string(b), "Hello <world> & friends") {
			t.Fatalf("%s escaped HTML unexpectedly: %s", name, b)
		}
		var back Item
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("%s unmarshal: %v", name, err)
		}
		if back.TTFTMS == nil || *back.TTFTMS != 342 {
			t.Fatalf("%s round-trip TTFTMS = %v, want 342", name, back.TTFTMS)
		}
	}

	// Observed 0ms TTFT is preserved (distinguished from nil/unobserved).
	zero := hist
	zero.SetTTFTMS(0)
	zb, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero TTFT: %v", err)
	}
	if !strings.Contains(string(zb), `"ttft_ms":0`) {
		t.Fatalf("observed 0ms TTFT must not be dropped: %s", zb)
	}
	var zeroBack Item
	if err := json.Unmarshal(zb, &zeroBack); err != nil {
		t.Fatalf("unmarshal zero TTFT: %v", err)
	}
	if zeroBack.TTFTMS == nil || *zeroBack.TTFTMS != 0 {
		t.Fatalf("zero TTFT round-trip = %v, want 0", zeroBack.TTFTMS)
	}

	// Negative TTFT values are ignored / sanitized to nil.
	neg := hist
	neg.SetTTFTMS(-10)
	if neg.TTFTMS != nil {
		t.Fatalf("negative TTFT should not be set, got %v", *neg.TTFTMS)
	}
	if SanitizeTTFTMS(Int64(-1)) != nil {
		t.Fatal("SanitizeTTFTMS must drop negative values")
	}
	cp := CloneTTFTMS(live.TTFTMS)
	*cp = 999
	if *live.TTFTMS != 342 {
		t.Fatal("CloneTTFTMS must not alias original pointer")
	}
}
