package node

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The configured model's failure classes as the recovery marker names them (#7824).
func TestModelRecoveryReason(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"API Error: Opus 5.5's safeguards flagged this message (https://www.anthropic.com/legal/aup).", proto.ModelRecoveryReasonSafeguards},
		{"API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 15:48 UTC (in 32m)", proto.ModelRecoveryReasonQuota},
		{`API Error: 429 {"error":{"code":"quota_exhausted","message":"..."}}`, proto.ModelRecoveryReasonQuota},
		{"API Error: Request rejected (429) · Too many requests", proto.ModelRecoveryReasonRateLimit},
		{"API Error: 529 overloaded", proto.ModelRecoveryReasonServer},
		{"API Error: 503 no eligible upstream account is currently available", proto.ModelRecoveryReasonServer},
		{"There's an issue with the selected model (x). It may not exist or you may not have access to it.", proto.ModelRecoveryReasonUnavailable},
	} {
		if got := ModelRecoveryReason(c.text); got != c.want {
			t.Errorf("%q: reason %q, want %q", c.text, got, c.want)
		}
	}
}

// A gateway names when an exhausted account is back; the bare clock form is the next such moment in UTC, the dated
// form is read as written. Other words name no time.
func TestGatewayAvailableAgain(t *testing.T) {
	at := time.Date(2026, 11, 14, 15, 16, 0, 0, time.UTC)
	for _, c := range []struct {
		text string
		want time.Time
		ok   bool
	}{
		{"an account becomes available again at 15:48 UTC (in 32m)", time.Date(2026, 11, 14, 15, 48, 0, 0, time.UTC), true},
		{"an account becomes available again at 09:00 UTC (in 17h44m)", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC), true},
		{"available again at 2026-11-17 04:03 UTC (in 2d12h)", time.Date(2026, 11, 17, 4, 3, 0, 0, time.UTC), true},
		{"API Error: Rate limit reached", time.Time{}, false},
		{"available again at 25:00 UTC", time.Time{}, false},
	} {
		got, ok := GatewayAvailableAgain(c.text, at)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("%q: %v %v, want %v %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

// recoveryTestNode is a node with conversation id recorded as the main conversation on launcher "claude" asking for
// primary-model, and recovery pinned to an ordered list primary → fallback → last, with sends captured.
func recoveryTestNode(t *testing.T, id string, isMain, followsSetting bool) (*Node, chan modelExecution) {
	t.Helper()
	models, send, probe := assistantRecoveryModels, assistantRecoverySend, assistantModelProbe
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend, assistantModelProbe = models, send, probe })
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}, {ID: "last-model"}}}, nil
	}
	sent := make(chan modelExecution, 8)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		sent <- execution
		return nil
	}
	assistantModelProbe = func(context.Context, *Node, string, string) bool { return false }
	n := assistantNode(t)
	n.state.put(id, sessionState{Launcher: "claude", Cwd: t.TempDir(), Model: "primary-model", IsMain: isMain, FollowsSetting: followsSetting})
	return n, sent
}

func waitRecoverySend(t *testing.T, sent chan modelExecution) modelExecution {
	t.Helper()
	select {
	case e := <-sent:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no recovery send")
	}
	return modelExecution{}
}

// The 23:20 regression: a safeguards refusal on the main conversation's configured model moves it to the next model,
// and the move is recorded where every client reads it, with the configured model, the model now answering and why.
// Before #7824 the node switched silently and the apps said no other model was used.
func TestRecoverySwitchIsVisibleOnTheMainConversation(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, sent := recoveryTestNode(t, id, true, false)
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
	n.procs[id] = p
	if !n.recoverAssistantResult(p, true, "API Error: Opus 5.5's safeguards flagged this message") {
		t.Fatal("the refusal was not recovered")
	}
	if e := waitRecoverySend(t, sent); e.Model != "fallback-model" {
		t.Fatalf("recovery sent on %q", e.Model)
	}
	waitRecovery(t, "the marker", func() bool { return n.modelRecovery(id) != nil })
	r := n.modelRecovery(id)
	if r.From != "primary-model" || r.To != "fallback-model" || r.Reason != proto.ModelRecoveryReasonSafeguards || r.ChainExhausted {
		t.Fatalf("marker %+v", r)
	}
	if len(r.Tried) < 2 || r.Tried[0] != "primary-model" || r.Tried[len(r.Tried)-1] != "fallback-model" {
		t.Fatalf("tried %v", r.Tried)
	}
	// The session row carries it in the shape the shipped apps already draw as "switched to X".
	s := proto.Session{SessionID: id}
	n.decorateModelRecovery(&s)
	f := s.ModelFallback
	if f == nil || f.Subtype != "model_refusal_fallback" || f.OriginalModel != "primary-model" || f.FallbackModel != "fallback-model" ||
		f.Category != proto.ModelRecoveryReasonSafeguards || f.Scope != "session" {
		t.Fatalf("projected fallback %+v", f)
	}
	// Persisted with the session's record, so a node restart keeps it.
	if st, _ := n.state.get(id); st.ModelRecovery == nil || st.ModelRecovery.To != "fallback-model" {
		t.Fatalf("not persisted: %+v", st.ModelRecovery)
	}
	// It never changes the configured model.
	if st, _ := n.state.get(id); st.Model != "primary-model" {
		t.Fatalf("the configured model changed to %q", st.Model)
	}
}

