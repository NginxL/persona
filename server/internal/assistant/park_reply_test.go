package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

const personReplyContactURL = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_example&open_thread_id=omt_example&thread_position=1"

func personReplyParkAt(now time.Time) time.Time {
	return now.UTC().Truncate(time.Second).Add(-5 * time.Minute)
}

// These raw JSON fixtures are shared with the PostgreSQL entrypoint tests. Keep provider observations as maps so
// removing the production wire types still leaves this test compiling and failing at its behavior assertions.
func personReplyParkArgs(now time.Time) map[string]any {
	sent := now.UTC().Truncate(time.Second).Add(-10 * time.Minute)
	return map[string]any{
		"verdict": "parked", "reason": "Waiting for the named reviewer to reply", "waiting_on": "someone_else", "until": "3d",
		"evidence": map[string]any{
			"kind": "person_reply", "next_actor": "external", "target": "ou_peer", "wake": "The named reviewer replies in the topic",
			"contact": map[string]any{"channel": "Feishu topic", "url": personReplyContactURL, "sender": "task supervisor",
				"sent_at": sent.Format(time.RFC3339), "request": "Please reply with the requested decision.", "status": "sent"},
			"reply": map[string]any{"source": "larkcli", "brand": "feishu", "app_id": "cli_example", "account_open_id": "ou_owner",
				"person_open_id": "ou_peer", "chat_id": "oc_example", "thread_id": "omt_example", "request_message_id": "om_request"},
		},
	}
}

func personReplyObservation(now time.Time) map[string]any {
	base := now.UTC().Truncate(time.Second)
	watch := map[string]any{"source": "larkcli", "brand": "feishu", "app_id": "cli_example", "account_open_id": "ou_owner",
		"person_open_id": "ou_peer", "chat_id": "oc_example", "thread_id": "omt_example", "request_message_id": "om_request"}
	return map[string]any{
		"watch": watch,
		"request": map[string]any{"id": "om_request", "sender_id": "ou_owner", "sender_type": "user", "id_type": "open_id",
			"chat_id": "oc_example", "thread_id": "omt_example", "url": "https://applink.feishu.cn/client/thread/open?thread_position=1&open_thread_id=omt_example&open_chat_id=oc_example",
			"at": base.Add(-10 * time.Minute).Format(time.RFC3339), "before": base.Add(-10*time.Minute + time.Second).Format(time.RFC3339)},
		"reply": map[string]any{"id": "om_reply", "sender_id": "ou_peer", "sender_type": "user", "id_type": "open_id",
			"chat_id": "oc_example", "thread_id": "omt_example", "at": base.Add(-2 * time.Minute).Format(time.RFC3339),
			"before": base.Add(-2*time.Minute + time.Second).Format(time.RFC3339)},
		"checked_at": base.Format(time.RFC3339),
	}
}

func parkPersonReply(t *testing.T, s *Service, todo Todo, now time.Time) {
	t.Helper()
	if _, err := reviewErr(s, todo.ID, personReplyParkArgs(now)); err != nil {
		t.Fatalf("park person reply task: %v", err)
	}
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
		p := v.parking()
		if p == nil || len(v.Reviews) == 0 {
			return false
		}
		v.Reviews[len(v.Reviews)-1].At = personReplyParkAt(now)
		return true
	}); err != nil {
		t.Fatalf("set deterministic park time: %v", err)
	}
}

func personReplyPendingKinds(s *Service, id string) []string { return pendingKinds(s, id) }

func personReplyAssertParked(t *testing.T, s *Service, f *fakeHub, id string, wantCalls int) {
	t.Helper()
	got := todoNow(s, id)
	if got.DoneAt != nil || got.Supervision.State != reviewParked {
		t.Fatalf("task woke unexpectedly: done=%v state=%q", got.DoneAt, got.Supervision.State)
	}
	if n := len(f.appParams(AppConnectors, "connectors.lark_reply")); n != wantCalls {
		t.Fatalf("reader calls=%d, want %d", n, wantCalls)
	}
	if got := personReplyPendingKinds(s, id); len(got) != 0 {
		t.Fatalf("pending signals=%v, want none", got)
	}
}

