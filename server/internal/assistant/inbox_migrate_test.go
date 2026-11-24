package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// The things of a plugin that took Feishu over keep the items the hub made itself: the same item goes on (its start
// stays), new ones get the hub's own shape and id, read and replied end them as before, a re-read document's rows
// update the item; replies and status moves go to the plugin with the hub's client id.
func TestMigratedInbox(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	h.inboxReady(ctx)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now()
	old := &InboxItem{ID: "lark:10:3", Source: "lark", Kind: KindDM, Account: "feishu", AccountName: "飞书", Title: "Alice", Last: "3", Count: 1,
		StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), Lark: &InboxLark{AccountID: 1, FeedID: "10", StartPosition: "3"}}
	h.inbox.mu.Lock() // the "Needs you" teller the first read started counts the items under it
	h.inbox.items[old.ID] = old
	h.inbox.mu.Unlock()
	extra := func(block string, v any, legacy string, count int) *connector.ThingExtra {
		x := &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", LegacyID: legacy, Count: count, StartedAt: now}
		switch block {
		case "lark":
			x.Lark = rawJSON(v)
		case "doc":
			x.Doc = rawJSON(v)
		}
		return x
	}
	p.things = []connector.InboxThing{
		{Thread: "chat:10", Kind: KindDM, Title: "Alice", Preview: "还在吗", At: now, Last: "5", CanReply: true,
			Extra: extra("lark", InboxLark{AccountID: 1, FeedID: "10", StartPosition: "4", ReplyTo: "m5"}, "lark:10:4", 2)},
		{Thread: "doc:n1", Kind: KindDoc, Title: "周报", At: now, Last: "100",
			Extra: extra("doc", InboxDoc{NoticeID: "n1", Type: "sheet", Rows: []DocRow{{Ref: "t!1", Title: "行", Status: []DocStatus{{Field: "2", Name: "状态", Value: "进行中",
				Options: []DocOption{{Name: "进行中"}, {Name: "完成"}}}}}}}, "doc:n1:100", 1)},
	}
	h.collectInbox(ctx, roundPoll)
	if len(h.inbox.items) != 2 {
		t.Fatalf("items: %d", len(h.inbox.items))
	}
	if old.Last != "5" || old.Count != 2 || old.Lark.StartPosition != "3" || old.Lark.ReplyTo != "m5" || old.Connector != nil || old.Preview != "还在吗" {
		t.Fatalf("the hub's own item goes on: %+v %+v", old, old.Lark)
	}
	doc := h.inbox.items["doc:n1:100"]
	if doc == nil || doc.Doc == nil || doc.Connector != nil || doc.Account != "feishu" || len(doc.Doc.Rows) != 1 {
		t.Fatalf("a new item in the hub's own shape: %+v", doc)
	}

	// The same move, the document read again: its rows change.
	p.things[1].Extra = extra("doc", InboxDoc{NoticeID: "n1", Type: "sheet", Rows: []DocRow{{Ref: "t!1", Title: "行", Status: []DocStatus{{Field: "2", Name: "状态", Value: "完成",
		Options: []DocOption{{Name: "进行中"}, {Name: "完成"}}}}}}}, "doc:n1:100", 1)
	h.collectInbox(ctx, roundPoll)
	if doc.Doc.Rows[0].Status[0].Value != "完成" {
		t.Fatalf("rows not updated: %+v", doc.Doc.Rows)
	}

	// A reply goes to the plugin with the client id the hub would have used itself; a status move as changes.
	if _, err := h.replyInbox(ctx, "lark:10:3", "在的"); err != nil {
		t.Fatal(err)
	}
	want := namedClientID("lark:10:3" + "\n" + "在的")
	if len(p.got) != 1 || p.got[0].Thread != "chat:10" || p.got[0].ClientID != want || p.got[0].Text != "在的" || old.DoneBy != "replied" {
		t.Fatalf("reply: %+v, item done by %q", p.got, old.DoneBy)
	}
	if _, err := h.setDocStatus(ctx, "doc:n1:100", DocChange{Ref: "t!1", Field: "2", Value: "进行中"}); err != nil {
		t.Fatal(err)
	}
	if len(p.got) != 2 || p.got[1].Thread != "doc:n1" || len(p.got[1].Changes) != 1 || p.got[1].Changes[0].Value != "进行中" || p.got[1].Text != "" {
		t.Fatalf("status move: %+v", p.got)
	}
	if _, err := h.setDocStatus(ctx, "doc:n1:100", DocChange{Ref: "t!1", Field: "2", Value: "没有这个"}); err != errNoDocRow {
		t.Fatalf("a value the column does not offer: %v", err)
	}

	// Read in Feishu: the doc (untriaged) is read; replied: the chat's next item ends.
	p.things[1].Read = true
	h.collectInbox(ctx, roundPoll)
	if !doc.Read {
		t.Fatal("read elsewhere")
	}
}

