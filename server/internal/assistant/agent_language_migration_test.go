package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// These goldens observe actual model requests, not an isolated template renderer.
func TestGoldenAgentLanguageModelCalls(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			s, f := workService(t)
			setAgentLanguage(f, locale)
			f.hello.Owner = "Example owner"
			f.hello.Agents.Delegate.Rules = "Keep source-rule 原文 unchanged."
			f.triage = `{"phase":"done","line":"Ready"}`
			f.delegate = `{"decision":"answer","answers":{"用哪个分支？":"feat/x"},"reason":"Within the task"}`
			f.setSessions("box", stopped("s1", "Source reply 原文; PR #7"))
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "Source request 原文"}, proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Source reply 原文; PR #7"})
			ctx := context.Background()
			v := s.newWorkView(ctx)
			c, _ := v.session("box", "s1")
			if _, _, err := s.triageTurnModel(ctx, v, "box", c); err != nil {
				t.Fatal(err)
			}
			if len(f.triageAsked) != 1 {
				t.Fatal("missing triage call")
			}
			// Normalize only the generated time line; source data is checked verbatim.
			clean := func(text string) string {
				lines := strings.Split(text, "\n")
				lines[0] = "TIME"
				return strings.Join(lines, "\n")
			}
			call := f.triageAsked[0]
			golden(t, "agent_language.triage.system."+locale, call.System)
			golden(t, "agent_language.triage.input."+locale, clean(call.User))
			if !strings.Contains(call.User, "Source request 原文") || !strings.Contains(call.User, "Source reply 原文; PR #7") {
				t.Fatal("source data changed")
			}
			if _, err := s.Delegate(ctx, DelegateParams{Interaction: branchQuestion()}); err != nil {
				t.Fatal(err)
			}
			if len(f.delegateAsked) != 1 {
				t.Fatal("missing delegate call")
			}
			call = f.delegateAsked[0]
			golden(t, "agent_language.delegate.system."+locale, call.System)
			golden(t, "agent_language.delegate.input."+locale, clean(call.User))
			for _, text := range []string{"Source request 原文", "Source reply 原文; PR #7", "用哪个分支？", "feat/x"} {
				if !strings.Contains(call.User, text) {
					t.Fatalf("lost original %q", text)
				}
			}
			if !strings.Contains(call.System, f.hello.Agents.Delegate.Rules) {
				t.Fatal("owner rules changed")
			}
			f.reportLine = "Report ready"
			if got := s.reportLineFromModel(ctx, "Task 原文", "progress", "Report 原文\n`source-id`"); got != "Report ready" {
				t.Fatal(got)
			}
			if len(f.reportLineAsked) != 1 {
				t.Fatal("missing report call")
			}
			call = f.reportLineAsked[0]
			golden(t, "agent_language.report.system."+locale, call.System)
			golden(t, "agent_language.report.input."+locale, call.User)
		})
	}
}