func TestPersonReplyWakeRecordsReceiptAndQueuesReviewOnce(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	before := todoNow(s, todo.ID).Supervision
	f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) { return personReplyObservation(now), nil })
	s.sweepDeploys(now)
	wokeBy := wakeBound(now)
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewPending || got.Supervision.Supervisor != "conv1" {
		t.Fatalf("source reply must only return the task to review: done=%v supervision=%+v", got.DoneAt, got.Supervision)
	}
	if len(got.Supervision.Reviews) != len(before.Reviews) {
		t.Fatalf("review history length=%d, want unchanged %d", len(got.Supervision.Reviews), len(before.Reviews))
	}
	prior, _ := json.Marshal(before.Reviews)
	latest, _ := json.Marshal(requireParkEnded(t, before, got.Supervision.Reviews, wokeBy))
	var priorMaps, latestMaps []map[string]any
	_ = json.Unmarshal(prior, &priorMaps)
	_ = json.Unmarshal(latest, &latestMaps)
	delete(latestMaps[len(latestMaps)-1], "reply")
	stripped, _ := json.Marshal(latestMaps)
	if !reflect.DeepEqual(priorMaps, latestMaps) {
		t.Fatalf("reply wake changed review history beyond its receipt: before=%s after=%s", prior, stripped)
	}
	if len(got.Supervision.Reviews) == 0 {
		t.Fatal("park review missing")
	}
	var receipt map[string]any
	raw, _ := json.Marshal(got.Supervision.Reviews[len(got.Supervision.Reviews)-1])
	_ = json.Unmarshal(raw, &receipt)
	if receipt["reply"] == nil {
		t.Fatalf("source receipt not persisted in review JSON: %s", raw)
	}
	if kinds := personReplyPendingKinds(s, todo.ID); !reflect.DeepEqual(kinds, []string{"review"}) {
		t.Fatalf("signals=%v, want exactly review", kinds)
	}
	if sends := sentToAgent(f); len(sends) != 0 {
		t.Fatalf("source reply was delivered to an Agent: %v", sends)
	}
	s.sweepDeploys(now.Add(time.Minute))
	if n := len(f.appParams(AppConnectors, "connectors.lark_reply")); n != 1 || !reflect.DeepEqual(personReplyPendingKinds(s, todo.ID), []string{"review"}) {
		t.Fatalf("replay reread or duplicated wake: calls=%d signals=%v", n, personReplyPendingKinds(s, todo.ID))
	}
}

func TestPersonReplyAdmissionRejectsIncompleteWatchBeforeMutation(t *testing.T) {
	for _, field := range []string{"app_id", "account_open_id", "person_open_id", "chat_id", "thread_id", "request_message_id"} {
		t.Run(field, func(t *testing.T) {
			s, _, todo := newSupervisedService(t)
			args := personReplyParkArgs(time.Now().UTC())
			e := args["evidence"].(map[string]any)
			e["reply"].(map[string]any)[field] = ""
			if _, err := reviewErr(s, todo.ID, args); err == nil {
				t.Fatal("incomplete source binding admitted")
			}
			if got := todoNow(s, todo.ID).Supervision; got.State != "" || len(got.Reviews) != 0 {
				t.Fatalf("refusal mutated ledger: %+v", got)
			}
		})
	}
}

func TestPersonReplyWakeRejectsMismatchedSourceFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong source", func(w map[string]any) { w["watch"].(map[string]any)["source"] = "other" }},
		{"wrong account", func(w map[string]any) { w["watch"].(map[string]any)["account_open_id"] = "ou_other" }},
		{"wrong app", func(w map[string]any) { w["watch"].(map[string]any)["app_id"] = "cli_other" }},
		{"wrong person", func(w map[string]any) { w["watch"].(map[string]any)["person_open_id"] = "ou_other" }},
		{"wrong chat", func(w map[string]any) { w["watch"].(map[string]any)["chat_id"] = "oc_other" }},
		{"wrong topic", func(w map[string]any) { w["watch"].(map[string]any)["thread_id"] = "omt_other" }},
		{"wrong request", func(w map[string]any) { w["watch"].(map[string]any)["request_message_id"] = "om_other" }},
		{"wrong reply sender", func(w map[string]any) { w["reply"].(map[string]any)["sender_id"] = "ou_other" }},
		{"bot reply", func(w map[string]any) { w["reply"].(map[string]any)["sender_type"] = "app" }},
		{"wrong id type", func(w map[string]any) { w["reply"].(map[string]any)["id_type"] = "union_id" }},
		{"reply from app", func(w map[string]any) { w["reply"].(map[string]any)["app_id"] = "cli_example" }},
		{"wrong reply message", func(w map[string]any) { w["reply"].(map[string]any)["id"] = "om_request" }},
		{"wrong topic URL", func(w map[string]any) { w["request"].(map[string]any)["url"] = "https://example.com/" }},
		{"request from other owner", func(w map[string]any) { w["request"].(map[string]any)["sender_id"] = "ou_other" }},
		{"request id type mismatch", func(w map[string]any) { w["request"].(map[string]any)["id_type"] = "union_id" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			holdDigest(s)
			now := time.Now().UTC().Truncate(time.Second)
			parkPersonReply(t, s, todo, now)
			observation := personReplyObservation(now)
			tc.edit(observation)
			f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) { return observation, nil })
			s.sweepDeploys(now)
			personReplyAssertParked(t, s, f, todo.ID, 1)
		})
	}
}

