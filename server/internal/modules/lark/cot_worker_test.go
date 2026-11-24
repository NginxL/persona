package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestFeishuTopicBlockedNativeAllowsLaterProgressAndAnswer(t *testing.T) {
	for _, method := range []string{FeishuCotCreate, FeishuCotAppend} {
		t.Run(method, func(t *testing.T) {
			s, _, _ := cotHub(t, false)
			r, prog := topicCotRun(s)
			r.Started = time.Now().Add(-time.Minute)
			prog.from = 1
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			s.hub.(*fakeHub).answerAppBlocking(AppFeishu, method, func(json.RawMessage) (any, error) {
				select {
				case <-entered:
				default:
					close(entered)
				}
				<-release
				return map[string]any{"cot_id": "cot1", "message_id": "om_cot"}, nil
			})
			first := make(chan struct{})
			go func() { s.channelProgressTick(r.ID); close(first) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("native request did not start")
			}
			s.chans.mu.Lock()
			prog.updated = time.Now().Add(-time.Minute)
			prog.dirty = true
			prog.steps = append(prog.steps, "second progress")
			s.chans.mu.Unlock()
			s.channelProgressTick(r.ID)
			if got := len(s.hub.(*fakeHub).appParams(AppFeishu, FeishuUpdateCard)); got == 0 {
				t.Error("later progress card update was blocked by native I/O")
			}
			answered := make(chan struct{})
			go func() { s.finishChannelRun(r, &channel.Result{Answer: "Synthetic answer"}); close(answered) }()
			select {
			case <-answered:
			case <-time.After(time.Second):
				unblock()
				<-first
				<-answered
				t.Fatal("answer waited for native I/O")
			}
			s.chans.mu.Lock()
			w := prog.native()
			s.chans.mu.Unlock()
			unblock()
			<-first
			if w != nil {
				select {
				case <-w.done:
				case <-time.After(2 * time.Second):
					t.Fatal("native worker did not end")
				}
			}
		})
	}
}

// nativeCancelHub models a transport that respects the request context, unlike
// the late-response fake above. Only its first selected request blocks.
type nativeCancelHub struct {
	wrap func(error) error
	Hub
	method   string
	entered  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (h *nativeCancelHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	blocked := false
	if p.AppID == AppFeishu && p.Method == h.method {
		h.once.Do(func() { blocked = true; close(h.entered) })
	}
	if blocked {
		<-ctx.Done()
		close(h.canceled)
		if h.wrap != nil {
			return nil, h.wrap(ctx.Err())
		}
		return nil, ctx.Err()
	}
	return h.Hub.AppCall(ctx, p)
}

func TestFeishuTopicFinishCancelsNativeProgress(t *testing.T) {
	for _, method := range []string{FeishuCotCreate, FeishuCotAppend} {
		t.Run(method, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			r, p := topicCotRun(s)
			h := &nativeCancelHub{Hub: s.hub, method: method, entered: make(chan struct{}), canceled: make(chan struct{})}
			s.hub = h
			s.sink().progress(context.Background(), r, time.Second, nil, "")
			select {
			case <-h.entered:
			case <-time.After(time.Second):
				t.Fatal("native request did not start")
			}
			s.finishChannelRun(r, &channel.Result{Answer: "Synthetic answer"})
			select {
			case <-h.canceled:
			case <-time.After(time.Second):
				t.Fatal("finish did not cancel native progress")
			}
			select {
			case <-p.native().done:
			case <-time.After(2 * time.Second):
				t.Fatal("native worker did not finish")
			}
			answers := 0
			for _, post := range *posts {
				if card, _ := post["card"].(string); strings.Contains(card, "Synthetic answer") {
					answers++
				}
			}
			if answers != 1 {
				t.Fatalf("answer deliveries = %d", answers)
			}
			f := h.Hub.(*fakeHub)
			if method == FeishuCotCreate && len(f.appParams(AppFeishu, FeishuCotCreate)) != 0 {
				t.Fatal("ambiguous canceled creation was retried")
			}
			if method == FeishuCotAppend {
				// The canceled batch is not replayed; the known message still receives its end.
				if got := len(f.appParams(AppFeishu, FeishuCotAppend)); got != 1 {
					t.Fatalf("appends after cancellation = %d, want only terminal batch", got)
				}
			}
		})
	}
}

