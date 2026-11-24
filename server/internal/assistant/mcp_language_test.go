package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Capture the JSON returned to the node, including deferred-loading metadata.
func TestGoldenMCPDiscoveryLanguages(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTest(t)
	ft.f.hello.Assistant.MachineID = "mac"
	ctx := i18n.With(context.Background(), i18n.En) // request locale must not replace the owner's
	var originalNames []string
	for _, lang := range []string{"zh-Hans", "en", "ja", "zh-Hans"} {
		ft.f.mu.Lock()
		ft.f.hello.Locale = lang
		ft.f.mu.Unlock()
		for _, assistant := range []bool{true, false} {
			params, _ := json.Marshal(map[string]any{"assistant": assistant, "session_id": "ordinary"})
			out, handled, err := ft.s.NodeRequest(ctx, "mac", "mcp.tools", params)
			if err != nil || !handled {
				t.Fatal(err, handled)
			}
			raw, _ := json.Marshal(out)
			var received struct {
				Tools []proto.Tool `json:"tools"`
			}
			if err := json.Unmarshal(raw, &received); err != nil {
				t.Fatal(err)
			}
			if len(received.Tools) == 0 {
				t.Fatal("discovery returned no tools")
			}
			if assistant {
				var names []string
				var found bool
				for _, tool := range received.Tools {
					names = append(names, tool.Name)
					if tool.Name == "task_plan" {
						found = true
						if tool.Description != prompts.Tool("task_plan", lang).Description {
							t.Fatalf("wrong owner language %s", lang)
						}
						if lang != "zh-Hans" && strings.Contains(string(tool.InputSchema), "Chinese") {
							t.Fatal("Chinese-only schema requirement remains")
						}
					}
				}
				if !found {
					t.Fatal("assistant list lacks task_plan")
				}
				if originalNames == nil {
					originalNames = names
				} else if strings.Join(names, ",") != strings.Join(originalNames, ",") {
					t.Fatal("locale changed tool membership/order")
				}
			}
			branch := "agent"
			if assistant {
				branch = "assistant"
			}
			b, err := json.MarshalIndent(received.Tools, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden(t, "mcp_language."+branch+"."+lang, string(b))
		}
	}
}

func TestMCPLanguageChangeInvalidatesDiscoveryCursor(t *testing.T) {
	s, f := newQuietService(t)
	before, changed := s.mcpChanges.snapshot()
	f.mu.Lock()
	f.hello.Locale = "ja"
	f.mu.Unlock()
	s.ProfileChanged(context.Background())
	select {
	case <-changed:
	default:
		t.Fatal("tool watch was not invalidated")
	}
	after, _ := s.mcpChanges.snapshot()
	if after == before {
		t.Fatal("language change kept stale revision")
	}
}

func TestGoldenMCPRestrictedLanguages(t *testing.T) {
	for _, lang := range []string{"zh-Hans", "en", "ja"} {
		for _, mode := range []string{"work", "meeting", "leader"} {
			t.Run(lang+"/"+mode, func(t *testing.T) {
				ft := newFleetTest(t)
				ft.f.hello.Locale = lang
				params := map[string]any{"session_id": "ordinary"}
				switch mode {
				case "work":
					ft.d.machines["mac"].trust = "work"
				case "meeting":
					needsRecordingModule(t) // a meeting guest's tools are the recording module's: none without it
					if err := ft.s.guestSetRoom(context.Background(), "mac", "ordinary", "sample-recording"); err != nil {
						t.Fatal(err)
					}
					params["guest"] = true
				case "leader":
					params = map[string]any{"leader_only": true}
				}
				raw, _ := json.Marshal(params)
				out, handled, err := ft.s.NodeRequest(context.Background(), "mac", "mcp.tools", raw)
				if err != nil || !handled {
					t.Fatal(err, handled)
				}
				b, _ := json.Marshal(out)
				var received struct {
					Tools []proto.Tool `json:"tools"`
				}
				if err := json.Unmarshal(b, &received); err != nil {
					t.Fatal(err)
				}
				if len(received.Tools) == 0 {
					t.Fatal("restricted discovery unexpectedly empty")
				}
				b, err = json.MarshalIndent(received.Tools, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				golden(t, "mcp_language."+mode+"."+lang, string(b))
			})
		}
	}
}
