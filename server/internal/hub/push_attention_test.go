package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// setAttention makes the pusher read mode, in force for d from the pusher's clock (focus: immersive's matter).
func setAttention(h *Hub, mode string, d time.Duration, focus *assistantwire.AttentionFocus) {
	a := &assistantwire.Attention{Mode: mode, Until: h.push.now().Add(d), SetBy: assistantwire.AttentionByUser, Focus: focus}
	var mu sync.Mutex
	h.push.attention = func() *assistantwire.Attention { mu.Lock(); defer mu.Unlock(); return a }
}

func trackNotification(level, track, depth, title string) assistantwire.Notification {
	n := gradingNotification(level, "", title)
	n.Track, n.Depth = track, depth
	return n
}

// heldReasons are the held pushes' reasons, oldest first (the view lists the newest first).
func heldReasons(h *Hub) []string {
	var out []string
	for _, e := range h.push.heldPushesView("", 0) {
		out = append([]string{e.Reason}, out...)
	}
	return out
}

func apsOf(t *testing.T, c apnsCall) map[string]any {
	t.Helper()
	var body struct {
		APS map[string]any `json:"aps"`
	}
	if err := json.Unmarshal(c.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.APS
}

// Immersive mode lets through only the P0 of the matter in focus; the rest is held with its reason. hub.ln_mode is
// off: the mode filters all the same.
func TestAttentionImmersiveHoldsWhatIsNotInFocus(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	setAttention(h, assistantwire.AttentionImmersive, 30*time.Minute, &assistantwire.AttentionFocus{Track: "qa-plain"})
	for _, n := range []assistantwire.Notification{
		trackNotification(assistantwire.LevelP0, "qa-finance", "", "elsewhere"),
		trackNotification(assistantwire.LevelP0, "qa-plain", assistantwire.DepthDeep, "in focus"),
		trackNotification(assistantwire.LevelP1, "qa-plain", "", "in focus, P1"),
		trackNotification(assistantwire.LevelP2, "qa-plain", "", "in focus, P2"),
	} {
		if err := h.deliverPush(n); err != nil {
			t.Fatal(err)
		}
	}
	f.calls(t, 1)
	if *attempts != 1 {
		t.Fatalf("%d pushes, want only the one in focus", *attempts)
	}
	want := []string{assistantwire.AttentionReasonImmersiveHeld, assistantwire.AttentionReasonImmersiveHeld, assistantwire.AttentionReasonSilent}
	if got := heldReasons(h); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("held %v, want %v", got, want)
	}
	if h.push.pushGradingOn() {
		t.Fatal("the mode turned hub.ln_mode on")
	}
}

// Quiet mode lets a P0 through without a sound, holds P1 for the briefing and keeps P2 silent.
func TestAttentionQuietHushesP0(t *testing.T) {
	h, f := gradingHub(t)
	setAttention(h, assistantwire.AttentionQuiet, 8*time.Hour, nil)
	for _, n := range []assistantwire.Notification{
		gradingNotification(assistantwire.LevelP0, "", "decide now"),
		gradingNotification(assistantwire.LevelP1, "", "worth knowing"),
		gradingNotification(assistantwire.LevelP2, "", "background"),
	} {
		if err := h.deliverPush(n); err != nil {
			t.Fatal(err)
		}
	}
	c := f.calls(t, 1)
	if a := apsOf(t, c[0]); a["sound"] != nil || a["interruption-level"] != "passive" {
		t.Fatalf("the P0 in quiet mode: %v", a)
	}
	got := heldReasons(h)
	if len(got) != 2 || got[0] != assistantwire.AttentionReasonQuietHeld || got[1] != assistantwire.AttentionReasonSilent {
		t.Fatalf("held %v", got)
	}
}

