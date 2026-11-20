package hub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/pushcrypto"
)

func pushAskCard(labels ...string) proto.Interaction {
	q := proto.Question{Question: "Which library?", MultiSelect: true}
	for _, label := range labels {
		q.Options = append(q.Options, proto.QuestionOption{Label: label})
	}
	return proto.Interaction{ID: "ix_ask", MachineID: "m1", SessionID: "s1", Kind: proto.IxQuestion, Questions: []proto.Question{q}}
}

func TestPushAskEligibility(t *testing.T) {
	labels := []string{"A complete option label longer than the alert preview", "日本語", "third", "fourth", "fifth"}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		ix := pushAskCard(labels...)
		payload := interactionPayloadIn(lang, ix, "computer")
		ask, _ := payload["ask"].(map[string]any)
		if ask["question"] != ix.Questions[0].Question || ask["multi_select"] != true ||
			ask["rev"] != askRev(ix.Questions[0]) || !reflect.DeepEqual(ask["options"], labels) {
			t.Fatalf("%s: incomplete ask: %v", lang, ask)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*proto.Interaction)
	}{
		{"multiple questions", func(ix *proto.Interaction) { ix.Questions = append(ix.Questions, ix.Questions[0]) }},
		{"no questions", func(ix *proto.Interaction) { ix.Questions = nil }},
		{"no options", func(ix *proto.Interaction) { ix.Questions[0].Options = nil }},
		{"fields", func(ix *proto.Interaction) { ix.Fields = []proto.Field{{Key: "value", Label: "Value"}} }},
		{"permission", func(ix *proto.Interaction) { ix.Kind = proto.IxPermission }},
		{"health", func(ix *proto.Interaction) { ix.Cwd = "/home/example/.persona/health" }},
		{"cannot be undone", func(ix *proto.Interaction) { ix.ToolName, ix.Reversible = "ask_user", new(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := pushAskCard("first", "second")
			tc.edit(&ix)
			if _, ok := interactionPayloadIn(i18n.En, ix, "computer")["ask"]; ok {
				t.Fatal("ineligible card exposes ask")
			}
		})
	}
}

// An ask_user card whose asker said going ahead cannot be undone is answered only after a look, as the to-do's view
// says (open_only, #10465): its push offers no options, and its alert still says what it asks. One that said it can be
// undone, or said nothing, keeps them.
func TestPushAskCannotUndoOpensOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reversible *bool
		options    bool
	}{
		{"cannot be undone", new(false), false},
		{"can be undone", new(true), true},
		{"said nothing", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := pushAskCard("Delete the old table", "Keep it")
			ix.ToolName, ix.Reversible = "ask_user", tc.reversible
			payload := interactionPayloadIn(i18n.En, ix, "computer")
			if _, ok := payload["ask"]; ok != tc.options {
				t.Fatalf("ask %v, want options %v", payload["ask"], tc.options)
			}
			body, _ := payload["aps"].(map[string]any)["alert"].(map[string]any)["body"].(string)
			if !strings.Contains(body, "Which library?") || !strings.Contains(body, "Delete the old table") {
				t.Fatalf("alert body %q", body)
			}
		})
	}
}

func TestPushAskRevision(t *testing.T) {
	base := pushAskCard("first", "second").Questions[0]
	rev := askRev(base)
	if len(rev) != 12 || askRev(base) != rev {
		t.Fatalf("unstable or unexpected revision: %q", rev)
	}
	for _, q := range []proto.Question{
		{Question: "Another question?", MultiSelect: true, Options: base.Options},
		{Question: base.Question, Options: base.Options},
		pushAskCard("second", "first").Questions[0],
		pushAskCard("first", "changed").Questions[0],
		pushAskCard("first").Questions[0],
	} {
		if askRev(q) == rev {
			t.Fatalf("changed question retained rev: %+v", q)
		}
	}
	// Concatenating labels with NUL separators would give these different cards the same revision.
	if askRev(pushAskCard("a\x00b", "c").Questions[0]) == askRev(pushAskCard("a", "b\x00c").Questions[0]) {
		t.Fatal("revision lost option boundaries")
	}
}

