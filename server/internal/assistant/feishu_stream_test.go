package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

// kitCall is one CardKit call the assistant made (feishu.card_*), its method's last word and its params.
type kitCall struct {
	op string
	p  map[string]any
}

// kitHub is cotHub with CardKit: it makes entities "ck1", "ck2"…, records every write, and fails a write when fail
// says so (by the call's op and how many of that op came before).
type kitHub struct {
	mu    sync.Mutex
	calls []kitCall
	fail  func(op string, n int) error
}

func (k *kitHub) ops() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for _, c := range k.calls {
		out = append(out, c.op)
	}
	return out
}

func (k *kitHub) last(op string) map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := len(k.calls) - 1; i >= 0; i-- {
		if k.calls[i].op == op {
			return k.calls[i].p
		}
	}
	return nil
}

func streamHub(t *testing.T) (*Service, *[]map[string]any, *kitHub) {
	t.Helper()
	s, posts, _ := cotHub(t, false)
	s.setLarkTiming("kit_check_every", kitCheckEvery)
	k := &kitHub{}
	f := s.hub.(*fakeHub)
	for _, m := range []struct{ method, op string }{{FeishuCardCreate, "create"}, {FeishuCardUpdate, "update"},
		{FeishuCardStream, "stream"}, {FeishuCardSettings, "settings"}} {
		f.answerApp(AppFeishu, m.method, func(raw json.RawMessage) (any, error) {
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			k.mu.Lock()
			n := 0
			for _, c := range k.calls {
				if c.op == m.op {
					n++
				}
			}
			k.calls = append(k.calls, kitCall{m.op, p})
			fail := k.fail
			k.mu.Unlock()
			if fail != nil {
				if err := fail(m.op, n); err != nil {
					return nil, err
				}
			}
			if m.op == "create" {
				return map[string]any{"card_id": "ck1"}, nil
			}
			return map[string]any{}, nil
		})
	}
	return s, posts, k
}

func seqOf(p map[string]any) int64 {
	n, _ := p["sequence"].(float64)
	return int64(n)
}

// A run's progress card is a card entity: made with its live text in an element of its own and streaming on, sent by
// its id, and remembered by the run.
func TestProgressCardIsACardEntity(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "我先看一下")
	if got := k.ops(); strings.Join(got, ",") != "create" {
		t.Fatalf("calls %v, want the entity made", got)
	}
	card, _ := k.last("create")["card"].(string)
	for _, want := range []string{`"element_id":"live"`, `"streaming_mode":true`, "我先看一下", "运行"} {
		if !strings.Contains(card, want) {
			t.Errorf("the entity lacks %s: %s", want, card)
		}
	}
	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	if sent, _ := (*posts)[0]["card"].(string); !strings.Contains(sent, `"card_id":"ck1"`) || !strings.Contains(sent, `"type":"card"`) {
		t.Fatalf("sent %s, want the entity by its id", sent)
	}
	if r.Ext["card"] != "om_card" || r.Ext["card_id"] != "ck1" {
		t.Fatalf("ext %v", r.Ext)
	}
}

// What the conversation writes is streamed: only the text goes, letter by letter, while nothing else on the card
// changes; a step (or the time it has taken) that changes the card writes it whole (its text as already streamed),
// then streams on. Every write's sequence is larger than the one before.
func TestProgressStreamsTheText(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	r, _ := cotRun(s)
	sink := feishuSink{s}
	ctx := context.Background()
	steps := []string{"运行 `ls`"}
	sink.progress(ctx, r, time.Second, steps, "我先")
	sink.progress(ctx, r, time.Second, steps, "我先看一下")
	if got := strings.Join(k.ops(), ","); got != "create,stream" {
		t.Fatalf("calls %s, want the text streamed alone", got)
	}
	st := k.last("stream")
	if st["card_id"] != "ck1" || st["element"] != "live" || st["text"] != "我先看一下" {
		t.Fatalf("stream %v", st)
	}
	sink.progress(ctx, r, time.Second, steps, "我先看一下")
	if got := strings.Join(k.ops(), ","); got != "create,stream" {
		t.Fatalf("calls %s, want nothing for nothing new", got)
	}
	steps = append(steps, "读取 `go.mod`")
	sink.progress(ctx, r, 4*time.Second, steps, "我先看一下，再改")
	if got := strings.Join(k.ops(), ","); got != "create,stream,update,stream" {
		t.Fatalf("calls %s, want the card written whole and the text streamed on", got)
	}
	up, _ := k.last("update")["card"].(string)
	if !strings.Contains(up, "go.mod") || !strings.Contains(up, `"content":"我先看一下"`) || !strings.Contains(up, `"streaming_mode":true`) {
		t.Fatalf("update %s, want the new step and the text as it was streamed", up)
	}
	var last int64
	k.mu.Lock()
	for _, c := range k.calls[1:] {
		if n := seqOf(c.p); n <= last {
			t.Errorf("sequence %d after %d", n, last)
		} else {
			last = n
		}
	}
	k.mu.Unlock()
	if len(*posts) != 1 {
		t.Fatalf("posts %v, want the one card", *posts)
	}
}

