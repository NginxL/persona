package devices

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func uw(label string, minutes int, used float64, resets time.Time) proto.UsageWindow {
	return proto.UsageWindow{Label: label, WindowMinutes: minutes, UsedPercent: used, ResetsAt: &resets}
}

func TestMergeUsageAccountsStillDedupesClaudeByWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h5, d7 := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	other5, other7 := now.Add(4*time.Hour), now.Add(5*24*time.Hour)
	in := []proto.UsageAccount{
		// machine A saw the account a while ago, machine B saw it just now
		{Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-2 * time.Hour), Windows: []proto.UsageWindow{uw("5h", 300, 10, h5), uw("7d", 10080, 20, d7)}},
		{Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("5h", 300, 30, h5), uw("7d", 10080, 25, d7)}},
		// another account: other reset times
		{Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("5h", 300, 5, other5), uw("7d", 10080, 90, other7)}},
		// the same reset second under another provider is not the same account
		{Provider: "gemini", Name: "Gemini", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 1, d7)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 3 {
		t.Fatalf("accounts: %+v", got)
	}
	// the most used first
	if got[0].Windows[1].UsedPercent != 90 {
		t.Errorf("first = %+v", got[0])
	}
	var merged *proto.UsageAccount
	for i := range got {
		if got[i].Provider == "claude" && got[i].Windows[0].UsedPercent == 30 {
			merged = &got[i]
		}
	}
	if merged == nil || merged.Windows[1].UsedPercent != 25 || !merged.SampledAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("the newest sample must win: %+v", got)
	}
	// two accounts with one name are numbered (which one gets the 2 follows their ids, not their numbers)
	names := map[string]bool{}
	for _, a := range got {
		names[a.Name] = true
	}
	if !names["Claude Max"] || !names["Claude Max 2"] || !names["Gemini"] || len(names) != 3 {
		t.Errorf("names: %v", names)
	}
	for _, a := range got {
		if a.ID == "" || a.Stale {
			t.Errorf("account %+v", a)
		}
	}
}

func TestUsageWindowsReconcilesAntigravitySources(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	five, week := now.Add(time.Hour), now.Add(3*24*time.Hour)
	gemini5, gemini7 := uw("5h", 300, 3, five), uw("7d", 10080, 38, week)
	claude5, claude7 := uw("5h", 300, 0, five), uw("7d", 10080, 0, week)
	gemini5.Model, gemini7.Model = "Gemini Models", "Gemini Models"
	claude5.Model, claude7.Model = "Claude and GPT models", "Claude and GPT models"
	node := proto.UsageAccount{Provider: "agy", Name: "Antigravity", Local: true, Origin: "computer/0",
		Machines: []string{"Computer"}, SampledAt: now, Windows: []proto.UsageWindow{gemini5, gemini7, claude5, claude7}}
	gemini := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Gemini", Pass: true, Origin: "external#0",
		Sources: []string{"External"}, SampledAt: now.Add(-time.Minute), Headline: "Quota details",
		Windows: []proto.UsageWindow{uw("5h", 300, 2, five), uw("7d", 10080, 36, week.Add(time.Millisecond))}}
	gemini.Windows[1].Title = "Weekly Gemini quota"
	protection := 90.0
	gemini.Windows[1].ProtectPercent = &protection
	claude := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Claude/GPT", Pass: true, Origin: "external#1",
		Sources: []string{"External"}, SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 0, five), uw("7d", 10080, 0, week)}}
	codex := proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 23, week)}}
	merged := mergeUsageAccounts([]proto.UsageAccount{node, gemini, claude, codex}, now)
	got := usageWindowsAnswer(merged, nil, now)["accounts"].([]proto.UsageAccount)
	if len(got) != 3 || got[0].Name != "Codex Pro" || got[1].Name != gemini.Name || got[2].Name != claude.Name {
		t.Fatalf("want Codex and two distinct external Antigravity quota groups: %+v", got)
	}
	g := got[1]
	if g.ID != usageRowID("antigravity", "extension|external#0") || g.Headline != gemini.Headline ||
		!g.Local || !slices.Equal(g.Machines, node.Machines) || !slices.Equal(g.Sources, gemini.Sources) ||
		!g.SampledAt.Equal(now) || g.StaleAt == nil || !g.StaleAt.Equal(now.Add(usageHubStaleAfter)) {
		t.Fatalf("external row identity, sources or sample time lost: %+v", g)
	}
	if len(g.Windows) != 2 || g.Windows[0].UsedPercent != 3 || g.Windows[1].UsedPercent != 38 ||
		g.Windows[1].Title != gemini.Windows[1].Title || g.Windows[1].ProtectPercent != &protection {
		t.Fatalf("Gemini needs the newest percentages and the external window metadata: %+v", g.Windows)
	}
	if c := got[2]; len(c.Windows) != 2 || mostUsed(c) != 0 || !c.Local {
		t.Fatalf("Claude/GPT has a separate quota even with the same resets: %+v", c)
	}
	if len(merged) != 4 || merged[0].Provider != "antigravity" || merged[0].ID != usageAccountID(node) ||
		len(merged[0].Windows) != 4 || merged[2].Windows[1].UsedPercent != 36 || gemini.Windows[1].UsedPercent != 36 {
		t.Fatalf("the display merge changed the poll's cached samples or the local row ID: %+v", merged)
	}
}

