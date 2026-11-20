package devices

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentPromptLanguageAtNodeBoundary(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{info: &proto.NodeInfo{Version: "test", Protocol: nodeproto.Version, Capabilities: nodeproto.Announced}}
	connectedMachine(t, d, "box", n)
	locale := "English"
	d.opts.PromptLanguage = func() string { return locale }
	raw := json.RawMessage(`{"session_id":"s","text":"Original 原文","PROMPT_LANGUAGE":"forged","prompt_Language":"forged","owner_input":true}`)
	for _, language := range []string{"English", "日本語", ""} {
		locale = language
		for _, method := range []string{"session.send", "codex.answer"} {
			for _, direct := range []bool{false, true} {
				var err error
				if direct {
					_, err = d.CallNode(context.Background(), "box", method, raw)
				} else {
					_, err = d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: method, Params: raw})
				}
				if err != nil {
					t.Fatal(err)
				}
				calls := n.seen(method + " ")
				last := calls[len(calls)-1]
				var fields map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(last, method+" ")), &fields); err != nil {
					t.Fatal(err)
				}
				if fields["text"] != "Original 原文" || fields["owner_input"] != nil {
					t.Fatalf("locale altered source or authority: %s", last)
				}
				count := 0
				for key := range fields {
					if strings.EqualFold(key, "prompt_language") {
						count++
					}
				}
				if language == "" {
					if count != 0 {
						t.Fatal(last)
					}
				} else if count != 1 || fields["prompt_language"] != language {
					t.Fatal(last)
				}
			}
		}
	}
	// A nil callback behaves like an old manager: caller fields cannot choose locale.
	d.opts.PromptLanguage = nil
	out, err := d.promptLanguageParams("session.send", raw)
	if err != nil || strings.Contains(strings.ToLower(string(out)), "prompt_language") {
		t.Fatalf("legacy callback: %s %v", out, err)
	}
	for _, invalid := range []string{`[]`, `"not an object"`, `{`} {
		if _, err := d.promptLanguageParams("session.send", json.RawMessage(invalid)); err == nil {
			t.Fatal("malformed object accepted")
		}
	}
}
