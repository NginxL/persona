package assistantwire

import (
	"fmt"
	"testing"
	"time"
)

// attentionWant is what the design's table (docs/server/server.md "Attention modes") says one push gets, written
// out case by case rather than computed the way DecideAttention computes it.
func attentionWant(mode, level, depth string, urgent, quietHours bool) AttentionVerdict {
	if urgent {
		return AttentionVerdict{} // an urgent call always rings, with its sound
	}
	if mode == AttentionNormal {
		return AttentionVerdict{Hush: quietHours} // hub.ln_mode decides; quiet hours as before
	}
	switch level {
	case LevelP2:
		return AttentionVerdict{Hold: true, Reason: AttentionReasonSilent}
	case LevelP1:
		switch mode {
		case AttentionFragment:
			return AttentionVerdict{Hold: true, Reason: AttentionReasonFragmentBatch}
		case AttentionImmersive:
			return AttentionVerdict{Hold: true, Reason: AttentionReasonImmersiveHeld}
		case AttentionQuiet:
			return AttentionVerdict{Hold: true, Reason: AttentionReasonQuietHeld}
		}
	case LevelP0:
		switch mode {
		case AttentionFragment:
			if depth == DepthDeep {
				return AttentionVerdict{Hold: true, Reason: AttentionReasonDeepDeferred}
			}
			return AttentionVerdict{Hush: quietHours}
		case AttentionImmersive:
			// Not in focus (the matrix's pushes come from elsewhere).
			return AttentionVerdict{Hold: true, Reason: AttentionReasonImmersiveHeld}
		case AttentionQuiet:
			return AttentionVerdict{Hush: true}
		}
	}
	panic("no case for " + mode + " " + level)
}

// 4 modes × 3 levels × depth × urgent call × quiet hours.
func TestDecideAttentionMatrix(t *testing.T) {
	focus := &AttentionFocus{Track: "qa-plain"}
	for _, mode := range AttentionModes {
		a := Attention{Mode: mode, Focus: focus}
		for _, level := range []string{LevelP0, LevelP1, LevelP2} {
			for _, depth := range []string{DepthQuick, DepthDeep} {
				for _, urgent := range []bool{false, true} {
					for _, quiet := range []bool{false, true} {
						p := AttentionPush{Level: level, Depth: depth, Track: "qa-finance", MachineID: "mac", SessionID: "s2",
							UrgentCall: urgent, QuietHours: quiet}
						got := DecideAttention(a, p)
						want := attentionWant(mode, level, depth, urgent, quiet)
						if got != want {
							t.Errorf("%s %s %s urgent=%v quiet_hours=%v: %+v, want %+v", mode, level, depth, urgent, quiet, got, want)
						}
					}
				}
			}
		}
	}
}

