package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// replyPolicyClassifierModel serves dynamic JSON responses to inbox.triage classifier calls.
// Zero sleeps: timeouts block on r.Context().Done() so tests remain deterministic and fast.
func replyPolicyClassifierModel(t *testing.T, fn func(call int, system, user string, r *http.Request) (int, string)) (*ai.Client, func() int) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		user := ""
		if len(in.Messages) > 0 {
			user = in.Messages[0].Content
		}
		n := int(calls.Add(1))
		status, reply := fn(n, in.System, user, r)
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": reply}},
		})
	}))
	t.Cleanup(srv.Close)
	return modelAt(t, srv.URL), func() int { return int(calls.Load()) }
}

func replyPolicyTestItem(id, last, text string) *InboxItem {
	now := time.Now().UTC()
	return &InboxItem{
		ID:        id,
		Source:    "lark",
		Kind:      KindDM,
		Account:   "feishu",
		Title:     "Example contact",
		Sender:    "Example contact",
		Preview:   text,
		Last:      last,
		Count:     1,
		StartedAt: now.Add(-time.Minute),
		UpdatedAt: now,
		Tier:      TierAction,
		TriagedAt: last,
		Summary:   "Example request",
		Context:   "Example contact: " + text,
		Lark:      &InboxLark{AccountID: 1, FeedID: "feed-" + id, StartPosition: last},
	}
}

func newReplyPolicyHub(t *testing.T, cfg assistantwire.InboxReplyPolicyConfig, model *ai.Client) (*inboxHub, *extensionLark) {
	t.Helper()
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) {
		c.Agent.Enabled = true
		c.ReplyPolicy = cfg
	})
	lark := &extensionLark{}
	h.addConnector(lark, true)
	if model != nil {
		h.in.models[purposeTriage] = model
	}
	return h, lark
}

func seedHandledReplyPolicyItem(t *testing.T, h *inboxHub, it *InboxItem) {
	t.Helper()
	now := time.Now().UTC()
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	it.Agent = &InboxAgent{
		State:          AgentRunning,
		MachineID:      "server",
		SessionID:      "s1",
		Of:             it.Last,
		StartedAt:      &now,
		ProcessingTurn: "turn-1",
	}
	h.inbox.setHandling(it, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusIdle})
	h.saveInbox(context.Background(), []*InboxItem{it})
}

func suggestReplyWithTurn(t *testing.T, h *inboxHub, turn, text string) (string, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"kind":            SuggestReply,
		"text":            text,
		"processing_turn": turn,
	})
	return h.toolInboxSuggest(context.Background(), "s1", args)
}

func TestInboxReplyPolicyDisabledAndEmptyPreservesBehavior(t *testing.T) {
	cases := []struct {
		name string
		cfg  assistantwire.InboxReplyPolicyConfig
	}{
		{name: "empty", cfg: assistantwire.InboxReplyPolicyConfig{}},
		{name: "disabled_with_categories", cfg: assistantwire.InboxReplyPolicyConfig{
			Enabled:    false,
			Revision:   "rev-off",
			Categories: map[string]string{ReplyCategoryApprovalCommitment: assistantwire.ReplyRouteOwnerOnly},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, callCount := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
				t.Error("classifier must not be called when reply_policy is disabled or empty")
				return http.StatusInternalServerError, "unexpected call"
			})
			h, lark := newReplyPolicyHub(t, tc.cfg, model)
			it := replyPolicyTestItem("lark:off:1", "10", "Can you share the status update?")
			h.inbox.mu.Lock()
			h.inbox.items[it.ID] = it
			h.inbox.mu.Unlock()

			// 1. Queue path works unchanged without calling classifier.
			h.handPass(context.Background())
			if h.f.made != 1 {
				t.Fatalf("expected 1 started conversation, got %d", h.f.made)
			}
			h.inbox.mu.Lock()
			turn := it.Agent.ProcessingTurn
			sessionID := it.Agent.SessionID
			h.inbox.mu.Unlock()
			if sessionID == "" {
				t.Fatal("expected handling conversation to start when policy is disabled")
			}

			// 2. Suggestion path works unchanged without policy snapshot.
			args, _ := json.Marshal(map[string]any{
				"kind":            SuggestReply,
				"text":            "On track for this afternoon.",
				"processing_turn": turn,
			})
			if _, err := h.toolInboxSuggest(context.Background(), sessionID, args); err != nil {
				t.Fatalf("inbox_suggest failed while policy disabled: %v", err)
			}
			h.inbox.mu.Lock()
			g := it.Suggestion
			h.inbox.mu.Unlock()
			if g == nil || g.ReplyPolicy != nil || it.ReplyPolicy != nil {
				t.Fatalf("disabled policy must leave reply policy snapshots nil: item=%+v suggestion=%+v", it.ReplyPolicy, g)
			}

			// 3. Confirmation sends normally and keeps attempt policy fields empty.
			code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
			if code != http.StatusOK {
				t.Fatalf("confirm failed: %d %v", code, out)
			}
			if len(lark.got) != 1 || lark.got[0].Text != "On track for this afternoon." {
				t.Fatalf("unexpected sent replies: %+v", lark.got)
			}
			h.inbox.mu.Lock()
			attempt := suggestionAttempt(it, 1)
			h.inbox.mu.Unlock()
			if attempt == nil || attempt.Status != suggestionSent {
				t.Fatalf("unexpected attempt: %+v", attempt)
			}
			if attempt.Category != "" || attempt.Route != "" || attempt.ContentVersion != "" || attempt.PolicyRevision != "" {
				t.Fatalf("disabled policy should leave attempt policy fields empty: %+v", attempt)
			}
			if callCount() != 0 {
				t.Fatalf("expected 0 classifier calls when disabled, got %d", callCount())
			}
		})
	}
}

