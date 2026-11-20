package assistant

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func branchQuestion() proto.Interaction {
	return proto.Interaction{ID: "ix_1", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{
			{Label: "feat/x", Description: "推荐：任务就在这上面"}, {Label: "main"}}}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(30 * time.Minute).UTC()}
}

func mergeQuestion() proto.Interaction {
	return proto.Interaction{ID: "ix_2", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "CI 过了，现在合并 PR 吗？", Options: []proto.QuestionOption{
			{Label: "合并"}, {Label: "先不合并"}}}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(30 * time.Minute).UTC()}
}

func delegateOutOf(t *testing.T, s string) delegateOut {
	t.Helper()
	var out delegateOut
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// What the model says becomes an answer only when it is a whole, valid one that sets off nothing on a red line;
// anything else is the user's, with the context the model gave and a deadline no later than the card's.
func TestDelegateResult(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		ix     proto.Interaction
		out    string
		answer string // "" : left to the user
		rec    string
	}{
		{"an option", branchQuestion(), `{"decision":"answer","answers":{"用哪个分支？":"feat/x"},"reason":"任务在 feat/x"}`, "feat/x", ""},
		{"not an option", branchQuestion(), `{"decision":"answer","answers":{"用哪个分支？":"dev"},"reason":"…"}`, "", ""},
		{"a question unanswered", branchQuestion(), `{"decision":"answer","answers":{},"reason":"…"}`, "", ""},
		{"asked", branchQuestion(), `{"decision":"ask","what":"修登录","recommend":"feat/x","reason":"目标不清楚"}`, "", "feat/x"},
		{"garbled", branchQuestion(), `{"decision":"maybe"}`, "", ""},
		{"a red line", mergeQuestion(), `{"decision":"answer","answers":{"CI 过了，现在合并 PR 吗？":"合并"},"reason":"CI 过了"}`, "", "合并"},
		{"holding off", mergeQuestion(), `{"decision":"answer","answers":{"CI 过了，现在合并 PR 吗？":"先不合并"},"reason":"等用户看"}`, "先不合并", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := delegateResult(tc.ix, delegateOutOf(t, tc.out), now)
			q := tc.ix.Questions[0].Question
			if tc.answer != "" {
				if !res.Answer || res.Answers[q] != tc.answer || res.Reason == "" || res.Ask != nil {
					t.Fatalf("got %+v, want the answer %q", res, tc.answer)
				}
				return
			}
			if res.Answer || res.Ask == nil || res.Ask.Deadline == nil || res.Ask.Deadline.After(tc.ix.ExpiresAt) {
				t.Fatalf("got %+v, want it left to the user", res)
			}
			if res.Ask.Recommend != tc.rec {
				t.Fatalf("recommend %q, want %q", res.Ask.Recommend, tc.rec)
			}
		})
	}
	// A card that lasts longer is due in 24 hours.
	long := branchQuestion()
	long.ExpiresAt = now.Add(72 * time.Hour)
	if res := delegateResult(long, delegateOut{Decision: "ask"}, now); !res.Ask.Deadline.Equal(now.Add(24 * time.Hour).UTC()) {
		t.Fatalf("deadline %v", res.Ask.Deadline)
	}
}