func TestPersonReplyWakeAcceptsBoundAppRequest(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	observation := personReplyObservation(now)
	request := observation["request"].(map[string]any)
	request["sender_type"], request["id_type"], request["sender_id"], request["app_id"] = "app", "app_id", "cli_example", "cli_example"
	f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) { return observation, nil })
	s.sweepDeploys(now)
	if got := todoNow(s, todo.ID); got.Supervision.State != reviewPending || got.DoneAt != nil {
		t.Fatalf("bound app request did not wake to review: %+v", got.Supervision)
	}
}

func TestPersonReplyWakeRequiresUnambiguousSourceTimes(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		edit                     func(map[string]any, time.Time)
		requestContactInInterval bool
	}{
		{name: "contact timestamp outside request interval", edit: func(o map[string]any, now time.Time) {
			q := o["request"].(map[string]any)
			q["at"], q["before"] = now.Add(-20*time.Minute).Format(time.RFC3339), now.Add(-19*time.Minute).Format(time.RFC3339)
		}},
		{name: "request interval straddles park", edit: func(o map[string]any, now time.Time) {
			parkAt := personReplyParkAt(now)
			q := o["request"].(map[string]any)
			q["at"], q["before"] = parkAt.Add(-30*time.Second).Format(time.RFC3339), parkAt.Add(30*time.Second).Format(time.RFC3339)
		}, requestContactInInterval: true},
		{name: "reply interval straddles park", edit: func(o map[string]any, now time.Time) {
			parkAt := personReplyParkAt(now)
			r := o["reply"].(map[string]any)
			r["at"], r["before"] = parkAt.Add(-30*time.Second).Format(time.RFC3339), parkAt.Add(30*time.Second).Format(time.RFC3339)
		}},
		{name: "reply before park", edit: func(o map[string]any, now time.Time) {
			parkAt := personReplyParkAt(now)
			r := o["reply"].(map[string]any)
			r["at"], r["before"] = parkAt.Add(-2*time.Second).Format(time.RFC3339), parkAt.Add(-time.Second).Format(time.RFC3339)
		}},
		{name: "reply exactly at park", edit: func(o map[string]any, now time.Time) {
			parkAt := personReplyParkAt(now)
			r := o["reply"].(map[string]any)
			r["at"], r["before"] = parkAt.Format(time.RFC3339), parkAt.Add(time.Second).Format(time.RFC3339)
		}},
		{name: "zero request time", edit: func(o map[string]any, _ time.Time) {
			o["request"].(map[string]any)["at"] = time.Time{}.Format(time.RFC3339)
		}},
		{name: "invalid timestamp", edit: func(o map[string]any, _ time.Time) { o["reply"].(map[string]any)["at"] = "not-a-time" }},
		{name: "non-increasing precision interval", edit: func(o map[string]any, _ time.Time) {
			o["reply"].(map[string]any)["before"] = o["reply"].(map[string]any)["at"]
		}},
		{name: "precision wider than one minute", edit: func(o map[string]any, now time.Time) {
			r := o["reply"].(map[string]any)
			r["before"] = now.Format(time.RFC3339)
		}},
		{name: "checked time in future", edit: func(o map[string]any, now time.Time) { o["checked_at"] = now.Add(time.Hour).Format(time.RFC3339) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			holdDigest(s)
			now := time.Now().UTC().Truncate(time.Second)
			args := personReplyParkArgs(now)
			observation := personReplyObservation(now)
			tc.edit(observation, now)
			if tc.requestContactInInterval {
				q := observation["request"].(map[string]any)
				args["evidence"].(map[string]any)["contact"].(map[string]any)["sent_at"] = q["at"]
			}
			if _, err := reviewErr(s, todo.ID, args); err != nil {
				t.Fatalf("admit fixture park: %v", err)
			}
			if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
				v.Reviews[len(v.Reviews)-1].At = personReplyParkAt(now)
				return true
			}); err != nil {
				t.Fatal(err)
			}
			f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) { return observation, nil })
			s.sweepDeploys(now)
			personReplyAssertParked(t, s, f, todo.ID, 1)
		})
	}
}

func TestPersonReplyInFlightReparkDoesNotApplyOldObservation(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	args := personReplyParkArgs(now)
	f.answerAppBlocking(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		if _, err := reviewErr(s, todo.ID, args); err != nil {
			return nil, err
		}
		// The read belongs to the older park even when a reply happened after the replacement park too.
		// A timestamp predicate alone must not satisfy this independent instance-fence regression.
		_, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
			v.Reviews[len(v.Reviews)-1].At = now.Add(-3 * time.Minute)
			return true
		})
		return personReplyObservation(now), err
	})
	s.sweepDeploys(now)
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewParked || len(got.Supervision.Reviews) != 2 {
		t.Fatalf("old observation crossed repark: state=%q done=%v reviews=%d", got.Supervision.State, got.DoneAt, len(got.Supervision.Reviews))
	}
	if got := personReplyPendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("repark created a wake signal: %v", got)
	}
}

