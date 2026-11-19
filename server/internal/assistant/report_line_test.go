package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

// reportingTask is a supervised task of conversation conv1 whose agent is box/s1abcdef.
func reportingTask(t *testing.T, s *Service) Todo {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Migrate the database", "x", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"migration done"}, 0)
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1abcdef", "Migrate the database", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	return todo
}

func reportAs(t *testing.T, s *Service, args string) string {
	t.Helper()
	res := s.agentTool(context.Background(), "box", "s1abcdef", "report_to_supervisor", json.RawMessage(args))
	if res.IsError {
		t.Fatalf("report %s: %+v", args, res)
	}
	return res.Content[0].Text
}

func progressOf(t *testing.T, s *Service, id string) TaskProgress {
	t.Helper()
	v := todoNow(s, id).Supervision
	if v == nil || v.Progress == nil {
		t.Fatalf("no progress: %+v", v)
	}
	return *v.Progress
}

// A report gets a line in the user's language from the small model, written after the tool answered; the model reads
// the whole report, not the progress's one clipped line, and the report's text stays the agent's own words. A long or
// multi-line report keeps its whole text as the detail; a short one-line report has none.
func TestReportLineIsWritten(t *testing.T) {
	s, f := newQuietService(t)
	f.reportLine = "「迁移脚本跑完，在补测试。」\n多余的一行"
	todo := reportingTask(t, s)
	long := "Ran the **migration** script on staging:\n\n- 41 tables moved\n- `orders` needs a backfill\n\n" +
		strings.Repeat("Details of the run. ", 12) + "TAIL-MARKER"
	raw, _ := json.Marshal(map[string]any{"kind": "progress", "text": long, "step": 2, "steps": 4})
	if res := reportAs(t, s, string(raw)); !strings.Contains(res, "Noted for your supervisor") {
		t.Fatalf("answer: %s", res)
	}
	s.supervise.lines.Wait()
	p := progressOf(t, s, todo.ID)
	if p.Line != "迁移脚本跑完，在补测试" {
		t.Fatalf("line %q", p.Line)
	}
	if p.Text != clip(oneLine(long), 200) || p.Detail != long || p.Step != 2 || p.Steps != 4 {
		t.Fatalf("progress: %+v", p)
	}
	f.mu.Lock()
	asked := append([]modelCall(nil), f.reportLineAsked...)
	f.mu.Unlock()
	if len(asked) != 1 || !strings.Contains(asked[0].User, "TAIL-MARKER") || !strings.Contains(asked[0].User, "- 41 tables moved\n") ||
		!strings.Contains(asked[0].User, "任务：Migrate the database") || !strings.Contains(asked[0].User, "汇报类型：progress") {
		t.Fatalf("asked: %+v", asked)
	}
	b, _ := json.Marshal(formatTodo(todoNow(s, todo.ID)))
	if !strings.Contains(string(b), `"line":"迁移脚本跑完，在补测试"`) || !strings.Contains(string(b), `"detail":"Ran the **migration**`) {
		t.Fatalf("the apps do not get it: %s", b)
	}
	if strings.Contains(feedKinds(s, "conv1"), "report") {
		t.Fatalf("a progress report is in the feed: %s", feedKinds(s, "conv1"))
	}

	// A short one-line report: no detail (it is the text).
	f.mu.Lock()
	f.reportLine = "Waiting on the backfill"
	f.mu.Unlock()
	reportAs(t, s, `{"kind":"blocked","text":"Blocked: the backfill needs a maintenance window."}`)
	s.supervise.lines.Wait()
	if p := progressOf(t, s, todo.ID); p.Detail != "" || p.Text != "Blocked: the backfill needs a maintenance window." || p.Line != "Waiting on the backfill" {
		t.Fatalf("short report: %+v", p)
	}
}

// The line is in the user's language: Chinese without saying so, another language by name, with its own length.
func TestReportLineLanguageFollowsLocale(t *testing.T) {
	for _, tc := range []struct {
		locale, has, hasNot string
	}{
		{"zh-Hans", "不超过 30 个字，只写一句", "个字符"},
		{"en", "Write in English, at most 60 characters", "不超过 30"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			s, f := newQuietService(t)
			f.mu.Lock()
			f.hello.Locale = tc.locale
			f.reportLine = "x"
			f.mu.Unlock()
			reportingTask(t, s)
			reportAs(t, s, `{"kind":"progress","text":"在跑迁移"}`)
			s.supervise.lines.Wait()
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.reportLineAsked) != 1 {
				t.Fatalf("%d calls", len(f.reportLineAsked))
			}
			if sys := f.reportLineAsked[0].System; !strings.Contains(sys, tc.has) || strings.Contains(sys, tc.hasNot) {
				t.Fatalf("system for %s:\n%s", tc.locale, sys)
			}
		})
	}
}