func TestPushAskFitKeepsLabelsOrDropsWhole(t *testing.T) {
	for _, huge := range []bool{false, true} {
		label := "A full label"
		if huge {
			label = strings.Repeat("界", 1500)
		}
		ix := pushAskCard(label, "second")
		payload := interactionPayloadIn(i18n.En, ix, "computer")
		// Alert text may shrink independently of the structured question and labels.
		payload["aps"].(map[string]any)["alert"].(map[string]any)["body"] = strings.Repeat("q", 8000)
		body, err := fitAlert(payload, apnsMaxAlert)
		if err != nil || len(body) > apnsMaxAlert || !utf8.Valid(body) {
			t.Fatalf("fit (huge=%v): %d bytes, %v", huge, len(body), err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		ask, ok := out["ask"].(map[string]any)
		if huge {
			if ok {
				t.Fatalf("oversized ask retained: %v", ask)
			}
			alert := out["aps"].(map[string]any)["alert"].(map[string]any)
			if utf8.RuneCountInString(alert["body"].(string)) < 1000 {
				t.Fatal("dropping ask did not restore the full alert before shortening")
			}
		} else if !ok || !reflect.DeepEqual(ask["options"], []any{label, "second"}) {
			t.Fatalf("fitting clipped or lost labels: %v", ask)
		}
		if out["interaction_id"] != ix.ID {
			t.Fatal("fit lost interaction routing")
		}
	}
}

func TestDirectPushAsk(t *testing.T) {
	for _, huge := range []bool{false, true} {
		h, f := testLive(t)
		label := "complete option label"
		if huge {
			label = strings.Repeat("界", 1500)
		}
		h.push.notifyInteraction(pushAskCard(label, "second"), "computer")
		body := f.calls(t, 1)[0].Body
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil || len(body) > apnsMaxAlert {
			t.Fatalf("direct payload: %d bytes, %v", len(body), err)
		}
		ask, ok := out["ask"].(map[string]any)
		if ok == huge {
			t.Fatalf("direct ask presence (huge=%v): %v", huge, ask)
		}
		if !huge && !reflect.DeepEqual(ask["options"], []any{label, "second"}) {
			t.Fatalf("direct labels changed: %v", ask)
		}
	}
}

func TestRelayPushAskSealedAndFitted(t *testing.T) {
	for _, huge := range []bool{false, true} {
		h, f, priv := relayHub(t)
		label := "private option label"
		if huge {
			label = strings.Repeat("界", 1500)
		}
		ix := pushAskCard(label, "second private label")
		h.push.notifyInteraction(ix, "computer")
		r := f.wait(t, 1)[0]
		for _, secret := range []string{ix.Questions[0].Question, label, "second private label", `"multi_select"`, `"ask"`} {
			if bytes.Contains(r.Body, []byte(secret)) {
				t.Fatalf("relay leaked %q", secret)
			}
		}
		if len(r.Body) > apnsMaxAlert {
			t.Fatalf("relay payload exceeds APNs: %d bytes", len(r.Body))
		}
		out := opened(t, priv, r)
		ask, ok := out["ask"].(map[string]any)
		if ok == huge {
			t.Fatalf("decrypted ask presence (huge=%v): %v", huge, ask)
		}
		if !huge && (!reflect.DeepEqual(ask["options"], []any{label, "second private label"}) || ask["rev"] != askRev(ix.Questions[0])) {
			t.Fatalf("decrypted ask changed: %v", ask)
		}
	}
}

func TestRelayAskBudgetDoesNotChangeDirectPayload(t *testing.T) {
	h, _, priv := relayHub(t)
	d, _ := h.push.device("phone")
	payload := interactionPayloadIn(i18n.En, pushAskCard(strings.Repeat("x", 2800)), "computer")
	direct, err := fitAlert(payload, apnsMaxAlert)
	if err != nil || payload["ask"] == nil {
		t.Fatalf("ask should fit direct delivery: %v", err)
	}
	sealed, err := h.push.sealFor(d, payload)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := pushcrypto.Open(priv, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		t.Fatal(err)
	}
	if out["ask"] != nil || out["interaction_id"] != "ix_ask" {
		t.Fatal("relay must drop ask to fit its smaller plaintext budget")
	}
	after, _ := json.Marshal(payload)
	if !bytes.Equal(direct, after) {
		t.Fatal("relay fitting mutated the payload shared with direct devices")
	}
}