func TestGoldenAgentLanguageDeliveries(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			// Subscription resets take the current language at delivery, after triage.
			s, f := workService(t)
			reset := time.Now().Add(time.Hour)
			f.setSessions("box", limitTurn("s1", reset, false))
			triage(t, s, "s1")
			setAgentLanguage(f, locale)
			s.workTick(context.Background(), reset.Add(2*time.Minute))
			sent := f.sentToAgents()
			if len(sent) != 1 {
				t.Fatalf("limit sends: %q", sent)
			}
			text := strings.TrimPrefix(sent[0], "s1: ")
			golden(t, "agent_language.limit."+locale, text)
			if in := prompts.ParseInjected(text); in == nil || in.State != "limit_continue" {
				t.Fatalf("lost continuation: %+v", in)
			}
			// A genuine auth relay completion passes through the production send path.
			s, f, _ = relayService(t, wire.AuthRelayResult{Status: "done", App: "Example CLI 原文"}, printedBy("Bash", relayLink))
			setAgentLanguage(f, locale)
			triage(t, s, "s1")
			sent = f.sentToAgents()
			if len(sent) != 1 || !strings.Contains(sent[0], "Example CLI 原文") {
				t.Fatalf("auth sends: %q", sent)
			}
			golden(t, "agent_language.auth."+locale, strings.TrimPrefix(sent[0], "s1: "))
			// A read-only task start carries the localized prohibition and original task.
			s, f = newTaskService(t)
			setAgentLanguage(f, locale)
			task := PlanTask{Title: "Inspect", Prompt: "Inspect source 原文", Machine: "box", Cwd: "/code/app", Readonly: true}
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			if len(f.starts) != 1 {
				t.Fatal("missing start")
			}
			text, _ = f.starts[0]["prompt"].(string)
			if !strings.HasPrefix(text, task.Prompt) || !strings.Contains(text, prompts.RenderReadonlyTaskConstraint(answerLanguage(locale))) {
				t.Fatal(text)
			}
			golden(t, "agent_language.readonly."+locale, text)
		})
	}
}

func setAgentLanguage(f *fakeHub, locale string) {
	f.mu.Lock()
	f.hello.Locale = locale
	f.mu.Unlock()
}

func TestAgentLanguageReanswerIdentity(t *testing.T) {
	for _, first := range []string{"en", "ja", "zh-Hans"} {
		t.Run(first, func(t *testing.T) {
			df := newDelegateFleet(t, func(DelegateParams) (DelegateResult, error) {
				return DelegateResult{Answer: true, Answers: map[string]string{"用哪个分支？": "feat/x"}, Reason: "Original reason 原文"}, nil
			})
			answerOf(t, df.ask(delegateQuestion("ix_language")))
			ctx := context.Background()
			for i, locale := range []string{first, "en", "ja", "zh-Hans"} {
				setAgentLanguage(df.f, locale)
				code, err := df.s.reanswer(ctx, "ix_language", map[string]string{"用哪个分支？": "main"})
				if code != 0 || err != nil {
					t.Fatalf("%s: %d %v", locale, code, err)
				}
				got := df.f.sentToAgents()
				if len(got) != 1 {
					t.Fatalf("locale change redelivered answer %d: %q", i, got)
				}
				if i == 0 {
					golden(t, "agent_language.reanswer.production."+first, strings.TrimPrefix(got[0], "s1: "))
				}
			}
		})
	}
}

// Input Language is subordinate to the one language captured for the model call.
func TestAgentLanguageJudgeBoundaries(t *testing.T) {
	for _, lang := range []string{"", "English", "日本語"} {
		sys := prompts.AgentDelegate{Language: lang, Rules: "Source policy 原文"}
		in := prompts.AgentDelegateInput{Language: "wrong", Reply: "Original reply 原文"}
		m := &cannedJSON{answer: `{"decision":"answer","answers":{"CI 过了，现在合并 PR 吗？":"合并"},"reason":"passed"}`}
		res, err := judgeDelegateWith(context.Background(), m, sys, in, mergeQuestion())
		if err != nil || res.Answer || res.Ask == nil {
			t.Fatalf("red-line gate bypassed: %+v %v", res, err)
		}
		in.Language = lang
		if m.user != prompts.RenderAgentDelegateInput(in) {
			t.Fatal("model input did not use captured system language")
		}
		reply := "Please confirm the sample choice?"
		out := map[string]any{"phase": "blocked", "on": "user", "line": "Choose", "questions": []map[string]any{{"question": "Invented question?", "header": "Choice", "options": []map[string]string{{"label": "A"}, {"label": "B"}}}}}
		raw, _ := json.Marshal(out)
		m = &cannedJSON{answer: string(raw)}
		w, _, err := judgeTriageWith(context.Background(), m, triageJudgeInput{System: prompts.AgentTriage{Language: lang}, Input: prompts.AgentTriageInput{Language: "wrong", Reply: reply, Answered: []prompts.AgentAnswerItem{{Question: "Original question 原文", Answer: "Original answer 原文"}}}, Reply: reply})
		if err != nil || w == nil || len(w.Questions) != 0 {
			t.Fatalf("invented question survived: %+v %v", w, err)
		}
		if strings.Contains(m.user, "wrong") || !strings.Contains(m.user, "Original answer 原文") {
			t.Fatal("triage locale or original record lost")
		}
	}
}