// An older recovery write must not finish after a newer one and leave the durable store saying the old model.
func TestModelRecoveryPersistenceSerializesConcurrentWriters(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer unblock()
	var mu sync.Mutex
	var durable *proto.ModelRecovery
	n.state.persist = func(_ string, v sessionState) {
		if v.ModelRecovery != nil && v.ModelRecovery.To == "first-model" {
			close(firstStarted)
			<-releaseFirst
		}
		mu.Lock()
		durable = v.ModelRecovery
		mu.Unlock()
	}
	firstDone := make(chan struct{})
	go func() {
		n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "first-model"})
		close(firstDone)
	}()
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("the first recovery write did not reach persistence")
	}
	secondDone := make(chan struct{})
	go func() {
		n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "second-model"})
		close(secondDone)
	}()
	waitRecovery(t, "the newer in-memory marker", func() bool {
		r := n.modelRecovery(id)
		return r != nil && r.To == "second-model"
	})
	select {
	case <-secondDone: // the old, unordered implementation can finish before the first
	case <-time.After(100 * time.Millisecond): // the serialized implementation waits here
	}
	unblock()
	for name, done := range map[string]chan struct{}{"first": firstDone, "second": secondDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("the %s recovery write did not finish", name)
		}
	}
	if r := n.modelRecovery(id); r == nil || r.To != "second-model" {
		t.Fatalf("in-memory marker %+v", r)
	}
	if st, _ := n.state.get(id); st.ModelRecovery == nil || st.ModelRecovery.To != "second-model" {
		t.Fatalf("session marker %+v", st.ModelRecovery)
	}
	mu.Lock()
	defer mu.Unlock()
	if durable == nil || durable.To != "second-model" {
		t.Fatalf("durable marker %+v: an older write finished last", durable)
	}
}

// The owner's required round trip: Opus refused → the conversation answers on the next model → Opus is available again
// → the very next request runs on Opus, not on the cheaper model for the rest of the hold window.
func TestRecoveryReturnsToTheConfiguredModelOnTheNextRequest(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, sent := recoveryTestNode(t, id, true, false)
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
	n.procs[id] = p
	if !n.recoverAssistantResult(p, true, "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 23:59 UTC (in 32m)", "", assistantRecordKind) {
		t.Fatal("the quota failure was not recovered")
	}
	// The gateway quota 429 gets one same-model try first (#3788), then the switch.
	first := waitRecoverySend(t, sent)
	if first.Model == "primary-model" {
		p2 := &assistantProc{id: id, agent: "claude", launcher: "claude", model: first}
		n.mu.Lock()
		n.procs[id] = p2
		n.mu.Unlock()
		waitRecovery(t, "the first attempt to finish", func() bool {
			n.mu.Lock()
			defer n.mu.Unlock()
			r := n.recoveries[id]
			return r != nil && !r.running
		})
		if !n.recoverAssistantResult(p2, true, "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 23:59 UTC (in 32m)", "", assistantRecordKind) {
			t.Fatal("the second quota failure was not recovered")
		}
		first = waitRecoverySend(t, sent)
	}
	if first.Model != "fallback-model" {
		t.Fatalf("switched to %q", first.Model)
	}
	waitRecovery(t, "the marker", func() bool { r := n.modelRecovery(id); return r != nil && r.To == "fallback-model" })
	if r := n.modelRecovery(id); r.Reason != proto.ModelRecoveryReasonQuota || r.ResetsAt == nil {
		t.Fatalf("quota marker %+v", r)
	}

	// The fallback answered: the hold starts, as before.
	fp := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "fallback-model"}}
	n.mu.Lock()
	n.procs[id] = fp
	n.mu.Unlock()
	n.clearAssistantRecovery(fp, true)
	asked := modelExecution{Model: "primary-model"}
	if got := n.heldFallback(id, "claude", asked); got.Model != "fallback-model" {
		t.Fatalf("the hold did not start: %q", got.Model)
	}

	// While the configured model still fails its probe, the next request stays on the fallback.
	var probes atomic.Int32
	var available atomic.Bool
	assistantModelProbe = func(_ context.Context, _ *Node, launcher, model string) bool {
		probes.Add(1)
		if model != "primary-model" {
			t.Errorf("probed %q, want the configured model", model)
		}
		return available.Load()
	}
	if n.returnToConfiguredAt(context.Background(), id, time.Now()) {
		t.Fatal("returned while the configured model still fails")
	}
	// The configured model comes back: the probe loop marks the return, and the next request runs on it.
	available.Store(true)
	n.probeModelRecoveries(context.Background())
	if r := n.modelRecovery(id); r == nil || !r.Returning {
		t.Fatalf("the probe did not mark the return: %+v", r)
	}
	if !n.returnToConfiguredAt(context.Background(), id, time.Now()) {
		t.Fatal("did not return although the configured model answers")
	}
	exec := n.heldFallback(id, "claude", asked)
	if n.recoveryProtected(id) && n.returnToConfiguredAt(context.Background(), id, time.Now()) {
		marker := n.modelRecovery(id)
		n.releaseModelRecovery(id, marker.From)
		exec.Model = marker.From
	}
	if exec.Model != "primary-model" {
		t.Fatalf("the next request runs on %q, want the configured model", exec.Model)
	}
	if n.modelRecovery(id) != nil {
		t.Fatal("the marker outlived the return")
	}
	if got := n.heldFallback(id, "claude", asked); got.Model != "primary-model" {
		t.Fatalf("the hold outlived the return: %q", got.Model)
	}
	if probes.Load() == 0 {
		t.Fatal("never probed")
	}
}