func TestFeishuTopicNativeWorkerCoalescesOrderedSnapshots(t *testing.T) {
	s, _, calls := cotHub(t, false)
	r, p := topicCotRun(s)
	f := s.hub.(*fakeHub)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	original := f.apps[AppFeishu+" "+FeishuCotAppend]
	var first sync.Once
	f.answerAppBlocking(AppFeishu, FeishuCotAppend, func(raw json.RawMessage) (any, error) {
		block := false
		first.Do(func() { block = true; close(entered) })
		if block {
			<-release
		}
		return original(raw)
	})
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native append did not start")
	}
	for i := 0; i < 60; i++ {
		s.chans.mu.Lock()
		p.items = append(p.items, proto.Item{ID: fmt.Sprintf("think-%d", i), Kind: proto.KindThinking, Role: "assistant", Text: fmt.Sprintf("ordered-%d", i)})
		s.chans.mu.Unlock()
		s.sink().progress(context.Background(), r, time.Duration(i+2)*time.Second, nil, "")
	}
	s.chans.mu.Lock()
	w := p.native()
	s.chans.mu.Unlock()
	w.mu.Lock()
	if !w.queued || len(w.pending) != len(p.items) || len(w.wake) > 1 {
		t.Errorf("pending snapshot was not coalesced: items=%d wake=%d", len(w.pending), len(w.wake))
	}
	w.mu.Unlock()
	unblock()
	waitTopicCot(t, s, r)
	finishTopicCot(t, s, r, &channel.Result{Answer: "Synthetic answer"})
	var all string
	for _, params := range f.appParams(AppFeishu, FeishuCotAppend) {
		raw, _ := json.Marshal(params["events"])
		all += string(raw)
	}
	last := -1
	for i := 0; i < 60; i++ {
		needle := fmt.Sprintf(`ordered-%d\"`, i)
		pos := strings.Index(all, needle)
		if pos <= last || strings.Count(all, needle) != 1 {
			t.Fatalf("native event %d missing, repeated or reordered", i)
		}
		last = pos
	}
	if got := strings.Join(*calls, " "); strings.Count(got, "RUN_STARTED") != 1 || strings.Count(got, "RUN_FINISHED") != 1 {
		t.Fatalf("native lifecycle = %s", got)
	}
	if n := len(f.appParams(AppFeishu, FeishuCotAppend)); n > 7 {
		t.Fatalf("burst produced %d appends; pending ticks were not bounded", n)
	}
}

func TestFeishuTopicLateNativeResponseCannotMutateHeldNextTurn(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	f := s.hub.(*fakeHub)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var attempt int
	f.answerAppBlocking(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
		attempt++
		if attempt == 1 {
			close(entered)
			<-release
			return map[string]any{"cot_id": "old-cot", "message_id": "old-message"}, nil
		}
		return map[string]any{"cot_id": "next-cot", "message_id": "next-message"}, nil
	})
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old native create did not start")
	}
	s.chans.mu.Lock()
	old := p.native()
	s.chans.mu.Unlock()
	s.finishChannelRun(r, &channel.Result{Answer: "Synthetic partial answer", Held: true})
	s.chans.mu.Lock()
	if p.native() != nil {
		t.Error("held turn retained old native worker")
	}
	p.items = []proto.Item{{ID: "next", Kind: proto.KindThinking, Role: "assistant", Text: "Synthetic next turn"}}
	s.chans.mu.Unlock()
	// A late result from the previous turn must be fenced even while the next run
	// reuses the same Run and chanLive pointers.
	unblock()
	select {
	case <-old.done:
	case <-time.After(2 * time.Second):
		t.Fatal("old native finish did not end")
	}
	for _, key := range []string{"cot", "cot_msg", "cot_para", "cot_done", "cot_off"} {
		if value := s.runExt(r, key); value != "" {
			t.Fatalf("late old-turn write: %s=%q", key, value)
		}
	}
	topicCotProgress(t, s, r, time.Second, nil, "")
	if got := s.runExt(r, "cot"); got != "next-cot" {
		t.Fatalf("next turn native ID = %q", got)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "Synthetic final answer"})
	var ids []string
	for _, params := range f.appParams(AppFeishu, FeishuCotAppend) {
		ids = append(ids, params["cot_id"].(string))
	}
	if len(ids) < 2 || ids[0] != "old-cot" || ids[len(ids)-1] != "next-cot" {
		t.Fatalf("native turn identities = %v", ids)
	}
}