func TestGoldenAgentLanguageAnswersAndCloseout(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		for _, delegated := range []bool{false, true} {
			t.Run(locale+map[bool]string{false: "/owner", true: "/assistant"}[delegated], func(t *testing.T) {
				s, f := workService(t)
				setAgentLanguage(f, locale)
				f.triage = `{"phase":"blocked","on":"user","line":"Choose","questions":[{"question":"用哪个方案？","header":"Choice","options":[{"label":"用 D4"},{"label":"用 D2"}]}]}`
				f.setSessions("box", stopped("s1", "用哪个方案？"))
				triage(t, s, "s1")
				cards := s.workCards()
				if len(cards) != 1 {
					t.Fatalf("cards: %+v", cards)
				}
				r := proto.Resolution{Answers: map[string]string{"用哪个方案？": "用 D4"}, Reason: "Original reason 原文"}
				if delegated {
					r.AnsweredBy = proto.AnsweredByAssistant
				}
				answerCard(t, s, cards[0].ID, r)
				sent := f.sentToAgents()
				if len(sent) != 1 || !strings.Contains(sent[0], "用哪个方案？ → 用 D4") {
					t.Fatalf("answers: %q", sent)
				}
				name := "owner"
				if delegated {
					name = "assistant"
					if !strings.Contains(sent[0], r.Reason) {
						t.Fatal("lost assistant reason")
					}
				}
				golden(t, "agent_language.answer."+name+"."+locale, strings.TrimPrefix(sent[0], "s1: "))
			})
		}
		s, f := workService(t)
		setAgentLanguage(f, locale)
		closeoutOn(t, s)
		for _, after := range []bool{false, true} {
			name := "brief"
			if after {
				name = "brief_delivery"
			}
			golden(t, "agent_language.closeout."+name+"."+locale, s.closeoutBrief(context.Background(), "box", after))
		}
		golden(t, "agent_language.closeout.ask."+locale, renderCloseoutAsk(answerLanguage(locale), closeout.Example))
		golden(t, "agent_language.closeout.required."+locale, renderCloseoutRequired(answerLanguage(locale), closeout.Example))
		// Review data and the machine-readable final-line contract remain verbatim.
		golden(t, "agent_language.review."+locale, prompts.RenderAgentReview(prompts.AgentReview{Language: answerLanguage(locale), Title: "Task 原文", Ask: "Original requirement 原文", DoneWhen: []string{"Original criterion 原文"}, Scope: "base..head", Diff: "+ source 原文", Omitted: 2}))
		golden(t, "agent_language.triage_records."+locale, prompts.RenderAgentTriageInput(prompts.AgentTriageInput{Language: answerLanguage(locale), Now: "TIME", Agent: "Example agent", Machine: "Example computer", Reply: "Source reply 原文", Answered: []prompts.AgentAnswerItem{{Question: "Original question 原文", Answer: "Original answer 原文"}}, ApprovedWait: "CI #7", ApprovedThen: "push branch/source", ApprovedByTask: true}))
		golden(t, "agent_language.reanswer."+locale, prompts.RenderAgentReanswer(prompts.AgentReanswer{Language: answerLanguage(locale), Items: []prompts.AgentReanswerItem{{Question: "Original question 原文", Before: "Before 原文", Answer: "After 原文"}}}))
	}
}