func TestDecideAttentionImmersiveFocus(t *testing.T) {
	for _, c := range []struct {
		name  string
		focus *AttentionFocus
		push  AttentionPush
		hold  bool
	}{
		{"no focus", nil, AttentionPush{Level: LevelP0, Track: "qa-plain"}, true},
		{"the track in focus", &AttentionFocus{Track: "qa-plain"}, AttentionPush{Level: LevelP0, Track: "qa-plain"}, false},
		{"the track in focus, deep", &AttentionFocus{Track: "qa-plain"}, AttentionPush{Level: LevelP0, Depth: DepthDeep, Track: "qa-plain"}, false},
		{"another track", &AttentionFocus{Track: "qa-plain"}, AttentionPush{Level: LevelP0, Track: "qa-finance"}, true},
		{"no track against a track focus", &AttentionFocus{Track: "qa-plain"}, AttentionPush{Level: LevelP0}, true},
		{"the session in focus", &AttentionFocus{MachineID: "mac", SessionID: "s1"}, AttentionPush{Level: LevelP0, MachineID: "mac", SessionID: "s1"}, false},
		{"the session on another machine", &AttentionFocus{MachineID: "mac", SessionID: "s1"}, AttentionPush{Level: LevelP0, MachineID: "pc", SessionID: "s1"}, true},
		{"the session, any machine", &AttentionFocus{SessionID: "s1"}, AttentionPush{Level: LevelP0, MachineID: "pc", SessionID: "s1"}, false},
		{"P1 of the track in focus is still held", &AttentionFocus{Track: "qa-plain"}, AttentionPush{Level: LevelP1, Track: "qa-plain"}, true},
		{"the hub's own alert", nil, AttentionPush{Level: LevelP0, System: true}, false},
		{"a finished turn in focus", &AttentionFocus{Track: "qa-plain"}, AttentionPush{Level: LevelP0, Track: "qa-plain", Done: true}, true},
		{"an urgent call", nil, AttentionPush{Level: LevelP0, UrgentCall: true}, false},
	} {
		got := DecideAttention(Attention{Mode: AttentionImmersive, Focus: c.focus}, c.push)
		if got.Hold != c.hold || got.Hush {
			t.Errorf("%s: %+v, want hold %v", c.name, got, c.hold)
		}
		if got.Hold && got.Reason == "" {
			t.Errorf("%s: held without a reason", c.name)
		}
	}
}

func TestDecideAttentionSystemAndDone(t *testing.T) {
	for _, mode := range []string{AttentionFragment, AttentionImmersive, AttentionQuiet} {
		a := Attention{Mode: mode}
		// The hub's own alert is never held, whatever level it carries, but quiet mode hushes it.
		for _, level := range []string{"", LevelP1, LevelP2} {
			got := DecideAttention(a, AttentionPush{Level: level, Depth: DepthDeep, System: true})
			if got.Hold || got.Hush != (mode == AttentionQuiet) {
				t.Errorf("%s system %q: %+v", mode, level, got)
			}
		}
		// A finished turn is never pushed in a mode, even one marked P0.
		if got := DecideAttention(a, AttentionPush{Level: LevelP0, Done: true}); !got.Hold || got.Reason != AttentionReasonSilent {
			t.Errorf("%s done: %+v", mode, got)
		}
		// An unknown level is P1.
		if got := DecideAttention(a, AttentionPush{Level: "P9"}); !got.Hold || got.Reason == AttentionReasonSilent {
			t.Errorf("%s unknown level: %+v", mode, got)
		}
	}
	// Normal holds nothing.
	if got := DecideAttention(Attention{Mode: AttentionNormal}, AttentionPush{Level: LevelP2, Done: true}); got.Hold {
		t.Errorf("normal: %+v", got)
	}
}

func TestAttentionEffective(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 30, 0, 0, time.UTC)
	for i, c := range []struct {
		a    *Attention
		want string
	}{
		{nil, AttentionNormal},
		{&Attention{}, AttentionNormal},
		{&Attention{Mode: AttentionQuiet}, AttentionNormal}, // no time limit: never on
		{&Attention{Mode: AttentionQuiet, Until: now.Add(time.Minute)}, AttentionQuiet},
		{&Attention{Mode: AttentionQuiet, Until: now}, AttentionNormal}, // ends at its time
		{&Attention{Mode: AttentionImmersive, Until: now.Add(-time.Second)}, AttentionNormal},
		{&Attention{Mode: "focus", Until: now.Add(time.Hour)}, AttentionNormal}, // a mode this build does not know
		{&Attention{Mode: AttentionFragment, Until: now.Add(time.Hour)}, AttentionFragment},
	} {
		got := c.a.Effective(now)
		if got.Mode != c.want {
			t.Errorf("case %d (%s): %q, want %q", i, fmt.Sprint(c.a), got.Mode, c.want)
		}
		if got.Active() != (c.want != AttentionNormal) {
			t.Errorf("case %d: active %v", i, got.Active())
		}
	}
}