// A launcher default keeps its requested execution (empty or literal "default") while ProbeFrom names the concrete
// configured model. Recovery probes that concrete model, and the next send returns to the unchanged default request.
func TestDefaultModelRecoveryReturnsAfterProbe(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, requested := range []string{"", "default"} {
		name := "empty"
		if requested != "" {
			name = "literal-default"
		}
		t.Run(name, func(t *testing.T) {
			n, sent := recoveryTestNode(t, id, true, false)
			assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
				return proto.LauncherModels{Models: []proto.AgentModel{
					{ID: "default", Default: true}, // unresolved defaults rely on the first reply's concrete model
					{ID: "fallback-model"}, {ID: "last-model"},
				}}, nil
			}
			st, _ := n.state.get(id)
			st.Model = requested
			n.state.put(id, st)
			p := &assistantProc{id: id, agent: "codex", launcher: "claude", model: modelExecution{Model: requested}}
			n.procs[id] = p
			const failure = "API Error: safeguards flagged"
			if !n.recoverAssistantResult(p, true, failure, "", assistantRecordKind) {
				t.Fatal("the first default-model failure was not recovered")
			}
			if first := waitRecoverySend(t, sent); first.Model != requested {
				t.Fatalf("the same-default retry used %q, want %q", first.Model, requested)
			}
			waitRecovery(t, "the same-default attempt to finish", func() bool {
				n.mu.Lock()
				defer n.mu.Unlock()
				r := n.recoveries[id]
				return r != nil && !r.running
			})
			// The first failure emitted no message_start. Only the retry names the concrete default; the existing
			// recovery chain must capture it now rather than staying unprobeable or reading a later fallback's name.
			p.mu.Lock()
			p.replyModel = "primary-model"
			p.mu.Unlock()
			if !n.recoverAssistantResult(p, true, failure, "", assistantRecordKind) {
				t.Fatal("the second default-model failure was not recovered")
			}
			if next := waitRecoverySend(t, sent); next.Model != "fallback-model" {
				t.Fatalf("the default-model recovery used %q", next.Model)
			}
			waitRecovery(t, "the default recovery marker", func() bool { return n.modelRecovery(id) != nil })
			r := n.modelRecovery(id)
			if r.From != requested || r.ProbeFrom != "primary-model" || r.To != "fallback-model" {
				t.Fatalf("default marker %+v", r)
			}
			if st, _ := n.state.get(id); st.ModelRecovery == nil || st.ModelRecovery.From != requested || st.ModelRecovery.ProbeFrom != "primary-model" {
				t.Fatalf("the default probe identity was not persisted: %+v", st.ModelRecovery)
			}
			fp := &assistantProc{id: id, agent: "codex", launcher: "claude", model: modelExecution{Model: "fallback-model"}}
			n.mu.Lock()
			n.procs[id] = fp
			n.mu.Unlock()
			n.clearAssistantRecovery(fp, true)
			var probed string
			assistantModelProbe = func(_ context.Context, _ *Node, _, model string) bool {
				probed = model
				return true
			}
			n.probeModelRecoveries(context.Background())
			if probed != "primary-model" {
				t.Fatalf("probed %q, want the concrete configured default", probed)
			}
			if r := n.modelRecovery(id); r == nil || !r.Returning {
				t.Fatalf("the default did not enter Returning: %+v", r)
			}
			s := proto.Session{SessionID: id}
			n.decorateModelRecovery(&s)
			if s.ModelFallback == nil || s.ModelFallback.OriginalModel != "default" || !s.ModelFallback.RecoveryReturning {
				t.Fatalf("an empty default's returning line would be hidden: %+v", s.ModelFallback)
			}
			n.unavailable = map[string]time.Time{unavailableKey("claude", "primary-model"): time.Now().Add(time.Hour)}
			asked := modelExecution{Model: requested, IsMain: true}
			execution, expired := n.heldFallbackWithExpiry(id, "claude", asked)
			execution = n.applyModelRecoveryReturn(context.Background(), id, asked.Model, execution, expired)
			if execution.Model != requested {
				t.Fatalf("the return pinned execution model %q, want requested default %q", execution.Model, requested)
			}
			if n.modelRecovery(id) != nil {
				t.Fatal("the default marker outlived the confirmed return")
			}
			if got := n.heldFallback(id, "claude", asked); got.Model != requested {
				t.Fatalf("the fallback hold outlived the confirmed return: %q", got.Model)
			}
			n.mu.Lock()
			_, unavailable := n.unavailable[unavailableKey("claude", "primary-model")]
			n.mu.Unlock()
			if unavailable {
				t.Fatal("the concrete default's unavailable record outlived the return")
			}
		})
	}
}