func TestFeishuTopicQuickAnswerDoesNotWaitForNativeCreation(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	f := s.hub.(*fakeHub)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.answerAppBlocking(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
		close(entered)
		<-release
		return map[string]any{"cot_id": "cot1", "message_id": "om_cot"}, nil
	})
	answerDone := make(chan struct{})
	go func() { s.finishChannelRun(r, &channel.Result{Answer: "Synthetic quick answer"}); close(answerDone) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("quick turn did not start native creation")
	}
	select {
	case <-answerDone:
	case <-time.After(time.Second):
		unblock()
		<-answerDone
		t.Fatal("quick answer waited for native creation")
	}
	if len(*posts) != 1 || !strings.Contains((*posts)[0]["card"].(string), "Synthetic quick answer") {
		t.Fatalf("quick answer = %v", *posts)
	}
	unblock()
	select {
	case <-p.native().done:
	case <-time.After(2 * time.Second):
		t.Fatal("quick turn native worker did not end")
	}
	if got := len(f.appParams(AppFeishu, FeishuCotCreate)); got != 1 {
		t.Fatalf("native creates = %d", got)
	}
}

func TestFeishuTopicRemovalCancelsAndFencesLateCreation(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, initial := topicCotRun(s)
	s.chans.mu.Lock()
	delete(s.chans.live, r.ID)
	s.chans.mu.Unlock()
	watched := make(chan struct{})
	go func() { s.watchChannelRun(r); close(watched) }()
	var p *chanLive
	waitFor(t, "watcher registration", func() bool {
		_, p = s.channelRunByID(r.ID)
		return p != nil
	})
	s.chans.mu.Lock()
	p.items = initial.items
	s.chans.mu.Unlock()
	f := s.hub.(*fakeHub)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.answerAppBlocking(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
		close(entered)
		<-release
		return map[string]any{"cot_id": "late-cot", "message_id": "late-message"}, nil
	})
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native create did not start")
	}
	s.chans.mu.Lock()
	w := p.native()
	s.chans.st.Runs = nil
	s.chans.mu.Unlock()
	// The real watcher cancels native work before returning.
	select {
	case <-watched:
	case <-time.After(2 * time.Second):
		t.Fatal("removed run watcher did not stop")
	}
	w.mu.Lock()
	aborted := w.aborted
	w.mu.Unlock()
	if !aborted {
		t.Error("removed run did not cancel native work")
	}
	unblock()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("removed native worker did not end")
	}
	if s.runExt(r, "cot") != "" {
		t.Fatal("late create mutated a removed run")
	}
	if got := len(f.appParams(AppFeishu, FeishuCotAppend)); got != 0 {
		t.Fatalf("removed run appended %d batches", got)
	}
	if got := len(f.appParams(AppFeishu, FeishuCotComplete)); got != 1 {
		t.Fatalf("late known message was completed %d times", got)
	}
}

