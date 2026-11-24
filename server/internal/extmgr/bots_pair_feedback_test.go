package extmgr

import (
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock replaces the bot hub's clock with an atomic one the test can push forward.
func fakeBotClock(w *botWorld) *atomic.Int64 {
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	w.h.bots.now = func() time.Time { return time.Unix(0, now.Load()) }
	return &now
}

func waitCount(t *testing.T, f *fakeOpenAPI, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.count(want) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("count of %q never reached %d (got %d)", want, n, f.count(want))
}

// assertCountStays asserts the number of calls containing want does not change for a moment: a throttled message
// must not slip out after a short wait.
func assertCountStays(t *testing.T, f *fakeOpenAPI, want string, n int) {
	t.Helper()
	for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); {
		if got := f.count(want); got != n {
			t.Fatalf("count of %q = %d, want it to stay %d", want, got, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A wrong code gets a specific reply with the remaining tries; the short window only absorbs a redelivery, then the
// next legitimate try is answered again. At the cap the code is declared dead, and afterwards the generic hint is
// back.
func TestBotPairWrongCodeFeedbackAndShortThrottle(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	now := fakeBotClock(w)
	advance := func(d time.Duration) { now.Store(now.Load() + int64(d)) }
	send := func(id, text string) {
		_, _ = w.h.botEvent(w.ctx, botMessageEvent(id, "ou_me", "text", textContent(text), nil))
	}

	_, out := w.do(t, "POST", "/api/v1/bots/feishu/pair", "")
	if out["pairing"] == nil {
		t.Fatalf("no pairing: %v", out)
	}
	// Not a code-shaped answer: the generic hint, through the same short window.
	send("om_hi", "你好")
	waitCount(t, w.api, "还没配对", 1)

	advance(botPairTellFor + time.Second)
	send("om_w1", "111111")
	waitCount(t, w.api, "还可试 4 次", 1)
	// A redelivery of the same text inside the window is fully ignored: no second note and no second guess.
	send("om_w1_redeliver", "111111")
	assertCountStays(t, w.api, "验证码不对", 1)

	advance(botPairTellFor + time.Second)
	send("om_w2", "222222")
	waitCount(t, w.api, "还可试 3 次", 1)
	advance(botPairTellFor + time.Second)
	send("om_w3", "333333")
	waitCount(t, w.api, "还可试 2 次", 1)
	advance(botPairTellFor + time.Second)
	send("om_w4", "444444")
	waitCount(t, w.api, "还可试 1 次", 1)
	advance(botPairTellFor + time.Second)
	send("om_w5", "555555")
	waitCount(t, w.api, "验证码已失效", 1)

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, got := w.do(t, "GET", "/api/v1/bots/feishu", "")
		if got["pairing"] == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pairing survived %d wrong codes: %v", botPairTries, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// With no live pairing, a code-shaped answer gets the generic hint again.
	advance(botPairTellFor + time.Second)
	send("om_w6", "777777")
	waitCount(t, w.api, "还没配对", 2)
}

// A time-expired pairing is not a wrong code: even the correct code gets the generic hint, does not burn a failure,
// and does not clear the pairing (only the cap, success, unpair and re-pair do).
func TestBotPairTimeExpiredCodeKeepsTheHint(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	now := fakeBotClock(w)
	_, out := w.do(t, "POST", "/api/v1/bots/feishu/pair", "")
	code := out["pairing"].(map[string]any)["code"].(string)
	now.Store(now.Load() + int64(botPairFor+time.Second))
	send := func(id, text string) {
		_, _ = w.h.botEvent(w.ctx, botMessageEvent(id, "ou_me", "text", textContent(text), nil))
	}
	send("om_late_right", code)
	waitCount(t, w.api, "还没配对", 1)
	// Neither the late correct code nor a later wrong one is called "wrong", and the pairing is not dropped.
	assertCountStays(t, w.api, "验证码不对", 0)
	now.Store(now.Load() + int64(botPairTellFor+time.Second))
	send("om_late_wrong", "000000")
	assertCountStays(t, w.api, "验证码不对", 0)
	waitCount(t, w.api, "还没配对", 2)
	// The expired pairing is hidden from the view, but it survives in the settings and burned no failures.
	var saved FeishuBotSettings
	if _, err := w.h.store.Setting(w.ctx, botSettingsKey, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Pairing == nil || saved.Pairing.Fails != 0 {
		t.Fatalf("expired pairing changed: %+v", saved.Pairing)
	}
}

// A non-owner is still on the 10-minute window: a retry 31 s later is swallowed, one 10 minutes later goes through.
func TestBotPairNonOwnerKeepsTheLongWindow(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	now := fakeBotClock(w)
	w.pair(t)
	stranger := func(id string) {
		_, _ = w.h.botEvent(w.ctx, botMessageEvent(id, "ou_stranger", "text", textContent("帮我删库"), nil))
	}
	stranger("om_s1")
	waitCount(t, w.api, "只为它的主人服务", 1)
	now.Store(now.Load() + int64(botPairTellFor+time.Second))
	stranger("om_s2")
	assertCountStays(t, w.api, "只为它的主人服务", 1)
	now.Store(now.Load() + int64(10*time.Minute))
	stranger("om_s3")
	waitCount(t, w.api, "只为它的主人服务", 2)
}

// When Feishu refuses the paired card, its body still arrives as rich text.
func TestBotPairedCardFallbackKeepsBody(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.h.opts.AssistantEvent = nil // no assistant to greet: the manager posts the paired card
	w.api.rejectInteractive = 230099
	_, out := w.do(t, "POST", "/api/v1/bots/feishu/pair", "")
	pairing := out["pairing"].(map[string]any)
	_, _ = w.h.botEvent(w.ctx, botMessageEvent("om_pair", "ou_me", "text", textContent(pairing["code"].(string)), nil))
	w.api.find(t, `"msg_type":"post"`, "以后在这里说的话会交给 Persona")
}

// The bot test message's body is kept as the rich-text fallback too.
func TestBotTestCardFallbackKeepsBody(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.api.rejectInteractive = 230099
	if code, out := w.do(t, "POST", "/api/v1/bots/feishu/test", ""); code != 200 {
		t.Fatalf("test: %d %v", code, out)
	}
	w.api.find(t, `"msg_type":"post"`, "这是来自 Persona 的测试消息")
}