// An account-qualified document notice keeps one thread across repeated collection. The identical unread version
// cannot recreate the item its owner handled; a later version still starts a new one.
func TestMigratedDocumentDoneStaysDoneForAccount(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	h.inboxReady(ctx)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	docThing := func(last string, at time.Time) connector.InboxThing {
		return connector.InboxThing{Thread: "doc:2:n1", Kind: KindDoc, Title: "Weekly plan", Sender: "A colleague", At: at, Last: last,
			Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "Feishu account 2", LegacyID: "doc:2:n1:" + last,
				Count: 1, StartedAt: at, Doc: rawJSON(map[string]any{"notice_id": "n1", "account_id": 2, "type": "docx"})}}
	}
	collect := func(th connector.InboxThing) {
		p.things = []connector.InboxThing{th}
		h.collectInbox(ctx, roundPoll)
	}
	first := docThing("100", now.Add(-time.Minute))
	collect(first)
	collect(first) // the unread source card is still there when the owner handles the item

	h.inbox.mu.Lock()
	var before []*InboxItem
	for _, it := range h.inbox.items {
		if it.Doc != nil && it.Doc.NoticeID == "n1" {
			before = append(before, it)
		}
	}
	h.inbox.mu.Unlock()
	var handled *InboxItem
	for _, it := range before {
		if strings.HasPrefix(it.ID, "lark:") { // the duplicate id produced by the old account-blind lookup
			handled = it
		}
	}
	if handled == nil && len(before) == 1 {
		handled = before[0]
	}
	if handled == nil {
		t.Fatalf("setup: document items = %+v", before)
	}
	code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+handled.ID+"/done", `{"done":true}`)
	if code != 200 || out["done_by"] != "user" {
		t.Fatalf("mark done: %d %v", code, out)
	}

	collect(first)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		var after []*InboxItem
		for _, it := range h.inbox.items {
			if it.Doc != nil && it.Doc.NoticeID == "n1" {
				after = append(after, it)
			}
		}
		if len(after) != 1 || after[0].ID != handled.ID || after[0].open() {
			t.Fatalf("the unchanged notice reopened or duplicated the handled item: before=%+v after=%+v", before, after)
		}
	}()

	later := docThing("101", now)
	collect(later)
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if len(h.inbox.items) != 2 || h.inbox.items[handled.ID] == nil || h.inbox.items[handled.ID].open() {
		t.Fatalf("the handled version was lost when later activity arrived: %+v", h.inbox.items)
	}
	var fresh *InboxItem
	for _, it := range h.inbox.items {
		if it.open() {
			fresh = it
		}
	}
	if fresh == nil || fresh.Last != "101" || fresh.Doc == nil {
		t.Fatalf("later document activity did not open one account-qualified item: %+v", fresh)
	}
	raw, err := json.Marshal(fresh.Doc)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields["account_id"] != float64(2) {
		t.Fatalf("the new item did not retain its source account: %s (%v)", raw, err)
	}
}