func TestFeishuTopicAmbiguousNativeCreationIsNotRetried(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			r, _ := topicCotRun(s)
			f := s.hub.(*fakeHub)
			f.answerApp(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) { return nil, failure })
			topicCotProgress(t, s, r, time.Second, nil, "")
			topicCotProgress(t, s, r, 2*time.Second, nil, "")
			finishTopicCot(t, s, r, &channel.Result{Answer: "Synthetic fallback answer"})
			if got := len(f.appParams(AppFeishu, FeishuCotCreate)); got != 1 {
				t.Fatalf("ambiguous creation attempts = %d", got)
			}
			if len(f.appParams(AppFeishu, FeishuCotAppend)) != 0 {
				t.Fatal("ambiguous creation emitted native events")
			}
			card := lastCard(t, r, *posts)
			if !strings.Contains(card, "Synthetic fallback answer") || !strings.Contains(card, "collapsible_panel") {
				t.Fatalf("fallback card = %s", card)
			}
		})
	}
}

func TestFeishuTopicRealFoldHasOneSerializedNativeEnd(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	withCot(s, r)
	f := s.hub.(*fakeHub)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.answerAppBlocking(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { close(entered); <-release; return map[string]any{}, nil })
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("append did not start")
	}
	next := *r
	next.ID, next.ReplyTo = "fb_next", "om_next"
	next.Ext = map[string]string{"chat": testChat, "thread": testThread}
	next.Started = time.Now()
	folded := make(chan struct{})
	go func() { s.startChannelRun(&next); close(folded) }()
	select {
	case <-folded:
	case <-time.After(time.Second):
		unblock()
		<-folded
		t.Fatal("fold waited for native append")
	}
	if got := len(f.appParams(AppFeishu, FeishuCotComplete)); got != 0 {
		t.Fatalf("fold completed native message %d times before append returned", got)
	}
	var nextProgress *chanLive
	waitFor(t, "next run watcher registration", func() bool {
		_, nextProgress = s.channelRunByID(next.ID)
		return nextProgress != nil
	})
	s.sink().progress(context.Background(), &next, time.Second, nil, "")
	finishTopicCot(t, s, &next, &channel.Result{Answer: "Synthetic next answer"})
	if got := len(f.appParams(AppFeishu, FeishuCotComplete)); got != 0 {
		t.Fatalf("next answer raced native end: completes=%d", got)
	}
	unblock()
	select {
	case <-p.native().done:
	case <-time.After(2 * time.Second):
		t.Fatal("folded native worker did not end")
	}
	if got := len(f.appParams(AppFeishu, FeishuCotComplete)); got != 1 {
		t.Fatalf("fold native complete calls = %d", got)
	}
}

func TestFeishuTopicCanceledAppendRespectsUnsentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wrap   func(error) error
		replay bool
	}{
		{"uncertain", func(err error) error { return err }, false},
		{"not sent", rpc.NotSent, true},
		{"not written", func(err error) error { return errors.Join(rpc.ErrNotWritten, err) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, calls := cotHub(t, false)
			r, p := topicCotRun(s)
			withCot(s, r)
			h := &nativeCancelHub{Hub: s.hub, method: FeishuCotAppend, entered: make(chan struct{}), canceled: make(chan struct{}), wrap: tc.wrap}
			s.hub = h
			s.sink().progress(context.Background(), r, time.Second, nil, "")
			select {
			case <-h.entered:
			case <-time.After(time.Second):
				t.Fatal("append did not start")
			}
			s.finishChannelRun(r, &channel.Result{Answer: "Synthetic answer"})
			select {
			case <-p.native().done:
			case <-time.After(2 * time.Second):
				t.Fatal("native end did not finish")
			}
			got := strings.Join(*calls, " ")
			if strings.Contains(got, "RUN_STARTED") != tc.replay || strings.Count(got, "RUN_FINISHED") != 1 {
				t.Fatalf("native events = %s", got)
			}
			if n := len(h.Hub.(*fakeHub).appParams(AppFeishu, FeishuCotAppend)); n != 1 {
				t.Fatalf("actually sent appends = %d", n)
			}
		})
	}
}