// CardKit turns streaming off ten minutes after it was turned on: it is turned on again and the text written.
func TestProgressTurnsAClosedStreamOnAgain(t *testing.T) {
	needsLarkLayer(t)
	s, _, k := streamHub(t)
	r, _ := cotRun(s)
	ctx := context.Background()
	feishuSink{s}.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先")
	k.fail = func(op string, n int) error {
		if op == "stream" && n == 0 {
			return feishuAPIError(300309)
		}
		return nil
	}
	feishuSink{s}.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先看")
	if got := strings.Join(k.ops(), ","); got != "create,stream,settings,stream" {
		t.Fatalf("calls %s", got)
	}
	if on := k.last("settings")["streaming"]; on != true {
		t.Fatalf("settings %v", k.last("settings"))
	}
}

// Any other failure of CardKit sends the run back to updating its card as a message, which a card sent as an entity
// still takes: the text is not lost and nothing waits on CardKit.
func TestProgressFallsBackToTheMessage(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	r, _ := cotRun(s)
	ctx := context.Background()
	feishuSink{s}.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先")
	k.fail = func(op string, _ int) error {
		if op == "stream" {
			return errors.New("feishu stream card: 300500 internal error")
		}
		return nil
	}
	feishuSink{s}.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先看")
	feishuSink{s}.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先看一下")
	if got := strings.Join(k.ops(), ","); got != "create,stream" {
		t.Fatalf("calls %s, want CardKit left after its failure", got)
	}
	var updates []string
	for _, p := range *posts {
		if p["update"] == true {
			c, _ := p["card"].(string)
			updates = append(updates, c)
		}
	}
	if len(updates) != 2 || !strings.Contains(updates[1], "我先看一下") {
		t.Fatalf("updates %v, want the message updated with the text", updates)
	}
}

// Without CardKit (the app lacks its permission, an older hub) the card is sent and updated as before.
func TestProgressWithoutCardKit(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	k.fail = func(op string, _ int) error {
		if op == "create" {
			return feishuAPIError(99991672)
		}
		return nil
	}
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "我先")
	if len(*posts) != 1 || r.Ext["card"] == "" || r.Ext["card_id"] != "" {
		t.Fatalf("posts %v, ext %v", *posts, r.Ext)
	}
	if sent, _ := (*posts)[0]["card"].(string); !strings.Contains(sent, `"schema":"2.0"`) || strings.Contains(sent, "streaming_mode") {
		t.Fatalf("sent %s, want the card itself", sent)
	}
	// A second run does not try CardKit again for a while.
	r2, _ := cotRun(s)
	r2.ID = "fb_m2"
	feishuSink{s}.progress(context.Background(), r2, time.Second, nil, "")
	if n := len(k.ops()); n != 1 {
		t.Fatalf("calls %v, want no second try so soon", k.ops())
	}
}

// The answer ends the streaming before it goes into the card, which then is the answer as any other.
func TestAnswerEndsTheStream(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "我先")
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if got := strings.Join(k.ops(), ","); got != "create,settings" {
		t.Fatalf("calls %s, want the streaming ended", got)
	}
	if on := k.last("settings")["streaming"]; on != false {
		t.Fatalf("settings %v", k.last("settings"))
	}
	last := (*posts)[len(*posts)-1]
	if c, _ := last["card"].(string); last["update"] != true || last["message_id"] != "om_card" || !strings.Contains(c, "结论在这里") {
		t.Fatalf("answer %v", last)
	}
}

// The progress card says its state with a coloured tag, not an hourglass.
func TestProgressCardLooks(t *testing.T) {
	c := progressCard("zh-Hans", "Persona", 12*time.Second, []string{"运行 `ls`"}, "", "", liveView{}, botTarget{Kind: targetPersona})
	js := c.JSON()
	if strings.Contains(js, "⏳") || !strings.Contains(js, "<text_tag color='blue'>") {
		t.Fatalf("progress card %s", js)
	}
	q := progressCard("zh-Hans", "Persona", time.Second, nil, "", "queued", liveView{}, botTarget{}).JSON()
	if !strings.Contains(q, "<text_tag color='neutral'>") {
		t.Fatalf("queued card %s", q)
	}
}

// answerMessages answers feishu.messages (reading the progress card's message back) with fn, counting the reads.
func answerMessages(s *Service, fn func() (any, error)) *int {
	n := 0
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
		n++
		return fn()
	})
	return &n
}