func TestInboxReplyPolicyServerCategoryMappingAndFailClosed(t *testing.T) {
	if got := len(OwnerOnlyReplyPolicyCategories()); got != 4 {
		t.Fatalf("expected 4 owner-only categories, got %d", got)
	}
	if got := len(BotReplyPolicyCategories()); got != 5 {
		t.Fatalf("expected 5 bot categories, got %d", got)
	}

	spec := compileReplyPolicy(assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "v1"})
	if !spec.enabled || !strings.HasPrefix(spec.revision, "v1@") {
		t.Fatalf("unexpected compiled policy: %+v", spec)
	}

	if cat, route, err := spec.resolve(ReplyCategoryRoutineReply); err != nil || cat != ReplyCategoryRoutineReply || route != assistantwire.ReplyRouteOwnerDraft {
		t.Fatalf("routine_reply resolution failed: cat=%q route=%q err=%v", cat, route, err)
	}
	for _, cat := range OwnerOnlyReplyPolicyCategories() {
		if _, route, err := spec.resolve(cat); err != nil || route != assistantwire.ReplyRouteOwnerOnly {
			t.Fatalf("owner-only category %q resolved to route=%q err=%v", cat, route, err)
		}
	}
	for _, cat := range BotReplyPolicyCategories() {
		if _, route, err := spec.resolve(cat); err != nil || route != assistantwire.ReplyRouteBot {
			t.Fatalf("bot category %q resolved to route=%q err=%v", cat, route, err)
		}
	}
	if _, route, err := spec.resolve(ReplyCategoryNoReply); err != nil || route != assistantwire.ReplyRouteNoReply {
		t.Fatalf("no_reply resolution failed: route=%q err=%v", route, err)
	}
	for _, unknown := range []string{"", "   ", "unknown_category", assistantwire.ReplyRouteOwnerDraft} {
		if _, route, err := spec.resolve(unknown); err == nil || route != "" {
			t.Fatalf("unknown category %q must fail closed: route=%q err=%v", unknown, route, err)
		}
	}

	invalidRouteSpec := compileReplyPolicy(assistantwire.InboxReplyPolicyConfig{
		Enabled:    true,
		Categories: map[string]string{"custom_cat": "auto_send"},
	})
	if _, route, err := invalidRouteSpec.resolve("custom_cat"); err == nil || route != "" {
		t.Fatalf("invalid route must fail closed: route=%q err=%v", route, err)
	}

	ambiguousSpec := compileReplyPolicy(assistantwire.InboxReplyPolicyConfig{
		Enabled: true,
		Categories: map[string]string{
			"Routine": assistantwire.ReplyRouteOwnerDraft,
			"routine": assistantwire.ReplyRouteOwnerOnly,
		},
	})
	if _, route, err := ambiguousSpec.resolve("routine"); err == nil || route != "" {
		t.Fatalf("conflicting normalized category keys must fail closed: route=%q err=%v", route, err)
	}

	spec2 := compileReplyPolicy(assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "v2"})
	if spec.revision == spec2.revision {
		t.Fatalf("different revisions must produce different effective revisions: %q", spec.revision)
	}
	specEdited := compileReplyPolicy(assistantwire.InboxReplyPolicyConfig{
		Enabled:    true,
		Revision:   "v1",
		Categories: map[string]string{ReplyCategoryRoutineReply: assistantwire.ReplyRouteOwnerOnly},
	})
	if spec.revision == specEdited.revision {
		t.Fatalf("editing categories must change effective revision even with same explicit revision: %q", spec.revision)
	}
}

