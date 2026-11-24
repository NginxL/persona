package assistant

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The card ledger (card_layers.go): each push to the owner counted by day with which of the owner's six kinds of
// decisions its source is (0: none of the six, a bug), and told once a day to the main conversation, not the owner.

// cardSourceConsts are the source constants of card_layers.go (the const block that holds cardSecretForm): name → value.
func cardSourceConsts(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "card_layers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		out := map[string]string{}
		holds := false
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == "cardSecretForm" {
					holds = true
				}
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						out[n.Name] = v
					}
				}
			}
		}
		if holds {
			return out
		}
	}
	t.Fatal("card_layers.go has no const block with cardSecretForm")
	return nil
}

// Every source constant has a kind in cardCategories (none is left to the missing-key 0), every kind is 0-6, and the
// table names no source that is not a constant.
func TestCardCategoriesCoverEverySource(t *testing.T) {
	consts := cardSourceConsts(t)
	if len(consts) < 30 {
		t.Fatalf("read only %d source constants: %v", len(consts), consts)
	}
	values := map[string]string{}
	for name, v := range consts {
		if other, dup := values[v]; dup {
			t.Errorf("%s and %s are both %q", name, other, v)
		}
		values[v] = name
		cat, ok := cardCategories[v]
		if !ok {
			t.Errorf("source %s (%q) has no kind in cardCategories", name, v)
		}
		if cat < cardCatNone || cat > cardCatInPerson {
			t.Errorf("source %s has kind %d, want 0-6", name, cat)
		}
	}
	for src := range cardCategories {
		if _, ok := values[src]; !ok {
			t.Errorf("cardCategories names %q, which is no source constant", src)
		}
	}
}

// The kinds the decision-rights design gives the sources.
func TestCardCategoriesOfTheSixKinds(t *testing.T) {
	for src, want := range map[string]int{
		cardSecretForm: cardCatInPerson, cardAgentSecretForm: cardCatInPerson,
		cardOutboundWrite: cardCatOutward,
		cardPlanMoney:     cardCatMoney, cardPlanOutward: cardCatOutward, cardPlanIrreversible: cardCatIrreversible,
		cardLeaderNeedsUser: cardCatDirection, cardDecision: cardCatDirection, cardAskEscalated: cardCatDirection,
		cardTodoRemind: cardCatDirection, cardHealthWater: cardCatDirection, cardMinutesReady: cardCatDirection,
		cardDayRecap: cardCatDirection, cardBriefFallback: cardCatDirection, cardAgentDone: cardCatDirection,
		cardConfirmOutbound: cardCatNone, cardSettingsChange: cardCatNone, cardAgentPermission: cardCatNone,
		cardAgentPlan: cardCatNone, cardAgentQuestion: cardCatNone, cardPlanWaiting: cardCatNone, cardAskUser: cardCatNone,
		cardToolPermission: cardCatNone, cardManagerQuestion: cardCatNone, cardTaskFailed: cardCatNone,
		cardUntagged: cardCatNone, "a source nobody named": cardCatNone,
	} {
		if got := cardCategoryOf(src); got != want {
			t.Errorf("%s: kind %d, want %d", src, got, want)
		}
	}
}

// cardsToday is the counts of the day it is now in s's zone, by source.
func cardsToday(t *testing.T, s *Service) map[string]cardCount {
	t.Helper()
	ctx := context.Background()
	got := map[string]cardCount{}
	for _, r := range s.cardsOn(ctx, time.Now().In(s.location(ctx)).Format(time.DateOnly)) {
		c := got[r.Source]
		c.Source, c.Category, c.N = r.Source, r.Category, c.N+r.N
		got[r.Source] = c
	}
	return got
}