// CardKit goes on taking writes into an entity whose message was withdrawn or deleted, so the message is read back
// every kitCheckEvery: one gone is forgotten, entity and all, and the next tick posts a new card, as a message update
// that finds it gone does.
func TestProgressNoticesAWithdrawnEntity(t *testing.T) {
	needsLarkLayer(t)
	for _, tc := range []struct {
		name string
		read func() (any, error)
	}{
		{"deleted", func() (any, error) {
			return map[string]any{"items": []map[string]any{{"message_id": "om_card", "deleted": true}}}, nil
		}},
		{"withdrawn", func() (any, error) {
			return nil, errors.New("feishu read message: 230011 The message was withdrawn.")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, posts, k := streamHub(t)
			now := time.Unix(1_800_000_000, 0)
			s.chans.now = func() time.Time { return now }
			reads := answerMessages(s, tc.read)
			r, _ := cotRun(s)
			sink, ctx, steps := feishuSink{s}, context.Background(), []string{"运行 `ls`"}
			sink.progress(ctx, r, time.Second, steps, "我先")
			now = now.Add(5 * time.Second)
			sink.progress(ctx, r, time.Second, steps, "我先看")
			if *reads != 0 || strings.Join(k.ops(), ",") != "create,stream" {
				t.Fatalf("reads %d, calls %v: the message is not read before kitCheckEvery", *reads, k.ops())
			}
			now = now.Add(kitCheckEvery)
			sink.progress(ctx, r, time.Second, steps, "我先看一下")
			if *reads != 1 || strings.Join(k.ops(), ",") != "create,stream" {
				t.Fatalf("reads %d, calls %v: want the message read and nothing written into a gone card", *reads, k.ops())
			}
			if r.Ext["card"] != "" || r.Ext["card_id"] != "" {
				t.Fatalf("ext %v: want the card and its entity forgotten", r.Ext)
			}
			sink.progress(ctx, r, time.Second, steps, "我先看一下")
			if strings.Join(k.ops(), ",") != "create,stream,create" || len(*posts) != 2 || r.Ext["card"] == "" {
				t.Fatalf("calls %v, posts %d, ext %v: want a new card", k.ops(), len(*posts), r.Ext)
			}
		})
	}
}

// A read that fails for another reason (a rate limit, the hub away) says nothing of the card: it goes on streaming.
func TestProgressKeepsTheEntityWhenTheReadFails(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	now := time.Unix(1_800_000_000, 0)
	s.chans.now = func() time.Time { return now }
	reads := answerMessages(s, func() (any, error) {
		return nil, errors.New("feishu read message: 99991400 request trigger frequency limit")
	})
	r, _ := cotRun(s)
	sink, ctx, steps := feishuSink{s}, context.Background(), []string{"运行 `ls`"}
	sink.progress(ctx, r, time.Second, steps, "我先")
	now = now.Add(kitCheckEvery)
	sink.progress(ctx, r, time.Second, steps, "我先看")
	if *reads != 1 || strings.Join(k.ops(), ",") != "create,stream" || len(*posts) != 1 || r.Ext["card_id"] != "ck1" {
		t.Fatalf("reads %d, calls %v, posts %d, ext %v", *reads, k.ops(), len(*posts), r.Ext)
	}
}

// A progress card sent as a message has no entity, even when an earlier card of the run had one: the old card_id is
// dropped, so its later ticks update the message rather than stream into a card nobody sees (also after a restart).
func TestProgressAsAMessageForgetsAnOldEntity(t *testing.T) {
	needsLarkLayer(t)
	s, posts, k := streamHub(t)
	k.fail = func(op string, _ int) error {
		if op == "create" {
			return feishuAPIError(99991672)
		}
		return nil
	}
	r, _ := cotRun(s)
	s.setRunExt(r, "card_id", "ck_old")
	sink, ctx := feishuSink{s}, context.Background()
	sink.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先")
	if len(*posts) != 1 || r.Ext["card"] == "" || r.Ext["card_id"] != "" {
		t.Fatalf("posts %v, ext %v: want the card as a message and no entity", *posts, r.Ext)
	}
	sink.progress(ctx, r, time.Second, []string{"运行 `ls`"}, "我先看")
	if got := strings.Join(k.ops(), ","); got != "create" {
		t.Fatalf("calls %s: want nothing streamed into the old entity", got)
	}
	if last := (*posts)[len(*posts)-1]; last["update"] != true {
		t.Fatalf("last %v: want the message updated", last)
	}
}

// kitCheckEvery is the lark module's kit_check_every, which these tests read the entity's message back by.
const kitCheckEvery = 30 * time.Second