// Focused Gate 1 (queue gate) tests: removing the queue gate causes these tests to fail.
func TestInboxReplyPolicyQueueGate(t *testing.T) {
	blockedCategories := make([]string, 0, 11)
	blockedCategories = append(blockedCategories, OwnerOnlyReplyPolicyCategories()...)
	blockedCategories = append(blockedCategories, BotReplyPolicyCategories()...)
	blockedCategories = append(blockedCategories, ReplyCategoryNoReply, "unlisted_mystery_category")

	for _, cat := range blockedCategories {
		t.Run("blocks_"+cat, func(t *testing.T) {
			// Also include a rogue "route":"owner_draft" field to prove the model cannot choose its own route.
			payload, _ := json.Marshal(map[string]any{
				"category": cat,
				"route":    assistantwire.ReplyRouteOwnerDraft,
				"reason":   "classified as " + cat,
			})
			model, calls := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
				return http.StatusOK, string(payload)
			})
			h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "q-v1"}, model)
			it := replyPolicyTestItem("lark:q-"+cat+":1", "1", "Message for "+cat)
			h.inbox.mu.Lock()
			h.inbox.items[it.ID] = it
			h.inbox.mu.Unlock()

			h.handPass(context.Background())

			if h.f.made != 0 {
				t.Fatalf("category %q started %d handling conversations, want 0", cat, h.f.made)
			}
			h.inbox.mu.Lock()
			agent := it.Agent
			handling := it.Handling
			snap := it.ReplyPolicy
			h.inbox.mu.Unlock()
			if agent != nil || handling != nil {
				t.Fatalf("category %q must not queue or handle: agent=%+v handling=%+v", cat, agent, handling)
			}
			if snap == nil || snap.Route == assistantwire.ReplyRouteOwnerDraft {
				t.Fatalf("unexpected snapshot for blocked category %q: %+v", cat, snap)
			}
			if calls() != 1 {
				t.Fatalf("expected 1 classifier call, got %d", calls())
			}
		})
	}

	t.Run("fails_closed_on_invalid_route_error_and_timeout", func(t *testing.T) {
		for _, mode := range []string{"invalid_route", "http_error", "timeout"} {
			t.Run(mode, func(t *testing.T) {
				cfg := assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "q-err"}
				if mode == "invalid_route" {
					cfg.Categories = map[string]string{ReplyCategoryRoutineReply: "invented_route"}
				}
				var model *ai.Client
				switch mode {
				case "invalid_route":
					model, _ = replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
						return http.StatusOK, `{"category":"routine_reply","route":"owner_draft","reason":"try bypass"}`
					})
				case "http_error":
					model, _ = replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
						return http.StatusInternalServerError, `{"error":"upstream failure"}`
					})
				case "timeout":
					cfg.TimeoutSeconds = 1
					model, _ = replyPolicyClassifierModel(t, func(_ int, _, _ string, r *http.Request) (int, string) {
						<-r.Context().Done()
						return http.StatusRequestTimeout, ""
					})
				}
				h, _ := newReplyPolicyHub(t, cfg, model)
				it := replyPolicyTestItem("lark:q-"+mode+":1", "1", "Message for "+mode)
				h.inbox.mu.Lock()
				h.inbox.items[it.ID] = it
				h.inbox.mu.Unlock()

				ctx := context.Background()
				if mode == "timeout" {
					// Use a short deadline on a sub-context? Notice classifyReplyPolicy checks cctx.Err() with ctx.Err()==nil
					// when policy.timeout expires. Keep policy.timeout small via TimeoutSeconds=1 (already set) or override ctx.
					// To avoid waiting 1s in unit tests, cancel via a tight policy timeout below.
				}
				// Execute queue gate directly with a tight timeout for the timeout subcase so test is instant.
				if mode == "timeout" {
					spec := compileReplyPolicy(cfg)
					spec.timeout = 20 * time.Millisecond
					h.gateReplyPolicyQueue(ctx, spec, time.Now().Add(-time.Hour))
					escalated := h.inbox.escalateWithPolicy(time.Now().Add(-time.Hour), time.Now(), spec)
					if len(escalated) != 0 {
						t.Fatalf("timeout mode escalated %d items, want 0", len(escalated))
					}
					if it.ReplyPolicy == nil || !strings.Contains(it.ReplyPolicy.Error, "timed out") {
						t.Fatalf("expected timeout error snapshot, got %+v", it.ReplyPolicy)
					}
					return
				}

				h.handPass(ctx)
				if h.f.made != 0 || it.Agent != nil {
					t.Fatalf("%s must fail closed without starting AI handling: starts=%d agent=%+v", mode, h.f.made, it.Agent)
				}
			})
		}
	})

	t.Run("releases_prequeued_item_when_policy_revision_changes", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"routine_reply","reason":"normal"}`
		})
		h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "v1"}, model)
		it := replyPolicyTestItem("lark:q-stale:1", "1", "Please check this")
		h.inbox.mu.Lock()
		h.inbox.items[it.ID] = it
		specV1 := compileReplyPolicy(h.in.cfg.ReplyPolicy)
		it.ReplyPolicy = &InboxReplyPolicySnapshot{
			Category:       ReplyCategoryRoutineReply,
			Route:          assistantwire.ReplyRouteOwnerDraft,
			ContentVersion: replyPolicyContentVersion(it),
			PolicyRevision: specV1.revision,
		}
		h.inbox.mu.Unlock()
		if ws := h.inbox.escalateWithPolicy(time.Now().Add(-time.Hour), time.Now(), specV1); len(ws) != 1 {
			t.Fatalf("expected item to queue under v1, got %d", len(ws))
		}

		// Policy revision changes before startInboxAgents runs; stale queued item must be released, not started.
		h.setConfig(func(c *assistantwire.InboxConfig) {
			c.ReplyPolicy.Revision = "v2"
		})
		h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
		if h.f.made != 0 {
			t.Fatalf("stale queued item started under changed policy revision: starts=%d", h.f.made)
		}
		h.inbox.mu.Lock()
		state := it.Agent.State
		handlingState := it.Handling.State
		h.inbox.mu.Unlock()
		if state != AgentSkipped || handlingState != HandlingDone {
			t.Fatalf("expected released queue item, got agent=%s handling=%s", state, handlingState)
		}
	})

	t.Run("allows_owner_draft_category", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"routine_reply","reason":"ordinary coordination"}`
		})
		h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "q-ok"}, model)
		it := replyPolicyTestItem("lark:q-ok:1", "1", "Can you share the timeline?")
		h.inbox.mu.Lock()
		h.inbox.items[it.ID] = it
		h.inbox.mu.Unlock()

		h.handPass(context.Background())
		if h.f.made != 1 {
			t.Fatalf("owner_draft category should start 1 conversation, got %d", h.f.made)
		}
		h.inbox.mu.Lock()
		snap := it.ReplyPolicy
		h.inbox.mu.Unlock()
		if snap == nil || snap.Category != ReplyCategoryRoutineReply || snap.Route != assistantwire.ReplyRouteOwnerDraft {
			t.Fatalf("unexpected reply policy snapshot: %+v", snap)
		}
	})
}