// Rows written by the account-blind version are adopted by their stable legacy id and deterministic fallback id.
// If both are present, the row the owner handled survives and the duplicate open row is filed into it, not orphaned.
func TestMigratedDocumentAccountUpgradeKeepsHandledRow(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	h.inboxReady(ctx)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := now.Add(-time.Minute)
	sum := sha1.Sum([]byte("doc:2:n1\n100"))
	fallbackID := "lark:" + hex.EncodeToString(sum[:8])
	duplicate := &InboxItem{ID: "doc:2:n1:100", Source: "lark", Kind: KindDoc, Title: "Weekly plan", UpdatedAt: at, Last: "100",
		Doc: &InboxDoc{NoticeID: "n1", Type: "docx"}}
	doneAt := now.Add(-time.Second)
	handled := &InboxItem{ID: fallbackID, Source: "lark", Kind: KindDoc, Title: "Weekly plan", UpdatedAt: at, Last: "100",
		DoneAt: &doneAt, DoneBy: "user", Doc: &InboxDoc{NoticeID: "n1", Type: "docx"}}
	h.inbox.mu.Lock()
	h.inbox.items[duplicate.ID], h.inbox.items[handled.ID] = duplicate, handled
	h.inbox.mu.Unlock()
	p.things = []connector.InboxThing{{Thread: "doc:2:n1", Kind: KindDoc, Title: "Weekly plan", At: at, Last: "100",
		Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", LegacyID: duplicate.ID, Count: 1, StartedAt: at,
			Doc: rawJSON(map[string]any{"notice_id": "n1", "account_id": 2, "type": "docx"})}}}
	h.collectInbox(ctx, roundPoll)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if len(h.inbox.items) != 2 || handled.open() || migratedThread(handled) != "doc:2:n1" {
			t.Fatalf("the stored handled row was not adopted by its account-qualified thread: %+v", h.inbox.items)
		}
		if duplicate.open() || duplicate.MergedInto != handled.ID || migratedThread(duplicate) != "doc:2:n1" {
			t.Fatalf("the old duplicate remains open or orphaned: %+v", duplicate)
		}
	}()

	h.collectInbox(ctx, roundPoll)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if len(h.inbox.items) != 2 || handled.open() || duplicate.open() {
			t.Fatalf("the upgraded rows were recreated by the next identical round: %+v", h.inbox.items)
		}
	}()
}

// The first collection after an upgrade may already carry a newer version. Historical rows still need their account
// recovered from each row's own Last, or an old hash row would stay open on the unqualified thread forever.
func TestMigratedDocumentAccountUpgradeMigratesHistory(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	h.inboxReady(ctx)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	at100 := now.Add(-2 * time.Minute)
	sum := sha1.Sum([]byte("doc:2:n1\n100"))
	hashID := "lark:" + hex.EncodeToString(sum[:8])
	doneAt := now.Add(-3 * time.Minute)
	legacy := &InboxItem{ID: "doc:2:n1:100", Source: "lark", Kind: KindDoc, UpdatedAt: at100, Last: "100",
		DoneAt: &doneAt, DoneBy: "user", Doc: &InboxDoc{NoticeID: "n1"}}
	hashOpen := &InboxItem{ID: hashID, Source: "lark", Kind: KindDoc, UpdatedAt: at100, Last: "100", Doc: &InboxDoc{NoticeID: "n1"}}
	h.inbox.mu.Lock()
	h.inbox.items[legacy.ID], h.inbox.items[hashOpen.ID] = legacy, hashOpen
	h.inbox.mu.Unlock()
	p.things = []connector.InboxThing{{Thread: "doc:2:n1", Kind: KindDoc, At: now, Last: "101",
		Extra: &connector.ThingExtra{Source: "lark", LegacyID: "doc:2:n1:101", Count: 1, StartedAt: now,
			Doc: rawJSON(map[string]any{"notice_id": "n1", "account_id": 2})}}}
	h.collectInbox(ctx, roundPoll)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if migratedThread(hashOpen) != "doc:2:n1" || hashOpen.open() || hashOpen.MergedInto != legacy.ID {
			t.Fatalf("the historical duplicate was left behind: %+v", hashOpen)
		}
		if legacy.open() || len(h.inbox.items) != 3 || h.inbox.items["doc:2:n1:101"] == nil || !h.inbox.items["doc:2:n1:101"].open() {
			t.Fatalf("the current version or handled history changed incorrectly: %+v", h.inbox.items)
		}
	}()

	h.collectInbox(ctx, roundPoll)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if len(h.inbox.items) != 3 || legacy.open() || hashOpen.open() {
			t.Fatalf("the historical rows were recreated by the next round: %+v", h.inbox.items)
		}
	}()
}

