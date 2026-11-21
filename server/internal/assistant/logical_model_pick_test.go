package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestOrdinaryModelPickCannotCrossRuntimeConnectionsOfTheSameKind(t *testing.T) {
	for _, tc := range []struct {
		name, source, instance, current, target, identity string
		want                                              int
	}{
		{"native to runtime", "", "", "claude", "hosted-two", "claude|runtime:runtime-two", http.StatusConflict},
		{"runtime to native", proto.SourceRuntime, "runtime-one", "hosted-one", "claude", "claude", http.StatusConflict},
		{"runtime to another runtime", proto.SourceRuntime, "runtime-one", "hosted-one", "hosted-two", "claude|runtime:runtime-two", http.StatusConflict},
		{"native to unidentified runtime", "", "", "claude", "hosted-unknown", "unknown|runtime:runtime-two", http.StatusConflict},
		{"runtime to unidentified runtime", proto.SourceRuntime, "runtime-one", "hosted-one", "hosted-unknown", "unknown|runtime:runtime-two", http.StatusConflict},
		{"same runtime alias", proto.SourceRuntime, "runtime-one", "hosted-one", "hosted-alias", "claude|runtime:runtime-one", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := pickFixture(t)
			f.mu.Lock()
			f.launcherList = append(f.launcherList,
				proto.Launcher{Name: "hosted-one", Source: "runtime-one", Agent: "claude", Assistant: true},
				proto.Launcher{Name: "hosted-alias", Source: "runtime-one", Agent: "claude", Assistant: true},
				proto.Launcher{Name: "hosted-two", Source: "runtime-two", Agent: "claude", Assistant: true},
				proto.Launcher{Name: "hosted-unknown", Source: "runtime-two", Agent: "unknown", Assistant: true})
			f.mu.Unlock()
			f.setSessions("server", proto.Session{SessionID: "main", Assistant: true, Agent: "claude", Launcher: tc.current, SourceKind: tc.source, SourceInstanceID: tc.instance})
			checks := 0
			f.answerNode("assistant.segment.check", func(string, json.RawMessage) (any, error) {
				checks++
				return segmentTarget{Registered: true, Prepare: true, Engine: tc.identity}, nil
			})
			r := putPick(t, s, "main", `{"launcher":"`+tc.target+`"}`)
			if r.Code != tc.want {
				t.Fatalf("pick: %d %s", r.Code, r.Body.String())
			}
			_, kept := s.modelPickOf(t.Context(), "server", "main")
			if kept != (tc.want == http.StatusOK) {
				t.Fatalf("rejected pick changed persisted choice: %s", r.Body.String())
			}
			if tc.want != http.StatusOK && (!strings.Contains(r.Body.String(), `"delivered":"none"`) || strings.Contains(r.Body.String(), "launcher_other_kind")) {
				t.Fatalf("wrong explicit-switch refusal: %s", r.Body.String())
			}
			if checks != 1 {
				t.Fatalf("connection identity was not attested once: %d", checks)
			}
			_, err := send(t, s, "main", map[string]any{"launcher": tc.target})
			if tc.want == http.StatusOK {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var refusal *rpc.Error
				if !errors.As(err, &refusal) || !strings.Contains(string(refusal.Data), `"delivered":"none"`) {
					t.Fatalf("send lost non-delivery receipt: %v", err)
				}
			}
			if checks != 2 {
				t.Fatalf("send did not attest connection: %d", checks)
			}

		})
	}
}

func TestUnknownRuntimeSourceCannotChangeLauncher(t *testing.T) {
	s, f := pickFixture(t)
	f.setSessions("server", proto.Session{SessionID: "main", Assistant: true, Agent: "unknown", Launcher: "hosted-one", SourceKind: proto.SourceRuntime, SourceInstanceID: "runtime-one"})
	_, err := send(t, s, "main", map[string]any{"launcher": "claude"})
	var refusal *rpc.Error
	if !errors.As(err, &refusal) || !strings.Contains(string(refusal.Data), `"delivered":"none"`) {
		t.Fatalf("unidentified runtime source changed execution: %v", err)
	}
}