// Focused Gate 2 (suggestion gate) tests: starts from an already-handled item (bypassing Gate 1).
// Removing the suggestion gate causes these tests to fail.
func TestInboxReplyPolicySuggestionGate(t *testing.T) {
	blockedCategories := make([]string, 0, 12)
	blockedCategories = append(blockedCategories, OwnerOnlyReplyPolicyCategories()...)
	blockedCategories = append(blockedCategories, BotReplyPolicyCategories()...)
	blockedCategories = append(blockedCategories, ReplyCategoryNoReply, "totally_unknown_category", assistantwire.ReplyRouteOwnerDraft)

	for _, cat := range blockedCategories {
		t.Run("refuses_"+cat, func(t *testing.T) {
			// Include rogue "route":"owner_draft" to prove the model cannot choose its own route.
			payload, _ := json.Marshal(map[string]any{
				"category": cat,
				"route":    assistantwire.ReplyRouteOwnerDraft,
				"reason":   "classified as " + cat,
			})
			model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
				return http.StatusOK, string(payload)
			})
			h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "s-v1"}, model)
			it := replyPolicyTestItem("lark:s-"+cat+":1", "1", "Incoming message for "+cat)
			seedHandledReplyPolicyItem(t, h, it)

			out, err := suggestReplyWithTurn(t, h, "turn-1", "Draft reply text")
			if err == nil {
				t.Fatalf("category %q must refuse inbox_suggest reply, got %q", cat, out)
			}
			h.inbox.mu.Lock()
			sug := it.Suggestion
			draft := it.Agent.Draft
			seq := it.SuggestSeq
			h.inbox.mu.Unlock()
			if sug != nil || draft != "" || seq != 0 {
				t.Fatalf("refused category %q created a draft: suggestion=%+v draft=%q seq=%d", cat, sug, draft, seq)
			}

			// Legacy inbox_draft tool must also refuse and propagate the error.
			draftArgs, _ := json.Marshal(map[string]any{"text": "Draft reply text", "processing_turn": "turn-1"})
			if _, err := h.runTool(context.Background(), "server", "s1", "inbox_draft", draftArgs); err == nil {
				t.Fatalf("legacy inbox_draft must refuse category %q", cat)
			}
			h.inbox.mu.Lock()
			if it.Suggestion != nil || it.Agent.Draft != "" {
				h.inbox.mu.Unlock()
				t.Fatalf("legacy inbox_draft created a draft for %q", cat)
			}
			h.inbox.mu.Unlock()
		})
	}

	t.Run("refuses_invalid_route_error_timeout_and_stale_revision_mid_call", func(t *testing.T) {
		for _, mode := range []string{"invalid_route", "http_error", "timeout", "stale_revision", "content_changed"} {
			t.Run(mode, func(t *testing.T) {
				cfg := assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "s-v1"}
				if mode == "invalid_route" {
					cfg.Categories = map[string]string{ReplyCategoryRoutineReply: "not_a_real_route"}
				}
				var h *inboxHub
				var it *InboxItem
				model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, r *http.Request) (int, string) {
					switch mode {
					case "http_error":
						return http.StatusBadGateway, `{"error":"down"}`
					case "timeout":
						<-r.Context().Done()
						return http.StatusRequestTimeout, ""
					case "stale_revision":
						h.setConfig(func(c *assistantwire.InboxConfig) { c.ReplyPolicy.Revision = "s-v2" })
						return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
					case "content_changed":
						h.inbox.mu.Lock()
						it.Last = "2"
						it.Context = "Updated urgent approval request"
						h.inbox.mu.Unlock()
						return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
					default:
						return http.StatusOK, `{"category":"routine_reply","route":"owner_draft","reason":"ok"}`
					}
				})
				h, _ = newReplyPolicyHub(t, cfg, model)
				it = replyPolicyTestItem("lark:s-"+mode+":1", "1", "Initial message")
				seedHandledReplyPolicyItem(t, h, it)

				if mode == "timeout" {
					// Bound the caller context so timeout completes immediately without sleeping.
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer cancel()
					args, _ := json.Marshal(map[string]any{"kind": SuggestReply, "text": "Draft", "processing_turn": "turn-1"})
					if _, err := h.toolInboxSuggest(ctx, "s1", args); err == nil {
						t.Fatal("timeout must refuse reply suggestion")
					}
				} else {
					if _, err := suggestReplyWithTurn(t, h, "turn-1", "Draft"); err == nil {
						t.Fatalf("%s must refuse reply suggestion", mode)
					}
				}
				h.inbox.mu.Lock()
				sug := it.Suggestion
				h.inbox.mu.Unlock()
				if sug != nil {
					t.Fatalf("%s created suggestion: %+v", mode, sug)
				}
			})
		}
	})

	t.Run("refuses_handed_off_conversation_reply_draft", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"sensitive_personal","reason":"personal matter"}`
		})
		h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "h-v1"}, model)
		it := replyPolicyTestItem("lark:s-handoff:1", "1", "Private personal question")
		h.inbox.mu.Lock()
		h.inbox.items[it.ID] = it
		it.Handoffs = []InboxHandoff{{MachineID: "server", SessionID: "main-1", At: time.Now().UTC(), Op: "op-1"}}
		h.inbox.mu.Unlock()

		args, _ := json.Marshal(map[string]any{"item": it.ID, "kind": SuggestReply, "text": "Drafted reply"})
		if _, err := h.toolInboxSuggest(context.Background(), "main-1", args); err == nil {
			t.Fatal("handed-off conversation must not create an owner reply suggestion for owner_only category")
		}
		h.inbox.mu.Lock()
		sug := it.Suggestion
		h.inbox.mu.Unlock()
		if sug != nil {
			t.Fatalf("unexpected suggestion created from handoff: %+v", sug)
		}
	})

	t.Run("allows_owner_draft_and_snapshots_metadata", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"routine_reply","reason":"ordinary update"}`
		})
		cfg := assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "s-ok"}
		h, _ := newReplyPolicyHub(t, cfg, model)
		it := replyPolicyTestItem("lark:s-ok:1", "1", "When is the sync?")
		seedHandledReplyPolicyItem(t, h, it)

		if _, err := suggestReplyWithTurn(t, h, "turn-1", "Sync is at 3pm."); err != nil {
			t.Fatalf("owner_draft suggestion should succeed: %v", err)
		}
		h.inbox.mu.Lock()
		g := it.Suggestion
		cv := replyPolicyContentVersion(it)
		h.inbox.mu.Unlock()
		wantRev := compileReplyPolicy(cfg).revision
		if g == nil || g.ReplyPolicy == nil {
			t.Fatalf("expected suggestion with ReplyPolicy snapshot, got %+v", g)
		}
		if g.ReplyPolicy.Category != ReplyCategoryRoutineReply ||
			g.ReplyPolicy.Route != assistantwire.ReplyRouteOwnerDraft ||
			g.ReplyPolicy.ContentVersion != cv ||
			g.ReplyPolicy.PolicyRevision != wantRev {
			t.Fatalf("unexpected suggestion policy snapshot: %+v (want cv=%s rev=%s)", g.ReplyPolicy, cv, wantRev)
		}
	})
}