func TestFeishuTopicTerminalRefusalRepairsSameCardProcess(t *testing.T) {
	for _, mode := range []string{"answer", "quiet", "new card"} {
		t.Run(mode, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			r, p := topicCotRun(s)
			f := s.hub.(*fakeHub)
			if mode == "new card" {
				f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
					return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Main: true, Cards: true, TopicCot: true, AnswerNewCard: true}}, nil
				})
			}
			topicCotProgress(t, s, r, time.Second, nil, "")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			f.answerAppBlocking(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { close(entered); <-release; return nil, feishuAPIError(99991672) })
			result := &channel.Result{Answer: "Synthetic final answer"}
			if mode == "quiet" {
				result = &channel.Result{Quiet: true}
			}
			answered := make(chan struct{})
			go func() { s.finishChannelRun(r, result); close(answered) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("terminal native append did not start")
			}
			select {
			case <-answered:
			case <-time.After(time.Second):
				unblock()
				<-answered
				t.Fatal("answer waited for terminal native refusal")
			}
			sends := len(f.appParams(AppFeishu, FeishuPost))
			unblock()
			select {
			case <-p.native().done:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal fallback did not finish")
			}
			if got := len(f.appParams(AppFeishu, FeishuPost)); got != sends {
				t.Fatalf("fallback posted another answer: before=%d after=%d", sends, got)
			}
			last := (*posts)[len(*posts)-1]
			card, _ := last["card"].(string)
			if last["update"] != true || last["message_id"] != "om_card" || !strings.Contains(card, "collapsible_panel") {
				t.Fatalf("same-card fallback = %v", last)
			}
			if mode == "answer" && !strings.Contains(card, "Synthetic final answer") {
				t.Fatal("fallback lost the final answer")
			}
		})
	}
}

// nativeCheckpoint holds the first save of the channels' state that carries an agent message (the core's checkpoint
// of the runs, saved under the channels' lock) until released.
type nativeCheckpoint struct {
	entered, release chan struct{}
	once             sync.Once
}

func (st *nativeCheckpoint) save(runs []*channel.Run) error {
	for _, r := range runs {
		if r.Ext["cot"] != "" {
			block := false
			st.once.Do(func() { block = true; close(st.entered) })
			if block {
				<-st.release
			}
		}
	}
	return nil
}

func TestFeishuTopicNativeCheckpointDoesNotHoldWorkerLock(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	st := &nativeCheckpoint{entered: make(chan struct{}), release: make(chan struct{})}
	s.save = st.save
	var once sync.Once
	unblock := func() { once.Do(func() { close(st.release) }) }
	defer unblock()
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	w := p.native() // set before progress returned; immutable for this turn
	select {
	case <-st.entered:
	case <-time.After(time.Second):
		t.Fatal("native checkpoint did not start")
	}
	if !w.mu.TryLock() {
		unblock()
		t.Fatal("worker mutex held over existing checkpoint I/O")
	}
	w.mu.Unlock()
	// The pre-existing whole-channel checkpoint still owns chans.mu. This patch
	// preserves that ordering instead of introducing an unsafe second writer.
	if s.chans.mu.TryLock() {
		s.chans.mu.Unlock()
		t.Error("fixture did not reach the existing ordered storage boundary")
	}
	canceled := make(chan struct{})
	go func() { w.abort(); close(canceled) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		unblock()
		t.Fatal("slow checkpoint prevented native cancellation")
	}
	unblock()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled native worker did not end")
	}
}