// Old account-blind writes can leave several versions open at once. Migration keeps the newest activity row and
// files older open rows into it, whether that survivor is open or already handled.
func TestMigratedDocumentAccountUpgradeConvergesOpenHistory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		latestDone bool
	}{{"latest_open", false}, {"latest_done", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newInboxHub(t)
			h.inboxReady(ctx)
			p := &extensionLark{}
			h.addConnector(p, false)
			now := time.Now().UTC().Truncate(time.Millisecond)
			old := &InboxItem{ID: "doc:2:n1:100", Source: "lark", Kind: KindDoc, UpdatedAt: now.Add(-2 * time.Minute), Last: "100", Doc: &InboxDoc{NoticeID: "n1"}}
			current := &InboxItem{ID: "doc:2:n1:101", Source: "lark", Kind: KindDoc, UpdatedAt: now.Add(-time.Minute), Last: "101", Doc: &InboxDoc{NoticeID: "n1"}}
			if tc.latestDone {
				doneAt := now.Add(-time.Second)
				current.DoneAt, current.DoneBy = &doneAt, "user"
			}
			h.inbox.mu.Lock()
			h.inbox.items[old.ID], h.inbox.items[current.ID] = old, current
			h.inbox.mu.Unlock()
			p.things = []connector.InboxThing{{Thread: "doc:2:n1", Kind: KindDoc, At: current.UpdatedAt, Last: "101",
				Extra: &connector.ThingExtra{Source: "lark", LegacyID: current.ID, Count: 1, StartedAt: current.UpdatedAt,
					Doc: rawJSON(map[string]any{"notice_id": "n1", "account_id": 2})}}}
			h.collectInbox(ctx, roundPoll)
			h.inbox.mu.Lock()
			defer h.inbox.mu.Unlock()
			if old.open() || old.MergedInto != current.ID || current.open() == tc.latestDone {
				t.Fatalf("cross-version open rows were not converged: old=%+v current=%+v", old, current)
			}
			if len(h.inbox.items) != 2 {
				t.Fatalf("migration added another row: %+v", h.inbox.items)
			}
		})
	}
}

// An explicit reopen may leave an older account-qualified row open beside the current one. An ordinary unchanged
// collection is not a legacy-account adoption and must not converge that reopened row into the current version.
func TestMigratedDocumentAccountUpgradePreservesExplicitReopen(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	h.inboxReady(ctx)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	doneAt := now.Add(-3 * time.Minute)
	reopened := &InboxItem{ID: "doc:2:n1:100", Source: "lark", Kind: KindDoc, UpdatedAt: now.Add(-2 * time.Minute), Last: "100",
		DoneAt: &doneAt, DoneBy: "user", Doc: &InboxDoc{NoticeID: "n1", AccountID: 2}}
	current := &InboxItem{ID: "doc:2:n1:101", Source: "lark", Kind: KindDoc, UpdatedAt: now.Add(-time.Minute), Last: "101",
		Doc: &InboxDoc{NoticeID: "n1", AccountID: 2}}
	h.inbox.mu.Lock()
	h.inbox.items[reopened.ID], h.inbox.items[current.ID] = reopened, current
	h.inbox.mu.Unlock()
	p.things = []connector.InboxThing{{Thread: "doc:2:n1", Kind: KindDoc, At: current.UpdatedAt, Last: current.Last,
		Extra: &connector.ThingExtra{Source: "lark", LegacyID: current.ID, Count: 1, StartedAt: current.UpdatedAt,
			Doc: rawJSON(map[string]any{"notice_id": "n1", "account_id": 2})}}}

	code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+reopened.ID+"/done", `{"done":false}`)
	if code != 200 || !reopened.open() {
		t.Fatalf("explicit reopen: code=%d out=%v row=%+v", code, out, reopened)
	}
	h.collectInbox(ctx, roundPoll)
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if !reopened.open() || reopened.MergedInto != "" {
		t.Fatalf("ordinary collection filed the explicitly reopened row: %+v", reopened)
	}
	if !current.open() || len(current.MergedFrom) != 0 || len(h.inbox.items) != 2 {
		t.Fatalf("ordinary collection changed the current row or its history: current=%+v items=%+v", current, h.inbox.items)
	}
}

// A document item written before account-qualified blocks keeps its old id and thread. Replaying its version neither
// duplicates nor detaches it; later activity still continues beside the handled row.
func TestMigratedDocumentLegacyIDRemainsAttached(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	h.inboxReady(ctx)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	doneAt := now.Add(-time.Minute)
	old := &InboxItem{ID: "doc:n1:100", Source: "lark", Kind: KindDoc, Account: "feishu", Title: "Weekly plan",
		StartedAt: doneAt, UpdatedAt: doneAt, Last: "100", DoneAt: &doneAt, DoneBy: "user", Doc: &InboxDoc{NoticeID: "n1", Type: "docx"}}
	h.inbox.mu.Lock()
	h.inbox.items[old.ID] = old
	h.inbox.mu.Unlock()
	p.things = []connector.InboxThing{{Thread: "doc:n1", Kind: KindDoc, Title: old.Title, At: old.UpdatedAt, Last: old.Last,
		Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", LegacyID: old.ID, Count: 1, StartedAt: old.StartedAt,
			Doc: rawJSON(InboxDoc{NoticeID: "n1", Type: "docx"})}}}
	h.collectInbox(ctx, roundPoll)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if len(h.inbox.items) != 1 || h.inbox.items[old.ID] != old || old.open() {
			t.Fatalf("the legacy document was duplicated or detached: %+v", h.inbox.items)
		}
	}()

	p.things[0].At, p.things[0].Last = now, "101"
	p.things[0].Extra.LegacyID = "doc:n1:101"
	h.collectInbox(ctx, roundPoll)
	func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		if len(h.inbox.items) != 2 || h.inbox.items[old.ID] != old || old.open() || h.inbox.items["doc:n1:101"] == nil || !h.inbox.items["doc:n1:101"].open() {
			t.Fatalf("later activity did not preserve the legacy row and its continuation: %+v", h.inbox.items)
		}
	}()
}