// Focused Gate 3 (send gate) tests: starts from an already-accepted reply suggestion (bypassing Gate 1 & Gate 2).
// Removing the send gate causes these tests to fail.
func TestInboxReplyPolicySendGate(t *testing.T) {
	setupAcceptedSuggestion := func(t *testing.T, cfg assistantwire.InboxReplyPolicyConfig, model *ai.Client, withSnapshot bool) (*inboxHub, *extensionLark, *InboxItem) {
		t.Helper()
		h, lark := newReplyPolicyHub(t, cfg, model)
		it := replyPolicyTestItem("lark:send:1", "1", "Can you share the status?")
		seedHandledReplyPolicyItem(t, h, it)
		now := time.Now().UTC()
		spec := compileReplyPolicy(cfg)
		h.inbox.mu.Lock()
		cv := replyPolicyContentVersion(it)
		g := &InboxSuggestion{
			Version:  1,
			Kind:     SuggestReply,
			Text:     "I will check and follow up.",
			At:       now,
			Owner:    &SessionRef{MachineID: "server", SessionID: "s1"},
			SourceOf: suggestionReplyTarget(it),
		}
		if withSnapshot {
			g.ReplyPolicy = &InboxReplyPolicySnapshot{
				Category:       ReplyCategoryRoutineReply,
				Route:          assistantwire.ReplyRouteOwnerDraft,
				ContentVersion: cv,
				PolicyRevision: spec.revision,
				At:             now,
			}
			it.ReplyPolicy = cloneReplyPolicySnapshot(g.ReplyPolicy)
		}
		it.SuggestSeq = 1
		it.Suggestion = g
		h.inbox.setHandling(it, HandlingDone, "", now)
		h.inbox.mu.Unlock()
		h.saveInbox(context.Background(), []*InboxItem{it})
		return h, lark, it
	}

	t.Run("final_text_veto_on_edited_text_zero_sender_calls", func(t *testing.T) {
		var sawEditedText atomic.Bool
		model, calls := replyPolicyClassifierModel(t, func(_ int, _, user string, _ *http.Request) (int, string) {
			if strings.Contains(user, "Approved for full budget sign-off") {
				sawEditedText.Store(true)
			}
			return http.StatusOK, `{"category":"approval_commitment","route":"owner_draft","reason":"budget approval in final text"}`
		})
		cfg := assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "send-v1"}
		h, lark, it := setupAcceptedSuggestion(t, cfg, model, true)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Approved for full budget sign-off."}`)
		if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked || out["delivered"] != "none" || out["retryable"] != false {
			t.Fatalf("expected 409 reply policy veto, got %d %v", code, out)
		}
		if len(lark.got) != 0 {
			t.Fatalf("final-text veto must make zero sender calls, got %d (%+v)", len(lark.got), lark.got)
		}
		if calls() != 1 || !sawEditedText.Load() {
			t.Fatalf("expected final-text classifier to inspect edited text once: calls=%d saw=%v", calls(), sawEditedText.Load())
		}
		h.inbox.mu.Lock()
		a := suggestionAttempt(it, 1)
		h.inbox.mu.Unlock()
		if a == nil || a.Status != suggestionFailed || a.Code != assistantwire.CodeInboxReplyPolicyBlocked || a.Retryable {
			t.Fatalf("unexpected vetoed attempt: %+v", a)
		}
		if a.Proposal == nil || a.Proposal.Text != "I will check and follow up." || a.Text != "Approved for full budget sign-off." {
			t.Fatalf("vetoed attempt did not retain proposal and actual text: %+v", a)
		}
		if a.Category != ReplyCategoryApprovalCommitment || a.Route != assistantwire.ReplyRouteOwnerOnly || a.PolicyRevision != compileReplyPolicy(cfg).revision {
			t.Fatalf("vetoed attempt missing policy metadata: %+v", a)
		}
	})

	t.Run("final_text_veto_on_all_disallowed_outcomes_zero_sender_calls", func(t *testing.T) {
		outcomes := []struct {
			name   string
			status int
			body   string
		}{
			{"owner_only", http.StatusOK, `{"category":"external_public","reason":"public statement"}`},
			{"bot", http.StatusOK, `{"category":"system_alert","reason":"alert"}`},
			{"no_reply", http.StatusOK, `{"category":"no_reply","reason":"no reply needed"}`},
			{"unknown_category", http.StatusOK, `{"category":"unknown_x","route":"owner_draft","reason":"unknown"}`},
			{"classifier_error", http.StatusInternalServerError, `{"error":"boom"}`},
		}
		for _, tc := range outcomes {
			t.Run(tc.name, func(t *testing.T) {
				model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
					return tc.status, tc.body
				})
				h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "send-v1"}, model, true)
				code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
				if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked {
					t.Fatalf("%s: expected 409 blocked, got %d %v", tc.name, code, out)
				}
				if len(lark.got) != 0 {
					t.Fatalf("%s: expected 0 sender calls, got %d", tc.name, len(lark.got))
				}
			})
		}
	})

	t.Run("final_text_veto_timeout_zero_sender_calls", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, r *http.Request) (int, string) {
			<-r.Context().Done()
			return http.StatusRequestTimeout, ""
		})
		h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "send-v1"}, model, true)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := h.actOnSuggestion(ctx, it.ID, suggestionReq{Version: 1, Action: suggestConfirm}, true); err == nil {
			t.Fatal("timeout during final-text veto must fail closed")
		}
		if len(lark.got) != 0 {
			t.Fatalf("timeout veto must make zero sender calls, got %d", len(lark.got))
		}
	})

	t.Run("blocks_stale_policy_revision_with_zero_sender_calls", func(t *testing.T) {
		model, calls := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
		})
		h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "rev-1"}, model, true)
		// Bump policy revision after suggestion was created.
		h.setConfig(func(c *assistantwire.InboxConfig) {
			c.ReplyPolicy.Revision = "rev-2"
		})

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked {
			t.Fatalf("stale policy revision: got %d %v", code, out)
		}
		if len(lark.got) != 0 {
			t.Fatalf("stale policy revision must make zero sender calls, got %d", len(lark.got))
		}
		if calls() != 0 {
			t.Fatalf("stale revision should fail closed before calling classifier, got %d calls", calls())
		}
	})

	t.Run("blocks_stale_content_version_with_zero_sender_calls", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
		})
		h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "rev-1"}, model, true)
		// Mutate item text while keeping SourceOf unchanged (e.g. context edit or body fetch update).
		h.inbox.mu.Lock()
		it.Context = "Example contact: Actually, please approve the $500k wire transfer."
		h.inbox.mu.Unlock()

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked {
			t.Fatalf("stale content version: got %d %v", code, out)
		}
		if len(lark.got) != 0 {
			t.Fatalf("stale content version must make zero sender calls, got %d", len(lark.got))
		}
	})

	t.Run("blocks_missing_snapshot_with_zero_sender_calls", func(t *testing.T) {
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
		})
		h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "rev-1"}, model, false)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked {
			t.Fatalf("missing snapshot: got %d %v", code, out)
		}
		if len(lark.got) != 0 {
			t.Fatalf("missing snapshot must make zero sender calls, got %d", len(lark.got))
		}
	})

	t.Run("blocks_category_remapped_or_invalid_route_with_zero_sender_calls", func(t *testing.T) {
		for _, newRoute := range []string{assistantwire.ReplyRouteOwnerOnly, "bogus_route"} {
			t.Run(newRoute, func(t *testing.T) {
				model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
					return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
				})
				h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "rev-1"}, model, true)
				h.setConfig(func(c *assistantwire.InboxConfig) {
					c.ReplyPolicy.Categories = map[string]string{ReplyCategoryRoutineReply: newRoute}
				})

				code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
				if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked {
					t.Fatalf("remapped route %q: got %d %v", newRoute, code, out)
				}
				if len(lark.got) != 0 {
					t.Fatalf("remapped route %q must make zero sender calls, got %d", newRoute, len(lark.got))
				}
			})
		}
	})

	t.Run("blocks_policy_revision_change_during_final_text_check_with_zero_sender_calls", func(t *testing.T) {
		var h *inboxHub
		var once sync.Once
		model, _ := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			once.Do(func() {
				h.setConfig(func(c *assistantwire.InboxConfig) { c.ReplyPolicy.Revision = "rev-2" })
			})
			return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
		})
		var lark *extensionLark
		var it *InboxItem
		h, lark, it = setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "rev-1"}, model, true)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != http.StatusConflict || out["code"] != assistantwire.CodeInboxReplyPolicyBlocked {
			t.Fatalf("mid-check revision change: got %d %v", code, out)
		}
		if len(lark.got) != 0 {
			t.Fatalf("mid-check revision change must make zero sender calls, got %d", len(lark.got))
		}
	})

	t.Run("blocks_stale_attempt_at_pre_dispatch_send_boundary_with_zero_sender_calls", func(t *testing.T) {
		h, lark, it := setupAcceptedSuggestion(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "rev-1"}, nil, true)
		h.inbox.mu.Lock()
		g := it.Suggestion
		a := &InboxSuggestionAttempt{
			Version:        g.Version,
			Status:         suggestionSending,
			Text:           g.Text,
			ClientID:       namedClientID("pre-dispatch-check"),
			Proposal:       copySuggestion(g),
			Target:         suggestionReplyTarget(it),
			Category:       g.ReplyPolicy.Category,
			Route:          g.ReplyPolicy.Route,
			ContentVersion: g.ReplyPolicy.ContentVersion,
			PolicyRevision: g.ReplyPolicy.PolicyRevision,
			active:         true,
		}
		keepSuggestionAttempt(it, a)
		h.inbox.mu.Unlock()
		// Policy revision advances right before replyInboxDoMode dispatches to the sender.
		h.setConfig(func(c *assistantwire.InboxConfig) { c.ReplyPolicy.Revision = "rev-2" })

		sendCtx := context.WithValue(context.Background(), suggestionReplyOnly{}, a)
		if _, err := h.replyInboxDo(sendCtx, it.ID, a.Text, a.ClientID, false, false); err == nil {
			t.Fatal("stale attempt at pre-dispatch send boundary must fail closed")
		}
		if len(lark.got) != 0 {
			t.Fatalf("pre-dispatch send gate must make zero sender calls, got %d", len(lark.got))
		}
	})
}