// assistant.delegate asks the model with the agent's recent turns and the question; the prompts are golden.
func TestDelegateAsksTheModel(t *testing.T) {
	s, f := workService(t)
	f.hello.Owner = "示例用户"
	f.hello.Agents.Delegate.Rules = "缺测试就补上，不用问我"
	f.devices = []Device{{ID: "box", Name: "示例电脑", Online: true, Reachable: true}}
	f.delegate = `{"decision":"answer","answers":{"用哪个分支？":"feat/x"},"reason":"任务就在 feat/x 上"}`
	f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "把登录页的崩溃修好，提 PR"},
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: "修好了，要推到哪个分支？"})
	f.setSessions("box", stopped("s1", "修好了，要推到哪个分支？"))
	res, err := s.Delegate(context.Background(), DelegateParams{Interaction: branchQuestion()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Answer || res.Answers["用哪个分支？"] != "feat/x" || res.Reason != "任务就在 feat/x 上" {
		t.Fatalf("result %+v", res)
	}
	if len(f.delegateAsked) != 1 {
		t.Fatalf("%d model calls", len(f.delegateAsked))
	}
	call := f.delegateAsked[0]
	golden(t, "agent_delegate.system", prompts.RenderJSONOnly(call.System))
	golden(t, "agent_delegate.user", regexp.MustCompile(`现在：[^\n]+`).ReplaceAllString(call.User, "现在：YYYY-MM-DD HH:MM Day"))

	// The model failing is an error: the hub asks the user as the card came.
	f.delegate = ""
	if _, err := s.Delegate(context.Background(), DelegateParams{Interaction: branchQuestion()}); err == nil {
		t.Fatal("no error from a failing model")
	}
}

// An assistant conversation's question, and one of the manager's cards that asks to open a link, are the user's: no
// model is asked.
func TestDelegateLeavesSomeAsTheyAre(t *testing.T) {
	s, f := workService(t)
	f.delegate = `{"decision":"answer","answers":{"用哪个分支？":"feat/x"},"reason":"…"}`
	own := stopped("s1", "")
	own.Assistant = true
	f.setSessions("box", own)
	res, err := s.Delegate(context.Background(), DelegateParams{Interaction: branchQuestion()})
	if err != nil || res.Answer || res.Ask != nil {
		t.Fatalf("an assistant conversation's: %+v %v", res, err)
	}
	if len(f.delegateAsked) != 0 {
		t.Fatal("asked the model about an assistant conversation's question")
	}
	perm := branchQuestion()
	perm.Kind = proto.IxPermission
	if res, _ := s.Delegate(context.Background(), DelegateParams{Interaction: perm}); res.Answer {
		t.Fatal("answered a permission")
	}

	// A triage card asking to open a link.
	f.setSessions("box", stopped("s2", "打开这个链接授权：https://example.com/device"))
	f.triage = `{"phase":"blocked","on":"user","line":"等你授权","link":{"url":"https://example.com/device"},"questions":[{"question":"授权好了吗？","options":[{"label":"好了"},{"label":"先不管"}]}]}`
	triage(t, s, "s2")
	cards := s.workCards()
	if len(cards) != 1 {
		t.Fatalf("%d cards", len(cards))
	}
	if res, _ := s.Delegate(context.Background(), DelegateParams{Interaction: cards[0]}); res.Answer || len(f.delegateAsked) != 0 {
		t.Fatalf("an authorization card: %+v", res)
	}
}

// The assistant's answer to one of the manager's cards reaches the agent saying it is the assistant's, and why.
func TestDelegatedAnswerToTheAgent(t *testing.T) {
	golden(t, "agent_answer.delegated", renderDelegatedAnswer([]agentAnswerItem{{"用哪个方案？", "用 D4"}}, "D4 是 Agent 推荐的，和任务不冲突", ""))
}

// "Answering questions" is a category of its own: by default on the triage's connection with Opus when that connection serves Claude
// models, the triage's model on another vendor's; a model the user picks for it wins, and the triage does not move.
func TestDelegateModel(t *testing.T) {
	for _, tc := range []struct{ triage, want string }{
		{"claude-haiku-5-5", "claude-opus-5-5"},
		{"gpt-5-mini", "gpt-5-mini"},
	} {
		f := newFakeHub()
		f.ownModels = true
		key := t.TempDir() + "/key"
		if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.hello.Models.AI = ai.Config{BaseURL: "http://127.0.0.1:1", APIKeyFile: key, Model: tc.triage}
		s := newTestService(t, f)
		ctx := context.Background()
		b, err := s.resolveModel(ctx, "agent.delegate")
		if err != nil || b.Model != tc.want || b.Provider != "legacy.triage" {
			t.Fatalf("%s: agent.delegate %+v %v, want %s", tc.triage, b, err, tc.want)
		}
		if b, _ := s.resolveModel(ctx, "agent.triage"); b.Model != tc.triage {
			t.Fatalf("agent.triage moved: %+v", b)
		}
		p, err := s.modelPurpose(ctx, "agent.delegate", s.modelProviders(ctx))
		if err != nil || p.CategoryID != "agent.delegate" || len(p.Fields) == 0 || p.Effective.Model != tc.want {
			t.Fatalf("%s: settings show %+v %v", tc.triage, p, err)
		}
		// "Inherited From" says what it runs on: the triage's connection, and the model actually on it.
		if want := "跟「信息筛选」同一个接入（hub 分拣 API），模型 " + tc.want; p.InheritedFrom != want {
			t.Fatalf("%s: inherited from %q, want %q", tc.triage, p.InheritedFrom, want)
		}
		setModel(t, s, "agent.delegate", ModelBinding{Model: "claude-sonnet-5-5"})
		if b, _ := s.resolveModel(ctx, "agent.delegate"); b.Model != "claude-sonnet-5-5" {
			t.Fatalf("the user's pick: %+v", b)
		}
		if p, _ := s.modelPurpose(ctx, "agent.delegate", s.modelProviders(ctx)); p.InheritedFrom != "跟「信息筛选」同一个接入（hub 分拣 API）" {
			t.Fatalf("%s: a model of the user's: inherited from %q", tc.triage, p.InheritedFrom)
		}
		setModel(t, s, "agent.delegate", ModelBinding{Provider: "launcher.claude", Model: "claude-sonnet-5-5"})
		if p, _ := s.modelPurpose(ctx, "agent.delegate", s.modelProviders(ctx)); p.InheritedFrom != "" {
			t.Fatalf("%s: a connection of the user's: inherited from %q", tc.triage, p.InheritedFrom)
		}
	}
}

// styleQuestion asks how to write new tests, the way the agent's card lists it: one option with a description.
func styleQuestion() proto.Interaction {
	return proto.Interaction{ID: "ix_3", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "新测试写成表驱动还是每个用例一个函数？", Options: []proto.QuestionOption{
			{Label: "表驱动", Description: "和这个包现有的测试一致"}, {Label: "每个用例一个函数"}}}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(30 * time.Minute).UTC()}
}