func TestUsageWindowsKeepsDistinctAntigravityWindows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	week := now.Add(24 * time.Hour)
	gemini := uw("7d", 10080, 38, week)
	gemini.Model = "Gemini Models"
	claude := uw("7d", 10080, 16, week)
	claude.Model = "Claude and GPT models"
	local := proto.UsageAccount{Provider: "agy", Name: "Antigravity", Local: true, Origin: "computer/0",
		SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{claude, gemini}}
	remote := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Gemini", Ref: "source:gemini",
		Sources: []string{"External"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 39, week)}}
	answer := func(accounts ...proto.UsageAccount) []proto.UsageAccount {
		return usageWindowsAnswer(mergeUsageAccounts(accounts, now), nil, now)["accounts"].([]proto.UsageAccount)
	}
	got := answer(local, remote)
	if len(got) != 2 || mostUsed(got[0]) != 39 || len(got[1].Windows) != 1 || got[1].Windows[0].Model != claude.Model {
		t.Fatalf("want the newer Gemini reading and the unmatched Claude/GPT window: %+v", got)
	}
	otherSeat := remote
	otherSeat.Ref = "source:other-seat"
	if got := answer(local, remote, otherSeat); len(got) != 3 || len(got[2].Windows) != 2 {
		t.Fatalf("matching resets on multiple gateway seats cannot identify the local subscription: %+v", got)
	}
	for _, remoteWindow := range []proto.UsageWindow{
		uw("7d", 10080, 39, week.Add(time.Second)),
		uw("5h", 300, 39, week),
		{Label: "7d", WindowMinutes: 10080, UsedPercent: 39},
	} {
		remote.Windows = []proto.UsageWindow{remoteWindow}
		if got := answer(local, remote); len(got) != 2 || len(got[1].Windows) != 2 {
			t.Fatalf("window %+v must leave both local quotas intact: %+v", remoteWindow, got)
		}
	}

	remote.Name = "Gateway seat"
	remote.Windows = []proto.UsageWindow{uw("7d", 10080, 39, week)}
	remote.Windows[0].Model = "gemini"
	remote.Stale, remote.Problem = true, "unreachable"
	if got := answer(local, remote); len(got) != 2 || mostUsed(got[0]) != 38 || got[0].Stale || got[0].Problem != "" ||
		got[0].Windows[0].Model != "gemini" || len(got[1].Windows) != 1 || got[1].Windows[0].Model != claude.Model {
		t.Fatalf("an older fresh reading should replace the stale model-scoped window: %+v", got)
	}
}

// A local sample is not a bridge between two different accounts of one gateway (#7324).
func TestMergeUsageAccountsDoesNotBridgeGatewayAccounts(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	resets := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		{Provider: "claude", Name: "Claude Max", Local: true, Origin: "mac/0", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 10, resets)}},
		{Provider: "claude", Name: "Seat A", Ref: "s1:a", Origin: "s1:a", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 20, resets)}},
		{Provider: "claude", Name: "Seat B", Ref: "s1:b", Origin: "s1:b", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 30, resets)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 {
		t.Fatalf("two gateway seats merged through a local sample: got %d account(s), want 2: %+v", len(got), got)
	}
	used := map[string]float64{}
	for _, a := range got {
		used[a.Ref] = mostUsed(a)
	}
	if used["s1:a"] != 10 || used["s1:b"] != 30 {
		t.Fatalf("a gateway seat or its usage disappeared: %+v", got)
	}
}