// End-to-end test for a normal owner-draft category:
// Gate 1 -> Gate 2 -> independent owner confirmation (with edit) -> Gate 3 -> sender sends once ->
// persisted InboxSuggestionAttempt stores proposal, actual text, category, route, content version, and policy revision.
func TestInboxReplyPolicyOwnerDraftConfirmationStoresAuditSnapshot(t *testing.T) {
	model, calls := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
		return http.StatusOK, `{"category":"routine_reply","reason":"ordinary coordination"}`
	})
	cfg := assistantwire.InboxReplyPolicyConfig{
		Enabled:  true,
		Revision: "policy-2026-11",
	}
	wantRev := compileReplyPolicy(cfg).revision
	h, lark := newReplyPolicyHub(t, cfg, model)
	it := replyPolicyTestItem("lark:ok:1", "42", "Could you confirm the meeting room for tomorrow?")
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	wantCV := replyPolicyContentVersion(it)
	h.inbox.mu.Unlock()

	// Gate 1: queue and start handling conversation.
	h.handPass(context.Background())
	if h.f.made != 1 {
		t.Fatalf("expected 1 started conversation, got %d", h.f.made)
	}
	if len(lark.got) != 0 {
		t.Fatal("queueing must never send an outward reply")
	}

	// Gate 2: accept reply suggestion.
	h.inbox.mu.Lock()
	sessionID := it.Agent.SessionID
	turn := it.Agent.ProcessingTurn
	h.inbox.mu.Unlock()
	args, _ := json.Marshal(map[string]any{
		"kind":            SuggestReply,
		"text":            "Proposed: Room B at 10am.",
		"processing_turn": turn,
	})
	if _, err := h.toolInboxSuggest(context.Background(), sessionID, args); err != nil {
		t.Fatalf("owner_draft suggestion failed: %v", err)
	}
	if len(lark.got) != 0 {
		t.Fatal("accepting a suggestion must never send without independent owner confirmation")
	}

	// Gate 3: independent owner confirmation with edited text.
	code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Confirmed: Room C at 10:30am."}`)
	if code != http.StatusOK {
		t.Fatalf("owner confirmation failed: %d %v", code, out)
	}
	if len(lark.got) != 1 || lark.got[0].Text != "Confirmed: Room C at 10:30am." {
		t.Fatalf("expected 1 sent reply with edited text, got %+v", lark.got)
	}
	if calls() != 3 {
		t.Fatalf("expected 3 classifier calls (queue, suggestion, send final-text), got %d", calls())
	}

	// Verify listed API output omits internal policy bindings.
	listedAttempts, _ := out["suggestion_attempts"].([]any)
	if len(listedAttempts) != 1 {
		t.Fatalf("expected 1 listed attempt, got %v", out["suggestion_attempts"])
	}
	listedMap, _ := listedAttempts[0].(map[string]any)
	for _, hidden := range []string{"proposal", "category", "route", "content_version", "policy_revision"} {
		if _, present := listedMap[hidden]; present {
			t.Fatalf("listed suggestion_attempts must omit internal field %q: %v", hidden, listedMap)
		}
	}

	// Verify stored audit record survives reload from storage with proposal, actual text, category, route, content version, and policy revision.
	reloaded := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	if err := reloaded.inboxReady(context.Background()); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	reloaded.inbox.mu.Lock()
	rit := reloaded.inbox.items[it.ID]
	var attempt *InboxSuggestionAttempt
	if rit != nil {
		attempt = suggestionAttempt(rit, 1)
	}
	reloaded.inbox.mu.Unlock()
	if attempt == nil {
		t.Fatal("reloaded item missing suggestion attempt")
	}
	if attempt.Status != suggestionSent {
		t.Fatalf("attempt status = %q, want %q", attempt.Status, suggestionSent)
	}
	if attempt.Proposal == nil || attempt.Proposal.Text != "Proposed: Room B at 10am." {
		t.Fatalf("stored proposal mismatch: %+v", attempt.Proposal)
	}
	if attempt.Text != "Confirmed: Room C at 10:30am." {
		t.Fatalf("stored actual text = %q, want edited text", attempt.Text)
	}
	if attempt.Category != ReplyCategoryRoutineReply ||
		attempt.Route != assistantwire.ReplyRouteOwnerDraft ||
		attempt.ContentVersion != wantCV ||
		attempt.PolicyRevision != wantRev {
		t.Fatalf("stored audit snapshot mismatch: cat=%q route=%q cv=%q rev=%q (want cv=%q rev=%q)",
			attempt.Category, attempt.Route, attempt.ContentVersion, attempt.PolicyRevision, wantCV, wantRev)
	}

	// Direct manual owner reply via /reply remains unaffected for owner_only items (the owner can still reply themselves).
	ownerOnlyItem := replyPolicyTestItem("lark:owner-direct:1", "9", "Please approve this contract")
	h.inbox.mu.Lock()
	h.inbox.items[ownerOnlyItem.ID] = ownerOnlyItem
	h.inbox.mu.Unlock()
	if _, err := h.replyInboxFrom(context.Background(), ownerOnlyItem.ID, "Owner direct reply", "direct-1", false); err != nil {
		t.Fatalf("direct owner reply must remain available: %v", err)
	}
	if len(lark.got) != 2 || lark.got[1].Text != "Owner direct reply" {
		t.Fatalf("expected direct owner reply to send, got %+v", lark.got)
	}
	_ = connector.TagChat
}