// With no model, or a model that fails or says nothing, the report goes as it always did and has no line.
func TestReportLineWithoutModel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeHub)
	}{
		{"none configured", func(f *fakeHub) { f.hello.Models.AI = ai.Config{} }},
		{"fails", func(f *fakeHub) { f.reportLine = "" }},
		{"says nothing", func(f *fakeHub) { f.reportLine = " \n「」\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			tc.setup(f)
			todo := reportingTask(t, s)
			if res := reportAs(t, s, `{"kind":"progress","text":"Running the migration","step":1,"steps":3}`); !strings.Contains(res, "Noted for your supervisor") {
				t.Fatalf("answer: %s", res)
			}
			if res := reportAs(t, s, `{"kind":"question","text":"Canary first, or all at once?"}`); !strings.Contains(res, "Sent to your supervisor") {
				t.Fatalf("answer: %s", res)
			}
			s.supervise.lines.Wait()
			if p := progressOf(t, s, todo.ID); p.Line != "" || p.Kind != "question" || p.Text != "Canary first, or all at once?" {
				t.Fatalf("progress: %+v", p)
			}
			waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
			if n := f.notices()[0]; !strings.Contains(n, "Canary first, or all at once?") {
				t.Fatalf("message: %s", n)
			}
			if !strings.Contains(feedKinds(s, "conv1"), "report") {
				t.Fatalf("the question is not in the feed: %s", feedKinds(s, "conv1"))
			}
		})
	}
}

// A newer report wins: the line of a report the model answered late is dropped, not written over the newer one's.
func TestReportLineStaleDropped(t *testing.T) {
	s, f := newQuietService(t)
	arrived, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !strings.Contains(in.System, "向 Persona 的一次汇报转述成一句话") || len(in.Messages) == 0 {
			http.Error(w, `{"error":{"message":"not this test's"}}`, http.StatusBadRequest)
			return
		}
		line := "newer line"
		if strings.Contains(in.Messages[0].Content, "first report") {
			close(arrived)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			line = "stale line"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": line}}})
	}))
	defer srv.Close()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hello.Models.AI = ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "fake"}
	f.mu.Unlock()
	todo := reportingTask(t, s)

	reportAs(t, s, `{"kind":"progress","text":"first report: copying tables"}`)
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was not asked")
	}
	reportAs(t, s, `{"kind":"progress","text":"second report: tables copied, checking counts"}`)
	waitFor(t, "the newer line", func() bool { return progressOf(t, s, todo.ID).Line == "newer line" })
	close(release)
	s.supervise.lines.Wait()
	if p := progressOf(t, s, todo.ID); p.Line != "newer line" || p.Text != "second report: tables copied, checking counts" {
		t.Fatalf("progress: %+v", p)
	}
}

func TestCleanReportLine(t *testing.T) {
	for in, want := range map[string]string{
		"迁移完成，在补测试。":                 "迁移完成，在补测试",
		"\n  \"Tables copied.\"  \n": "Tables copied",
		"**Waiting on CI**":          "Waiting on CI",
		"「」":                         "",
		strings.Repeat("长", 70):      strings.Repeat("长", 60) + "…",
	} {
		if got := cleanReportLine(in, 30); got != want {
			t.Errorf("cleanReportLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoldenReportLine(t *testing.T) {
	golden(t, "agent_report_line.system", prompts.RenderAgentReportLine(prompts.AgentReportLine{Max: 30}))
	golden(t, "agent_report_line.system_en", prompts.RenderAgentReportLine(prompts.AgentReportLine{Language: "English", Max: 60}))
	golden(t, "agent_report_line.user", prompts.RenderAgentReportLineInput(prompts.AgentReportLineInput{Task: "Migrate the database",
		Kind: "progress", Text: "Ran the **migration** script on staging:\n\n- 41 tables moved\n- `orders` needs a backfill"}))
}