// Later switches and chain exhaustion retain the concrete probe identity of a launcher default.
func TestDefaultModelRecoveryProbeFromSurvivesChainExhaustion(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.recordRecoverySwitch(id, "claude", "", "primary-model", "fallback-model", proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model"})
	n.recordRecoverySwitch(id, "claude", "", "changed-model", "last-model", proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model", "last-model"})
	if r := n.modelRecovery(id); r == nil || r.From != "" || r.ProbeFrom != "primary-model" || r.To != "last-model" {
		t.Fatalf("later switch lost the probe identity: %+v", r)
	}
	n.recordRecoveryExhausted(id, proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model", "last-model"})
	r := n.modelRecovery(id)
	if r == nil || !r.ChainExhausted || r.From != "" || r.ProbeFrom != "primary-model" || r.To != "last-model" {
		t.Fatalf("exhaustion lost the probe identity: %+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored proto.ModelRecovery
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.From != "" || restored.ProbeFrom != "primary-model" {
		t.Fatalf("the probe identity did not survive persistence: %+v", restored)
	}
}

// Publishing Returning replaces the marker instead of mutating a snapshot that session decoration may still be reading.
func TestRecoveryProbePublishesReturningWithoutMutatingOldMarker(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	before := n.modelRecovery(id)
	assistantModelProbe = func(context.Context, *Node, string, string) bool { return true }
	n.probeModelRecoveries(context.Background())
	current := n.modelRecovery(id)
	if current == nil || !current.Returning || current == before {
		t.Fatalf("the probe did not publish a returning replacement: before %p, current %+v", before, current)
	}
	if before.Returning {
		t.Fatal("the probe mutated the previously published marker")
	}
	if st, _ := n.state.get(id); st.ModelRecovery == nil || !st.ModelRecovery.Returning {
		t.Fatalf("the returning marker was not persisted: %+v", st.ModelRecovery)
	}
	s := proto.Session{SessionID: id}
	n.decorateModelRecovery(&s)
	if s.ModelFallback == nil || !s.ModelFallback.RecoveryReturning {
		t.Fatalf("the returning marker is not visible: %+v", s.ModelFallback)
	}
}

// Concurrent session decoration and probes are safe under -race: readers keep immutable snapshots throughout replacement.
func TestRecoveryProbeConcurrentDecorate(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	assistantModelProbe = func(context.Context, *Node, string, string) bool { return true }
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s := proto.Session{SessionID: id}
				n.decorateModelRecovery(&s)
			}
		}
	}()
	for i := 0; i < 500; i++ {
		n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
		n.probeModelRecoveries(context.Background())
	}
	close(stop)
	readers.Wait()
}

// A worker Agent's conversation keeps its own fallback behavior: no marker, no forced return.
func TestRecoveryMarkerOnlyForMainAndLeaders(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, c := range []struct {
		name            string
		isMain, follows bool
		want            bool
	}{
		{"main", true, false, true},
		{"leader", false, true, true},
		{"worker", false, false, false},
	} {
		n, sent := recoveryTestNode(t, id, c.isMain, c.follows)
		p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
		n.procs[id] = p
		if !n.recoverAssistantResult(p, true, "API Error: Opus 5.5's safeguards flagged this message") {
			t.Fatalf("%s: not recovered", c.name)
		}
		waitRecoverySend(t, sent)
		n.mu.Lock()
		done := n.recoveries[id].done
		n.mu.Unlock()
		if done != nil {
			<-done // the recovery is over, with any marker it sets
		}
		if got := n.modelRecovery(id) != nil; got != c.want {
			t.Errorf("%s: marker %v, want %v", c.name, got, c.want)
		}
	}
}

// Every model of the chain failing ends on an exhausted marker: the failure card's cause, reset time and Retry come
// from it, and it is projected without a fallback model so the apps' fallback card does not claim a switch.
func TestRecoveryChainExhausted(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	models := assistantRecoveryModels
	n, sent := recoveryTestNode(t, id, true, false)
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}}}, nil // nothing to move to
	}
	t.Cleanup(func() { assistantRecoveryModels = models })
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
	n.procs[id] = p
	const quota = "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 23:59 UTC (in 32m)"
	if !n.recoverAssistantResult(p, true, quota, "", assistantRecordKind) {
		t.Fatal("not recovered")
	}
	if e := waitRecoverySend(t, sent); e.Model != "primary-model" {
		t.Fatalf("same-model try on %q", e.Model)
	}
	waitRecovery(t, "the attempt to finish", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		r := n.recoveries[id]
		return r != nil && !r.running
	})
	if !n.recoverAssistantResult(p, true, quota, "", assistantRecordKind) {
		t.Fatal("the second failure was not taken")
	}
	waitRecovery(t, "the exhausted marker", func() bool { r := n.modelRecovery(id); return r != nil && r.ChainExhausted })
	r := n.modelRecovery(id)
	if r.Reason != proto.ModelRecoveryReasonQuota || r.ResetsAt == nil || r.ResetsAt.UTC().Format("15:04") != "23:59" {
		t.Fatalf("exhausted marker %+v", r)
	}
	s := proto.Session{SessionID: id}
	n.decorateModelRecovery(&s)
	if s.ModelFallback == nil || !s.ModelFallback.RecoveryExhausted || s.ModelFallback.FallbackModel != "" || s.ModelFallback.RecoveryReset == nil {
		t.Fatalf("projection %+v", s.ModelFallback)
	}
}