// A source is counted with its kind, a push no source was named for as untagged (none of the six); the pushes
// themselves go out as before.
func TestCardLedgerCountsPushesBySourceAndKind(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	s.noteCard(ctx, cardLeaderNeedsUser, Notification{SessionID: "conv1"})
	s.push(withCard(ctx, cardPlanWaiting), Notification{Title: "a plan waits", SessionID: "conv1"})
	s.push(withCard(ctx, cardPlanWaiting), Notification{Title: "another plan waits", SessionID: "conv1"})
	s.push(ctx, Notification{Title: "from nowhere"})

	sent := 0
	for _, n := range f.pushed() {
		if strings.Contains(n.Title, "plan waits") || n.Title == "from nowhere" {
			sent++
		}
	}
	if sent != 3 {
		t.Fatalf("%d of the pushes went out, want all 3: the ledger only records", sent)
	}
	got := cardsToday(t, s)
	if c := got[cardLeaderNeedsUser]; c.Category != cardCatDirection || c.N != 1 {
		t.Errorf("leader_needs_user = %+v, want kind 1 once", c)
	}
	if c := got[cardPlanWaiting]; c.Category != cardCatNone || c.N != 2 {
		t.Errorf("plan_waiting = %+v, want kind 0 twice", c)
	}
	if c := got[cardUntagged]; c.Category != cardCatNone || c.N != 1 {
		t.Errorf("untagged = %+v, want kind 0 once", c)
	}
}

// The counts are kept in the database: a manager started again reads them.
func TestCardLedgerSurvivesARestart(t *testing.T) {
	st := newFleetMem()
	s, _ := newTaskServiceOn(t, st)
	s.noteCard(context.Background(), cardSecretForm, Notification{SessionID: "lead1"})
	again, _ := newTaskServiceOn(t, st)
	if c := cardsToday(t, again)[cardSecretForm]; c.Category != cardCatInPerson || c.N != 1 {
		t.Fatalf("after a restart secret_form = %+v, want kind 6 once", c)
	}
}

// An Agent's card takes its source from its kind, the manager's own from what ask was told or from its tool.
func TestInputCardSource(t *testing.T) {
	s, _ := newQuietService(t)
	s.cardAsks.Store("ix_own", cardConfirmOutbound)
	for _, tc := range []struct {
		ix        proto.Interaction
		escalated bool
		want      string
	}{
		{proto.Interaction{ID: "ix_own", Kind: proto.IxPermission, ToolName: "agent_send"}, false, cardConfirmOutbound},
		{proto.Interaction{ID: "ix_a", Kind: proto.IxElicitation}, true, cardAgentSecretForm},
		{proto.Interaction{ID: "ix_b", Kind: proto.IxQuestion, Secret: true}, false, cardAgentSecretForm},
		{proto.Interaction{ID: "ix_c", Kind: proto.IxPermission, ToolName: "Bash"}, true, cardAskEscalated},
		{proto.Interaction{ID: "ix_d", Kind: proto.IxPermission, ToolName: "Bash"}, false, cardAgentPermission},
		{proto.Interaction{ID: "ix_e", Kind: proto.IxPlan}, false, cardAgentPlan},
		{proto.Interaction{ID: "ix_f", Kind: proto.IxQuestion, ToolName: "ask_user"}, false, cardAskUser},
		{proto.Interaction{ID: "ix_g", Kind: proto.IxScreen}, false, cardAgentQuestion},
		// persona_settings: the settings change card, even with fields (extmgr self_settings.go)
		{proto.Interaction{ID: "ix_h", Kind: proto.IxPermission, ToolName: "persona_settings", SessionTitle: "Persona",
			Fields: []proto.Field{{Key: "value"}}}, false, cardSettingsChange},
		{proto.Interaction{ID: "ix_i", Kind: proto.IxPermission, ToolName: "repository_add", SessionTitle: "Persona"}, false, cardRepositoryAdd},
		{proto.Interaction{ID: "ix_j", Kind: proto.IxPermission, ToolName: "mail_send", SessionTitle: "Persona"}, false, cardOutboundWrite},
		{proto.Interaction{ID: "ix_k", Kind: proto.IxPermission, ToolName: "github_comment", SessionTitle: "Persona"}, false, cardOutboundWrite},
		{proto.Interaction{ID: "ix_l", Kind: proto.IxPermission, ToolName: "iphone_do", SessionTitle: "Persona"}, false, cardDeviceControl},
		{proto.Interaction{ID: "ix_m", Kind: proto.IxPermission, ToolName: "extension.ui", SessionTitle: "Persona"}, false, cardExtensionApproval},
	} {
		got := s.inputCardSource(tc.ix, tc.escalated)
		if got != tc.want {
			t.Errorf("%s: source %q, want %q", tc.ix.ID, got, tc.want)
		}
		if _, ok := cardCategories[got]; !ok {
			t.Errorf("source %s has no kind in cardCategories", got)
		}
	}
}