func TestMergeUsageAccountsDropsResetStaleAndOld(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "reset", SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 99, now.Add(-time.Second))}},
		{Provider: "codex", Name: "old", SampledAt: now.Add(-usageHubMaxAge - time.Hour), Windows: []proto.UsageWindow{uw("7d", 10080, 1, now.Add(time.Hour))}},
		{Provider: "codex", Name: "stale", SampledAt: now.Add(-usageHubStaleAfter - time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 80, now.Add(time.Hour))}},
		{Provider: "claude", Name: "fresh", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 5, now.Add(time.Hour))}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 || got[0].Name != "fresh" || got[1].Name != "stale" || !got[1].Stale {
		t.Fatalf("got %+v", got)
	}
	if got := mergeUsageAccounts(nil, now); got == nil || len(got) != 0 {
		t.Errorf("nothing merges to [] not nil: %#v", got)
	}
}

func TestMergeUsageAccountsCodexUsesLatestSnapshotAcrossAReset(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "Codex Pro", CodexAccountKey: "same", SampledAt: now.Add(-3 * time.Hour), Windows: []proto.UsageWindow{uw("5h", 300, 95, now.Add(time.Hour)), uw("7d", 10080, 95, week.Add(-time.Second))}},
		{Provider: "codex", Name: "Codex Pro", CodexAccountKey: "same", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 2, week)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 1 || len(got[0].Windows) != 1 || got[0].Windows[0].UsedPercent != 2 || !got[0].Windows[0].ResetsAt.Equal(week) {
		t.Fatalf("want only the latest snapshot: %+v", got)
	}
	in[1].Windows = []proto.UsageWindow{uw("7d", 10080, 3, week.Add(7*24*time.Hour))}
	if after := mergeUsageAccounts(in, now); len(after) != 1 || after[0].ID != got[0].ID {
		t.Errorf("new quota cycle changed the row ID: %+v to %+v", got, after)
	}
	in[1].Windows = []proto.UsageWindow{uw("7d", 10080, 80, now.Add(-time.Second))}
	if after := mergeUsageAccounts(in, now); len(after) != 0 {
		t.Errorf("an expired latest snapshot restored an older one: %+v", after)
	}
}

func TestUsagePollAsksOnlineMachinesAndRemembersTheirLastAnswer(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var asked atomic.Int32
	win := []proto.UsageWindow{uw("7d", 10080, 12, now.Add(time.Hour))}
	var fail atomic.Bool
	on := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		asked.Add(1)
		if fail.Load() {
			return proto.UsageSnapshot{}, errors.New("unknown method")
		}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex", SampledAt: now, Windows: win}}}}, nil
	}}
	off := usageSource{id: "m2", online: false, ask: func(context.Context) (proto.UsageSnapshot, error) {
		t.Error("an offline machine is not asked")
		return proto.UsageSnapshot{}, nil
	}}
	var u usageWindowsHub
	got := u.poll(context.Background(), []usageSource{on, off}, now)
	if len(got) != 1 || asked.Load() != 1 {
		t.Fatalf("got %+v asked %d", got, asked.Load())
	}
	// within the fresh time: no second call
	u.poll(context.Background(), []usageSource{on, off}, now.Add(time.Second))
	if asked.Load() != 1 {
		t.Errorf("asked again within %v", usageHubFresh)
	}
	// later, the machine fails (an older node): its last answer stays
	fail.Store(true)
	got = u.poll(context.Background(), []usageSource{on, off}, now.Add(usageHubFresh+time.Second))
	if len(got) != 1 {
		t.Errorf("last answer lost: %+v", got)
	}
	// a machine taken out of hub.json is forgotten
	got = u.poll(context.Background(), nil, now.Add(2*usageHubFresh+2*time.Second))
	if len(got) != 0 {
		t.Errorf("removed machine kept: %+v", got)
	}
}

