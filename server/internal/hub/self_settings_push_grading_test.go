package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// persona_settings notifications.low_distraction (#8157): settings.list names it, get answers what GET
// /api/v1/push/ln-mode answers, and set changes the switch only once the owner approves the phone card: while the card
// waits and after a decline the switch is as it was; an approval turns it on and tells the managers (Hello.QuietPush).
func TestPushGradingSettingChangesTheSwitchOnlyOnApproval(t *testing.T) {
	h := newTestHub(t)
	h.push.store = newGradingStore()
	a := connectAssistant(t, h)
	m := &Machine{cfg: MachineConfig{ID: "box"}, hub: h}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	raw, ok, err := h.assistantAppCall(ctx, assistantwire.AssistantID, assistantwire.AppCall{AppID: assistantwire.AppSettings, Method: assistantwire.SettingsList})
	if !ok || err != nil {
		t.Fatalf("settings.list: %v %v", ok, err)
	}
	var list struct {
		Settings []assistantwire.Setting `json:"settings"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, s := range list.Settings {
		if s.Key == pushGradingSelfSettingKey {
			listed = strings.Join(s.Actions, ",") == "get,set" && s.Title != "" && s.About != ""
		}
	}
	if !listed {
		t.Fatalf("settings.list does not offer %s with get and set: %s", pushGradingSelfSettingKey, raw)
	}

	mux := http.NewServeMux()
	h.pushGradingRoutes(mux)
	route := func() bool {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/push/ln-mode", nil))
		var out struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET ln-mode: %d %s", rec.Code, rec.Body.String())
		}
		return out.Enabled
	}
	// run calls the tool; answer, when set, answers the one card it shows (after checking the switch has not moved).
	run := func(args string, answer *bool) (string, []proto.Interaction) {
		t.Helper()
		done := make(chan string, 1)
		go func() {
			out, err := h.toolPersonaSettings(ctx, assistantwire.AssistantID, m, "s1", json.RawMessage(args), nil)
			if err != nil {
				out = "error: " + err.Error()
			}
			done <- out
		}()
		var cards []proto.Interaction
		for {
			if pending := h.pendingInteractions(); answer != nil && len(cards) == 0 && len(pending) == 1 {
				cards = pending
				if h.push.pushGradingOn() || route() {
					t.Fatal("the switch changed before the owner answered the card")
				}
				_ = h.resolve(pending[0].ID, proto.Resolution{Allow: answer})
			}
			select {
			case out := <-done:
				return out, cards
			case <-ctx.Done():
				t.Fatalf("%s: no answer", args)
				return "", nil
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	get := `{"action":"get","key":"notifications.low_distraction"}`
	set := `{"action":"set","key":"notifications.low_distraction","value":true}`

	if out, _ := run(get, nil); out != "false" || route() {
		t.Fatalf("get = %q, route = %v; want both off", out, route())
	}
	no, yes := false, true
	if out, cards := run(set, &no); !strings.Contains(out, "declined") || len(cards) != 1 || h.push.pushGradingOn() {
		t.Fatalf("declined: %q, cards %d, on %v", out, len(cards), h.push.pushGradingOn())
	}
	out, cards := run(set, &yes)
	if len(cards) != 1 || cards[0].Kind != proto.IxPermission || cards[0].ToolName != "persona_settings" ||
		cards[0].Summary != i18n.T(h.userLang(), "hub-self_settings_lnmode.change", i18n.Args{"before": i18n.T(h.userLang(), "hub-self_settings_lnmode.off"),
			"after": i18n.T(h.userLang(), "hub-self_settings_lnmode.on"), "what": i18n.T(h.userLang(), "hub-self_settings_lnmode.when_on")}) {
		t.Fatalf("the card: %+v", cards)
	}
	if !h.push.pushGradingOn() || !route() || !strings.Contains(out, "true") {
		t.Fatalf("approved: %q, on %v, route %v", out, h.push.pushGradingOn(), route())
	}
	if got, _ := run(get, nil); got != "true" {
		t.Fatalf("get after the change = %q, want what the route says (true)", got)
	}
	eventually(t, 5*time.Second, "the manager told the switch is on", func() bool {
		hello, _ := a.bh.Hello(context.Background())
		return hello.QuietPush
	})
	if out, cards := run(set, &yes); len(cards) != 0 || !strings.Contains(out, "already") {
		t.Fatalf("setting what it already is: %q, %d cards", out, len(cards))
	}
	if out, _ := run(`{"action":"set","key":"notifications.low_distraction","value":"yes"}`, nil); !strings.HasPrefix(out, "error:") || !route() {
		t.Fatalf("a value that is not true or false: %q", out)
	}
}