// The runtime's own refusal fallback is first-hand evidence and is never replaced by the node's projection.
func TestRuntimeFallbackWinsOverTheProjection(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonSafeguards})
	own := &proto.ModelRefusal{Subtype: "model_refusal_fallback", OriginalModel: "primary-model", FallbackModel: "runtime-pick"}
	s := proto.Session{SessionID: id, ModelFallback: own}
	n.decorateModelRecovery(&s)
	if s.ModelFallback != own {
		t.Fatalf("the runtime's record was replaced: %+v", s.ModelFallback)
	}
}

// The marker survives a node restart through the session record (state.load), so the apps still say which model
// answers after a redeploy, and the probe loop can still bring the conversation back.
func TestRecoveryMarkerSurvivesARestart(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	st, _ := n.state.get(id)
	b, err := json.Marshal(map[string]sessionState{id: st})
	if err != nil {
		t.Fatal(err)
	}
	restarted := assistantNode(t)
	var loaded map[string]sessionState
	if err := json.Unmarshal(b, &loaded); err != nil {
		t.Fatal(err)
	}
	restarted.state.merge(loaded)
	if r := restarted.modelRecovery(id); r == nil || r.From != "primary-model" || r.To != "fallback-model" {
		t.Fatalf("after a restart: %+v", r)
	}
	if !restarted.recoveryProtected(id) {
		t.Fatal("the main conversation lost its protection")
	}
	// The probe loop must include this state-only marker even before anything hydrates recoveryMarks.
	var probed string
	assistantModelProbe = func(_ context.Context, _ *Node, _, model string) bool {
		probed = model
		return true
	}
	restarted.probeModelRecoveries(context.Background())
	if probed != "primary-model" {
		t.Fatalf("after a restart, probed %q", probed)
	}
	if r := restarted.modelRecovery(id); r == nil || !r.Returning {
		t.Fatalf("the state-only marker did not enter Returning: %+v", r)
	}
	if st, _ := restarted.state.get(id); st.ModelRecovery == nil || !st.ModelRecovery.Returning {
		t.Fatalf("Returning was not persisted after a restart: %+v", st.ModelRecovery)
	}
}

// Compacting a conversation summarizes the refused turns away: the temporary switch ends with them.
func TestCompactionEndsTheRecovery(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model"})
	n.contextCompacted(id)
	if n.modelRecovery(id) != nil {
		t.Fatal("the marker outlived the compaction")
	}
	if st, _ := n.state.get(id); st.ModelRecovery != nil {
		t.Fatal("the persisted marker outlived the compaction")
	}
}

// The probe never asks with the fallback's name, and only a 2xx counts: a 429, 5xx or auth failure keeps the fallback.
func TestModelProbeStatuses(t *testing.T) {
	for _, c := range []struct {
		status int
		want   bool
	}{{200, true}, {429, false}, {500, false}, {529, false}, {401, false}, {404, false}} {
		got := probeStatusOK(c.status)
		if got != c.want {
			t.Errorf("status %d: available %v, want %v", c.status, got, c.want)
		}
	}
}

func waitRecovery(t *testing.T, what string, ok func() bool) {
	t.Helper()
	waitWithin(t, 3*time.Second, what, ok)
}

// When the 30-minute fallback hold expires on its own, the next message asks for the configured model again even if no
// availability probe can run: the marker must not linger after the conversation has effectively returned (#7824).
func TestRecoveryMarkerClearsWhenTheHoldExpiresWithoutAProbe(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	// A marker on the fallback, and a hold whose window has already passed.
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	n.fallbacks = map[string]*assistantFallback{id: {launcher: "claude",
		from:  modelExecution{Model: "primary-model"},
		to:    modelExecution{Model: "fallback-model"},
		at:    time.Now().Add(-assistantRefusalSticky - time.Minute), // older than both the hold and the sticky window
		until: time.Now().Add(-time.Minute)}}
	// No gateway can be probed in this node (assistantModelProbe is replaced by recoveryTestNode to return false).
	asked := modelExecution{Model: "primary-model"}
	got := n.heldFallback(id, "claude", asked)
	if got.Model != "primary-model" {
		t.Fatalf("the expired hold did not return the asked model: %q", got.Model)
	}
	backViaHold := asked.Model == "primary-model" && got.Model == "primary-model"
	if !backViaHold {
		t.Fatal("the natural-expiry condition should hold")
	}
	// Mirror assistantSend: natural expiry clears only the marker, with no successful probe required.
	if r := n.modelRecovery(id); r != nil {
		n.clearModelRecovery(id)
	}
	if n.modelRecovery(id) != nil {
		t.Fatal("the marker outlived the expired hold")
	}
	if got := n.heldFallback(id, "claude", asked); got.Model != "primary-model" {
		t.Fatalf("a later message is not on the configured model: %q", got.Model)
	}
}