func TestUsagePollForgetsAMachineThatTurnedItOff(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	enabled := true
	src := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		if !enabled {
			return proto.UsageSnapshot{Enabled: false}, nil
		}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{Provider: "claude", Name: "Claude", SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 1, now.Add(time.Hour))}}}}}, nil
	}}
	var u usageWindowsHub
	if got := u.poll(context.Background(), []usageSource{src}, now); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	enabled = false
	if got := u.poll(context.Background(), []usageSource{src}, now.Add(usageHubFresh+time.Second)); len(got) != 0 {
		t.Errorf("off machine still shown: %+v", got)
	}
}

func TestMachineUsageWindowsSwitch(t *testing.T) {
	off := false
	if !(&Machine{}).usageWindows() || (&Machine{cfg: MachineConfig{UsageWindows: &off}}).usageWindows() {
		t.Error("usage_windows: unset is on, false is off")
	}
}

// #3794: a model's own allowance running out does not make the account the fullest; the plan windows decide the order.
func TestMergeUsageAccountsOrdersByThePlanWindows(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fable := uw("7d", 10080, 100, now.Add(3*24*time.Hour))
	fable.Model = "fable"
	modelOnly := uw("7d", 10080, 70, now.Add(6*24*time.Hour))
	modelOnly.Model = "argon"
	in := []proto.UsageAccount{
		{Provider: "claude", Name: "A", SampledAt: now, Ref: "gw:a", Windows: []proto.UsageWindow{uw("5h", 300, 10, now.Add(time.Hour)), fable}},
		{Provider: "claude", Name: "B", SampledAt: now, Ref: "gw:b", Windows: []proto.UsageWindow{uw("5h", 300, 60, now.Add(2*time.Hour))}},
		// no plan window at all: its model window is what there is
		{Provider: "gemini", Name: "C", SampledAt: now, Ref: "gw:c", Windows: []proto.UsageWindow{modelOnly}},
	}
	got := mergeUsageAccounts(in, now)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if len(names) != 3 || names[0] != "C" || names[1] != "B" || names[2] != "A" {
		t.Fatalf("order = %v, want [C B A]", names)
	}
}

// Each account says when it turns stale (#1905): a client that keeps the answer while refreshes fail judges it itself,
// with the threshold of where the newest sample came from (a computer's 30 minutes, a gateway's 6 hours).
func TestMergeUsageAccountsSaysWhenEachTurnsStale(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "machine", SampledAt: now.Add(-10 * time.Minute), Local: true, Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)}},
		{Provider: "claude", Name: "gateway", SampledAt: now.Add(-time.Hour), Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 20, week.Add(time.Hour))}},
		{Provider: "kimi", Name: "flagged", SampledAt: now, Sources: []string{"Relay"}, Stale: true, Windows: []proto.UsageWindow{uw("7d", 10080, 30, week)}},
	}
	got := map[string]proto.UsageAccount{}
	for _, a := range mergeUsageAccounts(in, now) {
		got[a.Name] = a
	}
	for name, want := range map[string]time.Time{
		"machine": now.Add(-10 * time.Minute).Add(usageHubStaleAfter),
		"gateway": now.Add(-time.Hour).Add(usageGatewayStaleAfter),
		"flagged": now.Add(usageGatewayStaleAfter),
	} {
		a := got[name]
		if a.StaleAt == nil || !a.StaleAt.Equal(want) {
			t.Errorf("%s: stale_at %v, want %v", name, a.StaleAt, want)
		}
	}
	if got["machine"].Stale || got["gateway"].Stale || !got["flagged"].Stale {
		t.Errorf("stale now: %+v", got)
	}
}