func TestFeishuTopicDroppedMembershipRejectsLateNativeState(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	f := s.hub.(*fakeHub)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.answerAppBlocking(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
		close(entered)
		<-release
		return map[string]any{"cot_id": "late-cot", "message_id": "late-message"}, nil
	})
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native create did not start")
	}
	s.chans.mu.Lock()
	w := p.native()
	s.chans.st.Runs = nil // durable drop before the watcher removes its live entry
	if err := s.saveChannelStateLocked(); err != nil {
		t.Error(err)
	}
	s.chans.mu.Unlock()
	unblock()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("dropped generation did not stop")
	}
	if s.runExt(r, "cot") != "" {
		t.Fatal("late response mutated the dropped run")
	}
	s.chans.mu.Lock()
	saved := slices.Clone(s.chans.st.Runs)
	s.chans.mu.Unlock()
	if len(saved) != 0 {
		t.Fatalf("late response resurrected durable runs: %v", saved)
	}
	if got := len(f.appParams(AppFeishu, FeishuCotAppend)); got != 0 {
		t.Fatalf("dropped run appended %d batches", got)
	}
	if got := len(f.appParams(AppFeishu, FeishuCotComplete)); got != 1 {
		t.Fatalf("late known message completion = %d", got)
	}
}

func TestFeishuTopicTerminalRefusalKeepsSuccessfulAnswerRetryTarget(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	f := s.hub.(*fakeHub)
	topicCotProgress(t, s, r, time.Second, nil, "")
	originalPost := f.apps[AppFeishu+" "+FeishuPost]
	originalUpdate := f.apps[AppFeishu+" "+FeishuUpdateCard]
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.answerAppBlocking(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { close(entered); <-release; return nil, feishuAPIError(99991672) })
	fail := func(json.RawMessage) (any, error) { return nil, rpc.ErrClosed }
	f.answerApp(AppFeishu, FeishuPost, fail)
	f.answerApp(AppFeishu, FeishuUpdateCard, fail)
	result := &channel.Result{Answer: "Synthetic retried answer"}
	s.finishChannelRun(r, result)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("terminal append did not start")
	}
	if current, _ := s.channelRunByID(r.ID); current == nil {
		t.Fatal("failed answer was not retained for retry")
	}
	f.answerApp(AppFeishu, FeishuPost, originalPost)
	f.answerApp(AppFeishu, FeishuUpdateCard, originalUpdate)
	s.finishChannelRun(r, result)
	if current, _ := s.channelRunByID(r.ID); current != nil {
		t.Fatal("successful retry did not end the run")
	}
	sends := len(f.appParams(AppFeishu, FeishuPost))
	unblock()
	select {
	case <-p.native().done:
	case <-time.After(2 * time.Second):
		t.Fatal("native fallback did not finish after retry")
	}
	if got := len(f.appParams(AppFeishu, FeishuPost)); got != sends {
		t.Fatalf("native fallback posted another answer: %d -> %d", sends, got)
	}
	last := (*posts)[len(*posts)-1]
	card, _ := last["card"].(string)
	if last["update"] != true || !strings.Contains(card, "Synthetic retried answer") || !strings.Contains(card, "collapsible_panel") {
		t.Fatalf("retried answer fallback = %v", last)
	}
}

func TestFeishuTopicTerminalRefusalWithoutDeliveredAnswerStops(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	r, p := topicCotRun(s)
	f := s.hub.(*fakeHub)
	topicCotProgress(t, s, r, time.Second, nil, "")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.answerAppBlocking(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { close(entered); <-release; return nil, feishuAPIError(99991672) })
	fail := func(json.RawMessage) (any, error) { return nil, rpc.ErrClosed }
	f.answerApp(AppFeishu, FeishuPost, fail)
	f.answerApp(AppFeishu, FeishuUpdateCard, fail)
	result := &channel.Result{Answer: "Synthetic undelivered answer"}
	s.finishChannelRun(r, result)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("terminal append did not start")
	}
	p.mu.Lock()
	p.failing = time.Now().Add(-2 * channelAnswerTries)
	p.mu.Unlock()
	s.finishChannelRun(r, result)
	if current, _ := s.channelRunByID(r.ID); current != nil {
		t.Fatal("expired answer retry kept the run")
	}
	unblock()
	select {
	case <-p.native().done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker waited for an answer target after the run ended")
	}
	if len(*posts) != 1 {
		t.Fatalf("undelivered answer produced extra visible cards: %v", *posts)
	}
}