// A plugin connector's thing with its own fallback (extra.rule_tier, capability fallback) and no tier is still sorted
// by the model; when the model fails, it comes in with that fallback ("By rule") instead of being held back. The
// fallback is provisional: RuleOf records the version to ask the model about again after it answers (#8298).
func TestExtensionThingFallbackTier(t *testing.T) {
	_, client := newTriageModel(t, "fail", `{"tier":"action","summary":"示例","reason":"示例"}`)
	th := genericThing("example-org/example-repo#8", "a", "示例：一条评论")
	th.Extra = &connector.ThingExtra{RuleTier: TierFYI}
	h, st := admitHub(t, client, &inboxConnector{things: []connector.InboxThing{th}})
	collectAndAdmit(h)
	it := itemOf(h, "example", th.Thread)
	if it == nil || it.Tier != TierFYI || it.Reason != "按规则" || it.TriagedAt == it.Last || it.RuleOf != it.Last || !st.saved(it.ID) {
		t.Fatalf("item %+v", it)
	}
	if _, status := inboxTriageOf(t, h); status.Waiting != 0 {
		t.Fatalf("held back: %+v", status)
	}
}

// A migrated connector's thing whose rules decided its tier is filed with it, without the model: the same contract a
// generic connector thing has (InboxThing.Tier), for items that keep the hub's own blocks. A rule changed for the same
// move re-tiers the open item, the next move follows the rules again, and a user's policy for the chat still wins.
func TestMigratedThingRuleTier(t *testing.T) {
	ctx := context.Background()
	h := muteHub(t)
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now()
	larkExtra := func(legacy, start string) *connector.ThingExtra {
		return &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", LegacyID: legacy, Count: 1, StartedAt: now,
			Lark: rawJSON(InboxLark{AccountID: 1, FeedID: "10", StartPosition: start})}
	}
	p.things = []connector.InboxThing{{Thread: "chat:10", Kind: KindDM, Title: "Alice", Preview: "P2 告警", At: now, Last: "5",
		Tier: connector.TierNoise, Why: "Muted in Feishu", Extra: larkExtra("lark:10:5", "5")}}
	h.collectInbox(ctx, roundPoll)
	it := h.inbox.items["lark:10:5"]
	if it == nil || it.Connector != nil || it.Lark == nil {
		t.Fatalf("item: %+v", it)
	}
	if it.Tier != TierNoise || it.Reason != "Muted in Feishu" || it.TriagedAt != it.Last {
		t.Fatalf("the rule tier is not applied: %+v", it)
	}

	// The rules changed since it was tiered (the chat was unmuted): the open item is re-tiered at once.
	p.things[0].Tier, p.things[0].Why = connector.TierAction, "Needs you"
	h.collectInbox(ctx, roundPoll)
	if it.Tier != TierAction || it.Reason != "Needs you" || it.TriagedAt != it.Last {
		t.Fatalf("the rule tier is not updated: %+v", it)
	}

	// The next move follows the rules, and the user's policy for the chat has the last word in the same round.
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "chat", Value: "10", Name: "Alice", Tier: TierFYI})
	p.things[0].At, p.things[0].Last, p.things[0].Extra = now.Add(time.Minute), "6", larkExtra("lark:10:6", "6")
	p.things[0].Tier, p.things[0].Why = connector.TierNoise, "Muted in Feishu"
	h.collectInbox(ctx, roundPoll)
	if it.Tier != TierFYI || it.TriagedAt != it.Last {
		t.Fatalf("the policy does not win: %+v", it)
	}
}