// An account read on computers names them (#1907): a phone or another computer cannot tell "this computer" apart from
// the device in hand. The same account on two computers names both; a node's own say is not taken, a rename is.
func TestUsagePollNamesTheComputersThatReadAnAccount(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	const accountKey = "a6604f017eb48c3a5c675159c4736adf3f68f4f3ba603c1ca4ebca6bb683ffb8"
	const sourceKey = "e7ad658cfab6ee96d59605b5273c952bddcdf31b7b93f2bc68020f2f2088c458"
	machine := func(id, name string, used float64) usageSource {
		return usageSource{id: id, name: name, online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{
				AccountKey: accountKey, SourceKey: sourceKey,
				UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now,
					Machines: []string{"what the node says"}, Windows: []proto.UsageWindow{uw("7d", 10080, used, week)}},
			}}}, nil
		}}
	}
	var u usageWindowsHub
	u.gateways = func(time.Time) []proto.UsageAccount {
		return []proto.UsageAccount{{Provider: "claude", Name: "seat", SampledAt: now, Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 1, week)}}}
	}
	got := u.poll(context.Background(), []usageSource{machine("m2", "Studio", 10), machine("m1", "", 20)}, now)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), accountKey) || strings.Contains(string(body), sourceKey) || strings.Contains(string(body), `"account_key"`) || strings.Contains(string(body), `"source_key"`) {
		t.Errorf("the usage response exposed an internal key: %s", body)
	}
	for _, a := range got {
		switch a.Provider {
		case "codex": // one account on two computers; one without a name goes by its id
			if !a.Local || !slices.Equal(a.Machines, []string{"Studio", "m1"}) {
				t.Errorf("codex: %+v", a)
			}
		case "claude":
			if a.Local || a.Machines != nil {
				t.Errorf("a gateway-only account names no computer: %+v", a)
			}
		}
	}
	got = u.poll(context.Background(), []usageSource{machine("m2", "Mac Studio", 10), machine("m1", "", 20)}, now.Add(usageHubFresh+time.Second))
	for _, a := range got {
		if a.Provider == "codex" && !slices.Equal(a.Machines, []string{"Mac Studio", "m1"}) {
			t.Errorf("renamed: %+v", a)
		}
	}
}

// Every account in the answer has its own id (#1904): the clients use it as a row key, and same-name numbering follows
// it. Two accounts of one gateway, and accounts with no reset time, used to share one.
func TestMergeUsageAccountsGivesEveryAccountItsOwnID(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	noReset := func(used float64) []proto.UsageWindow {
		return []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: used}}
	}
	same := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		// two seats of one gateway without a reset time, and two with the same one
		{Provider: "codex", Name: "Seat A", SampledAt: now, Ref: "s1:a", Origin: "s1:a", Sources: []string{"Relay"}, Windows: noReset(10)},
		{Provider: "codex", Name: "Seat B", SampledAt: now, Ref: "s1:b", Origin: "s1:b", Sources: []string{"Relay"}, Windows: noReset(20)},
		{Provider: "codex", Name: "Seat C", SampledAt: now, Ref: "s1:c", Origin: "s1:c", Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 30, same)}},
		{Provider: "codex", Name: "Seat D", SampledAt: now, Ref: "s1:d", Origin: "s1:d", Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 40, same)}},
		// a gateway account without an id of its own
		{Provider: "codex", Name: "Pooled", SampledAt: now, Origin: "s2#0", Sources: []string{"Pool"}, Windows: noReset(50)},
		// two computers, each signed in to a different Codex account, neither with a reset time; and two Codex homes on one
		{Provider: "codex", Name: "Codex Pro", SampledAt: now, Local: true, Origin: "mac/0", Windows: noReset(60)},
		{Provider: "codex", Name: "Codex Pro", SampledAt: now, Local: true, Origin: "linux/0", Windows: noReset(70)},
		{Provider: "codex", Name: "Codex Pro", SampledAt: now, Local: true, Origin: "linux/1", Windows: noReset(80)},
		// only per-model windows that reset together: nothing but the suffix tells them apart
		{Provider: "claude", Name: "Fable seat", SampledAt: now, Local: true, Origin: "mac/1", Windows: []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: 5, ResetsAt: &same, Model: "Fable"}}},
		{Provider: "claude", Name: "Fable seat", SampledAt: now, Local: true, Origin: "mac/2", Windows: []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: 6, ResetsAt: &same, Model: "Fable"}}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != len(in) {
		t.Fatalf("accounts merged that are not one: %d of %d: %+v", len(got), len(in), got)
	}
	ids := map[string]string{}
	for _, a := range got {
		if prev, dup := ids[a.ID]; dup {
			t.Errorf("%q and %q share the id %s", prev, a.Name, a.ID)
		}
		ids[a.ID] = a.Name
	}

	// The same answer in another order (the machines come from a map) gives the same ids and the same names to the same accounts.
	byOrigin := func(out []proto.UsageAccount) map[string]string {
		m := map[string]string{}
		for _, a := range out {
			m[a.Origin] = a.ID + " " + a.Name
		}
		return m
	}
	want := byOrigin(got)
	for range 20 {
		shuffled := slices.Clone(in)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if again := byOrigin(mergeUsageAccounts(shuffled, now)); !maps.Equal(again, want) {
			t.Fatalf("order changed ids or names:\n%v\n%v", again, want)
		}
	}

	// and the numbers moving does not move the id
	later := slices.Clone(in)
	for i := range later {
		later[i].Windows = slices.Clone(later[i].Windows)
		later[i].Windows[0].UsedPercent += 1
		later[i].SampledAt = now.Add(time.Minute)
	}
	if again := byOrigin(mergeUsageAccounts(later, now.Add(time.Minute))); !maps.Equal(again, want) {
		t.Errorf("new numbers changed ids:\n%v\n%v", again, want)
	}
}