func TestPersonReplyInFlightSupervisorReassignmentPreservesLatestOwner(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	f.answerAppBlocking(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		_, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool { v.Supervisor = "conv_latest"; return true })
		return personReplyObservation(now), err
	})
	s.sweepDeploys(now)
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewPending || got.Supervision.Supervisor != "conv_latest" {
		t.Fatalf("reply wake lost latest supervisor: %+v", got.Supervision)
	}
	if sends := sentToAgent(f); len(sends) != 0 {
		t.Fatalf("reply wake sent to Agent: %v", sends)
	}
}

func TestPersonReplyInFlightSameTimeBindingReplacementRejectsObservation(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	f.answerAppBlocking(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		_, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
			raw, _ := json.Marshal(v.Reviews)
			var reviews []map[string]any
			if json.Unmarshal(raw, &reviews) != nil || len(reviews) == 0 {
				return false
			}
			e := reviews[len(reviews)-1]["evidence"].(map[string]any)
			e["reply"].(map[string]any)["app_id"] = "cli_replaced"
			raw, _ = json.Marshal(reviews)
			return json.Unmarshal(raw, &v.Reviews) == nil
		})
		return personReplyObservation(now), err
	})
	s.sweepDeploys(now)
	personReplyAssertParked(t, s, f, todo.ID, 1)
}

func TestPersonReplyInFlightCompletionDoesNotWakeClosedTask(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	f.answerAppBlocking(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		closed, err := s.reviewDoneTodo(context.Background(), todo.ID, "The task was cancelled", false)
		if err != nil || !closed {
			t.Fatalf("complete the task while the read is out: closed=%v, %v", closed, err)
		}
		return personReplyObservation(now), nil
	})
	s.sweepDeploys(now)
	cur := todoNow(s, todo.ID)
	if cur.DoneAt == nil || cur.Supervision.State == reviewPending || pendingReviewSignal(s, todo.ID) ||
		len(f.appParams(AppConnectors, "connectors.lark_reply")) != 1 {
		t.Fatalf("an in-flight observation reopened or woke the closed task: %+v", cur)
	}
	raw, _ := json.Marshal(cur.Supervision.Reviews)
	var reviews []map[string]any
	if err := json.Unmarshal(raw, &reviews); err != nil || reviews[len(reviews)-1]["reply"] != nil {
		t.Fatalf("a closed task acquired a reply transition: %s, %v", raw, err)
	}
}

func TestPersonReplyUnboundAndOtherWaitsAreNotRead(t *testing.T) {
	for _, kind := range []string{"legacy", "person_reply", "merge_review", "external_event", "date", "completed"} {
		t.Run(kind, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			holdDigest(s)
			now := time.Now().UTC().Truncate(time.Second)
			if kind == "legacy" {
				seedLegacyPark(t, s, todo.ID, "Waiting for the named person in a topic", parkSomeoneElse, "3d")
			} else if kind == "completed" {
				parkPersonReply(t, s, todo, now)
				if closed, err := s.reviewDoneTodo(context.Background(), todo.ID, "The task was cancelled", false); err != nil || !closed {
					t.Fatalf("complete the bound task: closed=%v, %v", closed, err)
				}
			} else if _, err := reviewErr(s, todo.ID, parkEvidenceArgs(kind)); err != nil {
				t.Fatal(err)
			}
			s.sweepDeploys(now)
			if len(f.appParams(AppConnectors, "connectors.lark_reply")) != 0 || pendingReviewSignal(s, todo.ID) {
				t.Fatal("an unbound, unrelated or closed wait was observed or woken")
			}
			if kind != "completed" && todoNow(s, todo.ID).Supervision.State != reviewParked {
				t.Fatal("the unrelated wait changed")
			}
		})
	}
}

func TestPersonReplyUnavailableSourceRetriesWithoutWaking(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	parkPersonReply(t, s, todo, now)
	f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		return nil, errors.New("the configured source is unavailable")
	})
	s.sweepDeploys(now)
	personReplyAssertParked(t, s, f, todo.ID, 1)
	f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		return personReplyObservation(now), nil
	})
	s.sweepDeploys(now.Add(30 * time.Second))
	personReplyAssertParked(t, s, f, todo.ID, 1)
	s.sweepDeploys(now.Add(time.Minute))
	if todoNow(s, todo.ID).Supervision.State != reviewPending || !pendingReviewSignal(s, todo.ID) ||
		len(f.appParams(AppConnectors, "connectors.lark_reply")) != 2 {
		t.Fatal("a source failure was not retried on the same saved wait")
	}
}