// A reconnect has the same expired hold and persisted marker, but no recoveryMarks map entry yet.
func TestRecoveryStateOnlyMarkerClearsWhenTheHoldExpiresWithoutAProbe(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	n.recoveryMarks = nil
	n.fallbacks = map[string]*assistantFallback{id: {launcher: "claude",
		from: modelExecution{Model: "primary-model"}, to: modelExecution{Model: "fallback-model"},
		at: time.Now().Add(-assistantFallbackHold - time.Minute), until: time.Now().Add(-time.Minute)}}
	if r := n.modelRecovery(id); r == nil {
		t.Fatal("the state-only marker is not visible before assistantSend")
	}
	asked := modelExecution{Model: "primary-model"}
	got := n.heldFallback(id, "claude", asked)
	backViaHold := asked.Model == "primary-model" && got.Model == "primary-model"
	if !backViaHold {
		t.Fatalf("the expired hold did not reach assistantSend's backViaHold branch: %+v", got)
	}
	// Mirror assistantSend's natural-expiry branch: clear the marker but keep the sticky fallback record.
	n.clearModelRecovery(id)
	if n.modelRecovery(id) != nil {
		t.Fatal("the state-only marker outlived the expired hold")
	}
	if st, _ := n.state.get(id); st.ModelRecovery != nil {
		t.Fatal("the state-only marker was not cleared from session state")
	}
}

// Natural hold expiry clears the visible marker, but a new 429 inside the two-hour sticky window goes straight to the
// fallback instead of paying for another same-model retry. This drives the real assistantSend branch and needs no zsh.
func TestRecoveryHoldExpiryKeepsStickyFallback(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, sent := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	n.recoveryMarks = nil // reconnect-shaped: the marker exists only in session state
	n.fallbacks = map[string]*assistantFallback{id: {launcher: "claude",
		from: modelExecution{Model: "primary-model"}, to: modelExecution{Model: "fallback-model"},
		at: time.Now().Add(-assistantFallbackHold - time.Minute), until: time.Now().Add(-time.Minute)}}
	n.unavailable = map[string]time.Time{unavailableKey("claude", "primary-model"): time.Now().Add(time.Hour)}
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
	n.procs[id] = p
	sentinel := errors.New("stop after assistantSend chooses the configured model")
	probeKey := n.home + "\x00claude\x00primary-model"
	modelProbeCache.Lock()
	modelProbeCache.results[probeKey] = modelProbeResult{at: time.Now(), agent: "claude", err: sentinel}
	modelProbeCache.Unlock()
	t.Cleanup(func() {
		modelProbeCache.Lock()
		delete(modelProbeCache.results, probeKey)
		modelProbeCache.Unlock()
	})
	st, _ := n.state.get(id)
	_, err := n.assistantSend(context.Background(), id, st.Cwd, "claude", "next message", nil, "", "", modelExecution{Model: "primary-model", IsMain: true})
	if !errors.Is(err, sentinel) {
		t.Fatalf("assistantSend did not reach the configured-model check: %v", err)
	}
	if n.modelRecovery(id) != nil {
		t.Fatal("the state-only marker outlived natural hold expiry")
	}
	n.mu.Lock()
	from, launcher, sticky := n.recentFallback(id)
	_, unavailableKept := n.unavailable[unavailableKey("claude", "primary-model")]
	n.mu.Unlock()
	if !sticky || launcher != "claude" || from.Model != "primary-model" {
		t.Fatalf("natural expiry destroyed the sticky fallback: %+v %q %v", from, launcher, sticky)
	}
	if !unavailableKept {
		t.Fatal("natural expiry cleared the configured model's unavailable record")
	}
	if n.recoverAssistantResult(p, true, "API Error: 429 Too many requests", "", assistantRecordKind) {
		t.Fatal("throttling bypassed manager scheduling")
	}
	select {
	case <-sent:
		t.Fatal("throttling sent an immediate recovery")
	default:
	}
}