func TestInboxReplyPolicyDraftAndRetryGates(t *testing.T) {
	t.Run("draftReply_vetoes_generated_draft_text", func(t *testing.T) {
		classifier, classifierCalls := replyPolicyClassifierModel(t, func(call int, _, user string, _ *http.Request) (int, string) {
			if call == 1 {
				return http.StatusOK, `{"category":"routine_reply","reason":"initial question"}`
			}
			if strings.Contains(user, "Approved for immediate payout") {
				return http.StatusOK, `{"category":"approval_commitment","reason":"generated text approves payout"}`
			}
			return http.StatusOK, `{"category":"routine_reply","reason":"ok"}`
		})
		draftModel, draftCalls := replyPolicyClassifierModel(t, func(_ int, _, _ string, _ *http.Request) (int, string) {
			return http.StatusOK, "Approved for immediate payout."
		})
		h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "draft-v1"}, classifier)
		h.in.models[purposeDraft] = draftModel
		it := replyPolicyTestItem("lark:draft-veto:1", "1", "Can we proceed?")
		h.inbox.mu.Lock()
		h.inbox.items[it.ID] = it
		h.inbox.mu.Unlock()

		if got, err := h.draftReply(context.Background(), it.ID, ""); err == nil || got != "" {
			t.Fatalf("expected generated draft text veto, got text=%q err=%v", got, err)
		}
		if draftCalls() != 1 || classifierCalls() != 2 {
			t.Fatalf("expected 1 draft call and 2 classifier calls (pre + generated text), got draft=%d classifier=%d", draftCalls(), classifierCalls())
		}
	})

	t.Run("retryInboxHandling_blocks_disallowed_item", func(t *testing.T) {
		h, _ := newReplyPolicyHub(t, assistantwire.InboxReplyPolicyConfig{Enabled: true, Revision: "retry-v1"}, nil)
		it := replyPolicyTestItem("lark:retry-block:1", "1", "Please approve contract")
		now := time.Now().UTC()
		h.inbox.mu.Lock()
		h.inbox.items[it.ID] = it
		it.Agent = &InboxAgent{State: AgentFailed, Of: it.Last}
		h.inbox.setHandling(it, HandlingFailed, "failed", now)
		h.inbox.mu.Unlock()

		_, status, code, err := h.retryInboxHandling(context.Background(), it.ID)
		if err == nil || status != http.StatusConflict || code != assistantwire.CodeInboxReplyPolicyBlocked {
			t.Fatalf("expected 409 reply_policy_blocked on retry, got status=%d code=%q err=%v", status, code, err)
		}
	})
}