// This default-only inventory is also run locally with all 17 original templates
// overlaid from the starting commit, proving byte equality independently of the
// translated branches and without changing historical goldens.
func TestGoldenAgentLanguageLegacyAll(t *testing.T) {
	for _, populated := range []bool{false, true} {
		suffix := "empty"
		if populated {
			suffix = "populated"
		}
		answer := prompts.AgentAnswer{}
		auth := prompts.AgentAuthRelayed{}
		delegate := prompts.AgentDelegate{}
		din := prompts.AgentDelegateInput{}
		tin := prompts.AgentTriageInput{}
		reanswer := prompts.AgentReanswer{}
		report := prompts.AgentReportLineInput{}
		review := prompts.AgentReview{}
		brief := prompts.AgentCloseoutBrief{Form: "<closeout …>"}
		if populated {
			answer.ByAssistant = true
			answer.Reason = "Reason 原文"
			answer.Items = []prompts.AgentAnswerItem{{Question: "Question 原文", Answer: "Answer 原文"}}
			auth.App = "App 原文"
			delegate.Owner = "Owner 原文"
			delegate.Rules = "Rules 原文"
			din = prompts.AgentDelegateInput{Now: "TIME", Agent: "Agent 原文", Machine: "box", Cwd: "/code/example", Project: "Project 原文", Task: "Task 原文", Decisions: "Decisions 原文", Asks: []string{"Ask 原文"}, Reply: "Reply 原文", Questions: []prompts.DelegateQuestion{{Question: "Question 原文", MultiSelect: true, Options: []prompts.DelegateOption{{Label: "Label 原文", Description: "Description 原文"}}}}}
			tin = prompts.AgentTriageInput{Now: "TIME", Agent: "Agent 原文", Machine: "box", Cwd: "/code/example", Project: "Project 原文", Ask: "Ask 原文", Reply: "Reply 原文", Others: []prompts.AgentTriageOther{{ID: "other-id", Label: "Label 原文", Line: "Line 原文"}}, Answered: answer.Items, ApprovedWait: "CI #7", ApprovedThen: "push source/branch", ApprovedByTask: true}
			reanswer.Items = []prompts.AgentReanswerItem{{Question: "Question 原文", Before: "Before 原文", Answer: "After 原文"}}
			report = prompts.AgentReportLineInput{Task: "Task 原文", Kind: "progress", Text: "Report 原文"}
			review = prompts.AgentReview{Title: "Task 原文", Ask: "Ask 原文", DoneWhen: []string{"Criterion 原文"}, Scope: "base..head", Diff: "+source 原文", Omitted: 2}
			brief.Session = true
			brief.AfterDelivery = true
		}
		values := map[string]string{
			"answer": prompts.RenderAgentAnswer(answer), "auth": prompts.RenderAgentAuthRelayed(auth),
			"delegate": prompts.RenderAgentDelegate(delegate), "delegate_input": prompts.RenderAgentDelegateInput(din),
			"triage": prompts.RenderAgentTriage(prompts.AgentTriage{Owner: delegate.Owner}), "triage_input": prompts.RenderAgentTriageInput(tin),
			"report": prompts.RenderAgentReportLine(prompts.AgentReportLine{Max: 30}), "report_input": prompts.RenderAgentReportLineInput(report),
			"review": prompts.RenderAgentReview(review), "readonly": prompts.RenderReadonlyTaskConstraint(""),
			"limit": prompts.RenderAgentLimitContinue(""), "peer": prompts.RenderPeerMessage(prompts.PeerMessage{Text: "Source 原文"}),
			"reanswer": prompts.RenderAgentReanswer(reanswer), "closeout_brief": prompts.RenderAgentCloseoutBrief(brief),
			"closeout_ask": renderCloseoutAsk("", closeout.Example), "closeout_required": renderCloseoutRequired("", closeout.Example),
		}
		for name, text := range values {
			// Framing preserves exact final newlines without trailing blank lines in fixture files.
			golden(t, "agent_language.legacy."+name+"."+suffix, "<rendered-prompt>\n"+text+"\n</rendered-prompt>")
		}
	}
}