// A cold restart has the persisted marker but no in-memory hold. That is not natural expiry: keep the fallback until
// the configured model answers a probe, then return to the unchanged requested model.
func TestRecoveryRestartWithoutHoldKeepsFallbackUntilProbe(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, requested := range []string{"primary-model", ""} {
		t.Run("requested="+requested, func(t *testing.T) {
			n, _ := recoveryTestNode(t, id, true, false)
			n.putModelRecovery(id, &proto.ModelRecovery{From: requested, ProbeFrom: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
			n.recoveryMarks = nil
			var available atomic.Bool
			var probes atomic.Int32
			assistantModelProbe = func(_ context.Context, _ *Node, _, model string) bool {
				if model != "primary-model" {
					t.Errorf("probed %q", model)
				}
				probes.Add(1)
				return available.Load()
			}
			asked := modelExecution{Model: requested, IsMain: true}
			execution, expired := n.heldFallbackWithExpiry(id, "claude", asked)
			if expired {
				t.Fatal("a missing hold after restart was mistaken for natural expiry")
			}
			execution = n.applyModelRecoveryReturn(context.Background(), id, asked.Model, execution, expired)
			if execution.Model != "fallback-model" {
				t.Fatalf("an unavailable configured model was retried after restart: %q", execution.Model)
			}
			if n.modelRecovery(id) == nil || probes.Load() == 0 {
				t.Fatal("restart skipped the availability probe or cleared the marker")
			}
			available.Store(true)
			execution, expired = n.heldFallbackWithExpiry(id, "claude", asked)
			execution = n.applyModelRecoveryReturn(context.Background(), id, asked.Model, execution, expired)
			if execution.Model != requested {
				t.Fatalf("the confirmed return used %q, want %q", execution.Model, requested)
			}
			if n.modelRecovery(id) != nil {
				t.Fatal("the confirmed return left the marker")
			}
		})
	}
}

// A newer explicit model choice wins even when the old configured model has already entered Returning.
func TestRecoveryReturnHonorsNewModelChoice(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, requested := range []string{"primary-model", "", "default"} {
		t.Run("requested="+requested, func(t *testing.T) {
			n, _ := recoveryTestNode(t, id, true, false)
			n.putModelRecovery(id, &proto.ModelRecovery{From: requested, ProbeFrom: "primary-model", To: "fallback-model", Returning: true})
			n.fallbacks = map[string]*assistantFallback{id: {launcher: "claude",
				from: modelExecution{Model: requested}, to: modelExecution{Model: "fallback-model"},
				at: time.Now(), until: time.Now().Add(assistantFallbackHold)}}
			other := modelExecution{Model: "other-model", IsMain: true}
			execution, expired := n.heldFallbackWithExpiry(id, "claude", other)
			execution = n.applyModelRecoveryReturn(context.Background(), id, other.Model, execution, expired)
			n.commitModelChoice(id, execution) // the direct helper models the delivery boundary in assistantSend
			if execution.Model != "other-model" {
				t.Fatalf("automatic return overrode the newer model choice with %q", execution.Model)
			}
			if n.modelRecovery(id) != nil {
				t.Fatal("the old recovery marker outlived the newer model choice")
			}
		})
	}
}

// A probe/reset-confirmed return through the real assistantSend path releases the fallback hold, sticky record and
// unavailable entry. Natural expiry above deliberately does less; confusing the two branches leaves either behavior broken.
func TestRecoveryConfirmedReturnReleasesFallbackThroughAssistantSend(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		name               string
		returning, reset   bool
		probe, expiredHold bool
	}{
		{name: "returning", returning: true},
		{name: "reset", reset: true},
		{name: "probe", probe: true},
		{name: "returning after hold expiry", returning: true, expiredHold: true},
		{name: "reset after hold expiry", reset: true, expiredHold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := recoveryTestNode(t, id, true, false)
			marker := &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota, Returning: tc.returning}
			if tc.reset {
				past := time.Now().Add(-time.Minute)
				marker.ResetsAt = &past
			}
			n.putModelRecovery(id, marker)
			n.recoveryMarks = nil // also exercise release when only session state carries the marker
			at, until := time.Now(), time.Now().Add(assistantFallbackHold)
			if tc.expiredHold {
				at, until = time.Now().Add(-assistantFallbackHold-time.Minute), time.Now().Add(-time.Minute)
			}
			n.fallbacks = map[string]*assistantFallback{id: {launcher: "claude",
				from: modelExecution{Model: "primary-model"}, to: modelExecution{Model: "fallback-model"}, at: at, until: until}}
			n.unavailable = map[string]time.Time{unavailableKey("claude", "primary-model"): time.Now().Add(time.Hour)}
			var probes atomic.Int32
			assistantModelProbe = func(_ context.Context, _ *Node, _, model string) bool {
				if model != "primary-model" {
					t.Errorf("probed %q, want primary-model", model)
				}
				probes.Add(1)
				return tc.probe
			}
			sentinel := errors.New("stop after confirmed assistantSend return")
			probeKey := n.home + "\x00claude\x00primary-model"
			modelProbeCache.Lock()
			modelProbeCache.results[probeKey] = modelProbeResult{at: time.Now(), agent: "claude", err: sentinel}
			modelProbeCache.Unlock()
			t.Cleanup(func() {
				modelProbeCache.Lock()
				delete(modelProbeCache.results, probeKey)
				modelProbeCache.Unlock()
			})
			st, _ := n.state.get(id)
			_, err := n.assistantSend(context.Background(), id, st.Cwd, "claude", "next message", nil, "", "", modelExecution{Model: "primary-model", IsMain: true})
			if !errors.Is(err, sentinel) {
				t.Fatalf("assistantSend did not reach the configured-model check: %v", err)
			}
			if n.modelRecovery(id) != nil {
				t.Fatal("the confirmed return left the recovery marker")
			}
			if st, _ := n.state.get(id); st.ModelRecovery != nil {
				t.Fatal("the confirmed return left the persisted marker")
			}
			n.mu.Lock()
			_, _, sticky := n.recentFallback(id)
			_, unavailable := n.unavailable[unavailableKey("claude", "primary-model")]
			n.mu.Unlock()
			if sticky {
				t.Fatal("the confirmed return left the fallback hold or sticky record")
			}
			if unavailable {
				t.Fatal("the confirmed return left the unavailable entry")
			}
			if tc.probe && probes.Load() == 0 {
				t.Fatal("the probe case returned without checking the configured model")
			}
		})
	}
}

