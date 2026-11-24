package cachewarm

import (
	"strings"
	"testing"
)

func TestReplayThinkingAndOnlyTwoFieldsChange(t *testing.T) {
	for _, tc := range []struct {
		name, thinking string
		want           int
	}{{"disabled", `{"type":"disabled"}`, 1}, {"adaptive", `{"type":"adaptive"}`, 1}, {"manual", `{"type":"enabled","budget_tokens":1024}`, 1025}, {"large-budget", `{"type":"enabled","budget_tokens":32768}`, 32769}} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(warmBody, `{"type":"disabled"}`, tc.thinking, 1)
			out, limit, err := Replay([]byte(body))
			if err != nil || limit != tc.want {
				t.Fatalf("%d %v", limit, err)
			}
			if !strings.Contains(string(out), tc.thinking) || !strings.Contains(string(out), `"messages" : [{"role":"user", "content":"private history"}]`) {
				t.Fatal("cached bytes changed")
			}
		})
	}
}
func TestReplayRejectsUnsupportedRequests(t *testing.T) {
	for _, body := range []string{`{}`, strings.ReplaceAll(warmBody, `"1h"`, `"5m"`), strings.Replace(warmBody, `"type":"disabled"`, `"type":"unknown"`, 1), strings.Replace(warmBody, `"type":"disabled"`, `"type":"enabled","budget_tokens":0`, 1), warmBody + `{}`, strings.Replace(warmBody, `"role":"user",`, `"role":"user","cache_control":{"type":"ephemeral"},`, 1), strings.Replace(warmBody, `"max_tokens" : 1024`, `"max_tokens":12,"max_tokens":13`, 1)} {
		if _, _, err := Replay([]byte(body)); err == nil {
			t.Fatal("accepted unsupported replay")
		}
	}
}