// The delegation input lists an option as "label：description", and the model sometimes copies the whole line (#3929).
// That names the option: the answer is its label, and a red line is still the user's.
func TestDelegateResultTakesAnOptionWrittenOut(t *testing.T) {
	now := time.Now()
	deploy := proto.Interaction{ID: "ix_4", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "现在部署吗？", Options: []proto.QuestionOption{
			{Label: "部署", Description: "推到线上"}, {Label: "先不部署"}}}},
		CreatedAt: now.UTC(), ExpiresAt: now.Add(30 * time.Minute).UTC()}
	for _, tc := range []struct {
		name   string
		ix     proto.Interaction
		answer string // the model's
		want   string // "": left to the user
		rec    string
	}{
		{"label：description", styleQuestion(), "表驱动：和这个包现有的测试一致", "表驱动", ""},
		{"a description with a colon of its own", branchQuestion(), "feat/x：推荐：任务就在这上面", "feat/x", ""},
		{"its own reason after the label", styleQuestion(), "表驱动：更好维护", "", ""},
		{"a red line written out", deploy, "部署：推到线上", "", "部署"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.ix.Questions[0].Question
			out := delegateOut{Decision: "answer", Reason: "和现有的一致", Answers: map[string]json.RawMessage{}}
			out.Answers[q], _ = json.Marshal(tc.answer)
			res := delegateResult(tc.ix, out, now)
			if tc.want != "" {
				if !res.Answer || res.Answers[q] != tc.want || res.Ask != nil {
					t.Fatalf("got %+v, want the answer %q", res, tc.want)
				}
				return
			}
			if res.Answer || res.Ask == nil || res.Ask.Recommend != tc.rec {
				t.Fatalf("got %+v, want it left to the user recommending %q", res, tc.rec)
			}
		})
	}
}