// While the 30-minute hold is still active and the configured model has not answered a probe, the conversation stays
// on its fallback: the natural-expiry release must not fire early (#7824).
func TestActiveHoldIsNotReleasedEarly(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	n.fallbacks = map[string]*assistantFallback{id: {launcher: "claude",
		from:  modelExecution{Model: "primary-model"},
		to:    modelExecution{Model: "fallback-model"},
		at:    time.Now(),
		until: time.Now().Add(assistantFallbackHold)}}
	asked := modelExecution{Model: "primary-model"}
	got := n.heldFallback(id, "claude", asked)
	if got.Model != "fallback-model" {
		t.Fatalf("an active hold should keep the fallback: %q", got.Model)
	}
	backViaHold := asked.Model == "primary-model" && got.Model == "primary-model"
	if backViaHold {
		t.Fatal("natural-expiry must be false while the hold is active")
	}
	// The probe still fails in recoveryTestNode, so returnToConfiguredAt is false and nothing is released.
	if n.returnToConfiguredAt(context.Background(), id, time.Now()) {
		t.Fatal("returned early although the configured model has not answered a probe")
	}
	if n.modelRecovery(id) == nil {
		t.Fatal("the marker was released while the hold is active")
	}
}

// Answering on the configured model after a switch releases the marker from inside clearAssistantRecovery, which holds
// n.mu. That path must not re-lock n.mu (a sync.Mutex is not reentrant): before the fix it deadlocked (#7824).
func TestClearAssistantRecoveryReleasesWithoutDeadlock(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonQuota})
	// A reconnect keeps session state but starts with no in-memory recovery markers.
	n.recoveryMarks = nil
	if r := n.modelRecovery(id); r == nil {
		t.Fatal("the state-only marker is not visible before the release")
	}
	// A recovery that switched, now answered on the configured model again.
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
	n.procs[id] = p
	n.recoveries = map[string]*assistantTurnRecovery{id: {
		retried: map[string]bool{}, used: map[string]bool{}, launcher: "claude",
		from: modelExecution{Model: "primary-model"}, switched: true}}
	done := make(chan struct{})
	go func() {
		n.clearAssistantRecovery(p, true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("clearAssistantRecovery deadlocked releasing the marker under n.mu")
	}
	if n.modelRecovery(id) != nil {
		t.Fatal("answering on the configured model did not release the marker")
	}
	if st, _ := n.state.get(id); st.ModelRecovery != nil {
		t.Fatal("the released marker is still persisted")
	}
	n.mu.Lock()
	f := n.fallbacks[id]
	_, _, sticky := n.recentFallback(id)
	n.mu.Unlock()
	if f == nil || f.from.Model != "primary-model" || f.to.Model != "primary-model" || !sticky {
		t.Fatalf("the configured-model answer lost its from→from sticky hold: %+v, sticky %v", f, sticky)
	}
}

// An empty configured model is a valid launcher default, not a reason to skip configured-answer cleanup.
func TestClearAssistantRecoveryAcceptsLauncherDefault(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n, _ := recoveryTestNode(t, id, true, false)
	n.putModelRecovery(id, &proto.ModelRecovery{From: "", ProbeFrom: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonServer})
	n.unavailable = map[string]time.Time{unavailableKey("claude", "primary-model"): time.Now().Add(time.Hour)}
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: ""}}
	n.procs[id] = p
	n.recoveries = map[string]*assistantTurnRecovery{id: {
		retried: map[string]bool{}, used: map[string]bool{}, launcher: "claude",
		from: modelExecution{Model: ""}, switched: true}}
	done := make(chan struct{})
	go func() {
		n.clearAssistantRecovery(p, true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("clearAssistantRecovery deadlocked on a launcher default")
	}
	if n.modelRecovery(id) != nil {
		t.Fatal("the default marker outlived a configured-default answer")
	}
	if st, _ := n.state.get(id); st.ModelRecovery != nil {
		t.Fatal("the default marker remains persisted")
	}
	n.mu.Lock()
	_, unavailable := n.unavailable[unavailableKey("claude", "primary-model")]
	f := n.fallbacks[id]
	n.mu.Unlock()
	if unavailable {
		t.Fatal("the concrete default's unavailable record was not released")
	}
	if f == nil || f.from.Model != "" || f.to.Model != "" {
		t.Fatalf("the default answer did not preserve its sticky hold: %+v", f)
	}
}