// Fragment mode keeps a deep P0 for a block of time and batches P1; the hub's own alert always goes.
func TestAttentionFragmentDefersDeepAndLetsSystemThrough(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	setAttention(h, assistantwire.AttentionFragment, time.Hour, nil)
	for _, n := range []assistantwire.Notification{
		trackNotification(assistantwire.LevelP0, "", assistantwire.DepthDeep, "read the plan"),
		trackNotification(assistantwire.LevelP0, "", assistantwire.DepthQuick, "yes or no"),
		trackNotification(assistantwire.LevelP1, "", "", "a conclusion"),
	} {
		if err := h.deliverPush(n); err != nil {
			t.Fatal(err)
		}
	}
	h.push.sendLocalized(func(i18n.Lang) map[string]any {
		return map[string]any{"aps": map[string]any{"alert": map[string]any{"title": "A computer joined"}, "sound": "default"}}
	}, "", nil)
	f.calls(t, 2)
	if *attempts != 2 {
		t.Fatalf("%d pushes, want the quick P0 and the hub's own", *attempts)
	}
	got := heldReasons(h)
	if len(got) != 2 || got[0] != assistantwire.AttentionReasonDeepDeferred || got[1] != assistantwire.AttentionReasonFragmentBatch {
		t.Fatalf("held %v", got)
	}
}

// When the mode ends the pusher is back on hub.ln_mode as it was: on stays on, off stays off.
func TestAttentionEndRestoresLnMode(t *testing.T) {
	for _, lnOn := range []bool{false, true} {
		h, f := gradingHub(t)
		if err := h.push.setPushGrading(context.Background(), lnOn); err != nil {
			t.Fatal(err)
		}
		attempts := gradingAttempts(h)
		setAttention(h, assistantwire.AttentionImmersive, 10*time.Minute, nil)
		if err := h.deliverPush(gradingNotification(assistantwire.LevelP1, "", "during")); err != nil {
			t.Fatal(err)
		}
		if *attempts != 0 {
			t.Fatalf("ln %v: a P1 went out during immersive mode", lnOn)
		}
		moveGradingClock(h, h.push.now().Add(11*time.Minute))
		if h.push.attentionNow().Active() {
			t.Fatalf("ln %v: the mode is still in force after its end", lnOn)
		}
		if err := h.deliverPush(gradingNotification(assistantwire.LevelP1, "", "after")); err != nil {
			t.Fatal(err)
		}
		if h.push.pushGradingOn() != lnOn {
			t.Fatalf("ln %v: hub.ln_mode reads %v after the mode", lnOn, h.push.pushGradingOn())
		}
		got := heldReasons(h)
		if lnOn {
			if *attempts != 0 || len(got) != 2 || got[1] != interceptReasonP1 {
				t.Fatalf("ln on after the mode: %d pushes, held %v", *attempts, got)
			}
			continue
		}
		f.calls(t, 1)
		if *attempts != 1 || len(got) != 1 {
			t.Fatalf("ln off after the mode: %d pushes, held %v", *attempts, got)
		}
	}
}

// Without a mode (or with normal) nothing changes: with hub.ln_mode off every level goes out as before.
func TestAttentionNormalChangesNothing(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	setAttention(h, assistantwire.AttentionNormal, time.Hour, nil)
	for _, level := range []string{assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2} {
		if err := h.deliverPush(trackNotification(level, "qa-plain", assistantwire.DepthDeep, "card "+level)); err != nil {
			t.Fatal(err)
		}
	}
	f.calls(t, 3)
	if *attempts != 3 || len(heldReasons(h)) != 0 {
		t.Fatalf("normal: %d pushes, held %v", *attempts, heldReasons(h))
	}
}

// Immersive focus trusts a push's track only from the assistant that reports the mode: another manager naming the
// track in focus is held like any push that is not about it.
func TestAttentionFocusTrackOnlyFromTheAssistant(t *testing.T) {
	attn := assistantwire.Attention{Mode: assistantwire.AttentionImmersive, Until: time.Now().Add(time.Hour),
		Focus: &assistantwire.AttentionFocus{Track: "qa-plain"}}
	own := pushPolicy{manager: assistantwire.AssistantID, track: "qa-plain"}
	if got := attentionPush(own, assistantwire.LevelP0).Track; got != "qa-plain" {
		t.Fatalf("the assistant's track %q, want qa-plain", got)
	}
	if v := assistantwire.DecideAttention(attn, attentionPush(own, assistantwire.LevelP0)); v.Hold {
		t.Fatalf("the assistant's P0 in focus was held: %+v", v)
	}
	other := pushPolicy{manager: "ext-other", track: "qa-plain"}
	if got := attentionPush(other, assistantwire.LevelP0).Track; got != "" {
		t.Fatalf("another manager's track %q, want none", got)
	}
	if v := assistantwire.DecideAttention(attn, attentionPush(other, assistantwire.LevelP0)); !v.Hold {
		t.Fatalf("another manager's P0 naming the focus went: %+v", v)
	}
}