// The day's report: in all, each of the six kinds, and none of the six by source, the most first; nothing pushed says
// nothing. A row is counted by the kind the table gives its source now.
func TestCardLedgerReport(t *testing.T) {
	rows := []cardCount{
		{Source: cardDecision, Category: cardCatDirection, Owner: "lead1", N: 3},
		{Source: cardSecretForm, Category: cardCatInPerson, Owner: "conv1", N: 1},
		{Source: cardTaskFailed, Category: cardCatNone, Owner: "lead1", N: 1},
		{Source: cardAgentPermission, Category: cardCatNone, Owner: "lead1", N: 2},
		{Source: cardTaskFailed, Category: cardCatNone, Owner: "conv1", N: 1},
		{Source: cardSettingsChange, Category: cardCatDirection, Owner: "conv1", N: 1}, // saved under an older table
	}
	tally := tallyCards(rows)
	if tally.Total != 9 || tally.ByKind[cardCatDirection] != 3 || tally.ByKind[cardCatInPerson] != 1 || tally.ByKind[cardCatNone] != 5 {
		t.Fatalf("tally = %+v", tally)
	}
	got := cardLedgerReport("2026-11-22", rows)
	for _, want := range []string{
		"Card ledger for 2026-11-22", "Pushed to the owner's phone: 9.",
		"1 goals and direction: 3\n", "2 only the owner knows: 0\n", "3 outward as the owner: 0\n", "4 money: 0\n",
		"5 irreversible, never said: 0\n", "6 only the owner in person: 1\n",
		"None of the six (each one a bug): 5 — agent_permission×2, task_failed×2, settings_change×1.",
		"not the owner",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report lacks %q:\n%s", want, got)
		}
	}
	if got := cardLedgerReport("2026-11-22", nil); got != "" {
		t.Errorf("nothing pushed = %q, want no report", got)
	}
}

// The report goes to the main conversation once a day from the morning briefing's time, kept without waking it: no
// push to the owner, no message sent into a session.
func TestCardLedgerReportGoesToTheMainConversationWithoutAPush(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	loc := s.location(ctx)
	today := time.Now().In(loc)
	yesterday := today.AddDate(0, 0, -1)
	s.countCard(ctx, yesterday, cardCount{Source: cardAgentPermission, Category: cardCatNone, Owner: "lead1", N: 2})
	s.countCard(ctx, yesterday, cardCount{Source: cardDecision, Category: cardCatDirection, Owner: "lead1", N: 1})
	pushes := len(f.pushed())
	sends := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.calls {
			if c.Method == DeviceSessionSend {
				n++
			}
		}
		return n
	}
	sent := sends()

	early := time.Date(today.Year(), today.Month(), today.Day(), 0, 1, 0, 0, loc)
	s.reportCardLedger(ctx, early)
	if n := len(laterFor(s, "conv1")); n != 0 {
		t.Fatalf("%d kept before the morning briefing's time", n)
	}
	late := time.Date(today.Year(), today.Month(), today.Day(), 23, 30, 0, 0, loc)
	s.reportCardLedger(ctx, late)
	s.reportCardLedger(ctx, late.Add(10*time.Minute)) // once a day
	kept := laterFor(s, "conv1")
	if len(kept) != 1 {
		t.Fatalf("%d kept for the main conversation, want the one report: %+v", len(kept), kept)
	}
	if !strings.Contains(kept[0].Text, "Card ledger for "+yesterday.Format(time.DateOnly)) ||
		!strings.Contains(kept[0].Text, "None of the six (each one a bug): 2 — agent_permission×2.") ||
		!strings.Contains(kept[0].Text, "1 goals and direction: 1\n") {
		t.Errorf("report:\n%s", kept[0].Text)
	}
	if n := len(f.pushed()); n != pushes {
		t.Errorf("%d push(es) after the report, want none: it is the main conversation's, not the owner's", n-pushes)
	}
	if n := sends(); n != sent {
		t.Errorf("%d message(s) sent into a session, want none: the report waits without waking anyone", n-sent)
	}
}