func TestUsagePollKeepsCodexSamplesWithoutMatchingKeysApart(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	machine := func(id, accountKey, sourceKey string) usageSource {
		return usageSource{id: id, online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{
				AccountKey: accountKey, SourceKey: sourceKey,
				UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)}},
			}}}, nil
		}}
	}
	var u usageWindowsHub
	u.gateways = func(time.Time) []proto.UsageAccount {
		return []proto.UsageAccount{{Provider: "codex", Name: "Codex Pro", Ref: "s1:seat", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)}}}
	}
	sources := []usageSource{machine("mac", "a", ""), machine("linux", "b", ""), machine("old-node", "", ""), machine("new-node", "", "home")}
	got := u.poll(context.Background(), sources, now)
	if len(got) != 5 {
		t.Fatalf("got %+v", got)
	}
	ids := map[string]string{}
	for _, a := range got {
		source := a.Machines
		if len(source) == 0 {
			source = a.Sources
		}
		if len(source) != 1 {
			t.Fatalf("sources merged: %+v", a)
		}
		ids[source[0]] = a.ID
		if a.CodexAccountKey == "" && a.Name != "Codex Pro" {
			t.Errorf("an unidentified row was named as another account: %+v", a)
		}
	}
	week = week.Add(7 * 24 * time.Hour)
	got = u.poll(context.Background(), sources, now.Add(usageHubFresh+time.Second))
	if len(got) != len(ids) {
		t.Fatalf("new quota cycle changed the row count: %+v", got)
	}
	for _, a := range got {
		source := append(slices.Clone(a.Machines), a.Sources...)
		if len(source) != 1 || ids[source[0]] != a.ID {
			t.Errorf("new quota cycle changed the source's row ID: %+v", a)
		}
	}
}

func TestUsagePollDistinguishesUnidentifiedCodexHomesOnOneMachine(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	sample := func(key string, used float64) proto.UsageNodeAccount {
		return proto.UsageNodeAccount{
			SourceKey:    key,
			UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, used, now.Add(time.Hour))}},
		}
	}
	accounts := []proto.UsageNodeAccount{sample("home-a", 10), sample("home-b", 20)}
	source := usageSource{id: "mac", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		return proto.UsageSnapshot{Enabled: true, Accounts: accounts}, nil
	}}
	type row struct{ id, name string }
	rows := func(accounts []proto.UsageAccount) map[float64]row {
		result := map[float64]row{}
		for _, account := range accounts {
			result[mostUsed(account)] = row{account.ID, account.Name}
		}
		return result
	}
	var u usageWindowsHub
	first := rows(u.poll(context.Background(), []usageSource{source}, now))
	names := []string{first[10].name, first[20].name}
	slices.Sort(names)
	if len(first) != 2 || first[10].id == first[20].id || !slices.Equal(names, []string{"Codex Pro", "Codex Pro 2"}) {
		t.Fatalf("two same-name homes on one machine need distinct IDs and names: %v", first)
	}
	slices.Reverse(accounts)
	after := rows(u.poll(context.Background(), []usageSource{source}, now.Add(usageHubFresh)))
	if !maps.Equal(first, after) {
		t.Errorf("changing the source order changed the row IDs or names: %v to %v", first, after)
	}
}
