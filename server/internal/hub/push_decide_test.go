package hub

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/jsonsubset"
	"github.com/nwverse/persona/server/internal/pushcrypto"
)

// A to-do whose conclusion waits for the owner's decision with a recommended option is pushed with the category whose OK
// takes it (#2056), and with what OK needs: the to-do, the round's asked_at, the recommendation.

// decideNotification is a "decide" push as the assistant sends it over the bus.
func decideNotification(t *testing.T) assistantwire.Notification {
	t.Helper()
	var n assistantwire.Notification
	if err := json.Unmarshal([]byte(`{"id":"ntf_1","kind":"decide","title":"结论：修登录崩溃","subtitle":"Persona",
		"body":"🤔 要你拍板：先灰度还是直接全量\n推荐：先灰度 10%","device_id":"m","session_id":"conv1",
		"todo":{"id":"todo_1","asked_at":"2026-11-09T05:06:07.123456789Z","recommend":"先灰度 10%"}}`), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

var decideTodo = map[string]any{"id": "todo_1", "asked_at": "2026-11-09T05:06:07.123456789Z", "recommend": "先灰度 10%", "manager": assistantwire.AssistantID}

// decideHub is a hub with one device, pushed directly (a fake APNs) and connected with ws_notify, taking "needs you"
// pushes or not.
func decideHub(t *testing.T, input bool) (*Hub, *client, *fakeAPNs) {
	t.Helper()
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &client{device: "phone", out: make(chan []byte, 8)}
	h := &Hub{clients: map[*client]struct{}{c: {}}, push: newPusher(t.TempDir(), nil, t.Logf)}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "tok-phone", Topic: "app.example"})
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: input, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	return h, c, f
}

func decidePushAttempts(h *Hub) *atomic.Int64 {
	attempts := new(atomic.Int64)
	h.push.traffic = func(key trafficKey, _ int64, _ time.Duration) {
		if key.route == "apns" && key.action == "attempt" {
			attempts.Add(1)
		}
	}
	return attempts
}

// On APNs: category TODO_DECIDE, time-sensitive, the assistant's words, and `todo` at the top level (the app's OK reads
// it from userInfo); the device that takes "needs you" pushes gets it.
func TestDecisionPushHasItsCategoryAndTodo(t *testing.T) {
	h, _, f := decideHub(t, true)
	attempts := decidePushAttempts(h)
	if err := h.deliverPush(decideNotification(t)); err != nil {
		t.Fatal(err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("%d APNs attempts with needs-you on", n)
	}
	calls := f.calls(t, 1)
	var got struct {
		APS struct {
			Alert    map[string]string `json:"alert"`
			Category string            `json:"category"`
			Level    string            `json:"interruption-level"`
			Thread   string            `json:"thread-id"`
		} `json:"aps"`
		SessionID string         `json:"session_id"`
		Todo      map[string]any `json:"todo"`
	}
	if err := json.Unmarshal(calls[0].Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.APS.Category != "TODO_DECIDE" || got.APS.Level != "time-sensitive" || got.APS.Thread != "conv1" || got.SessionID != "conv1" {
		t.Fatalf("aps %+v session %q", got.APS, got.SessionID)
	}
	if got.APS.Alert["title"] != "结论：修登录崩溃" || got.APS.Alert["body"] != "🤔 要你拍板：先灰度还是直接全量\n推荐：先灰度 10%" {
		t.Fatalf("alert %v", got.APS.Alert)
	}
	if !reflect.DeepEqual(got.Todo, decideTodo) {
		t.Fatalf("todo %v, want %v", got.Todo, decideTodo)
	}
	// The next round of the same to-do takes the earlier one's place on the phone (one collapse id a to-do).
	next := decideNotification(t)
	next.Todo.AskedAt = next.Todo.AskedAt.Add(time.Hour)
	if err := h.deliverPush(next); err != nil {
		t.Fatal(err)
	}
	calls = f.calls(t, 2)
	if calls[0].Collapse == "" || calls[1].Collapse != calls[0].Collapse {
		t.Fatalf("collapse ids %q and %q", calls[0].Collapse, calls[1].Collapse)
	}
}

// A decision's push names the manager whose to-do it is, which OK names in ManagerHeader: another manager's, and the
// assistant's too (the default manager may be another one).
func TestDecisionPushNamesItsManager(t *testing.T) {
	n := decideNotification(t)
	if td, _ := decidePayloadIn("m-0b0b0b", "mb", "mb", "b1", n.Title, n.Body, *n.Todo)["todo"].(map[string]any); td["manager"] != "m-0b0b0b" {
		t.Fatalf("another manager's: %v", td)
	}
	if td, _ := decidePayloadIn(assistantwire.AssistantID, "m", "m", "conv1", n.Title, n.Body, *n.Todo)["todo"].(map[string]any); td["manager"] != assistantwire.AssistantID {
		t.Fatalf("the assistant's: %v", td)
	}
}

// On the WebSocket it is an "assistant" notify (a client that does not know decisions shows it as any other) with the
// same `todo`.
func TestDecisionPushOnTheWebSocket(t *testing.T) {
	h, c, _ := decideHub(t, true)
	if err := h.deliverPush(decideNotification(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-c.out:
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if got["type"] != "notify" || got["kind"] != "assistant" || got["session_id"] != "conv1" || !reflect.DeepEqual(got["todo"], decideTodo) {
			t.Fatalf("notify %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no notify on the WebSocket")
	}
}

// A device with "needs you" pushes off gets it neither way.
func TestDecisionPushTakesTheInputSwitch(t *testing.T) {
	h, c, f := decideHub(t, false)
	attempts := decidePushAttempts(h)
	if err := h.deliverPush(decideNotification(t)); err != nil {
		t.Fatal(err)
	}
	if n := attempts.Load(); n != 0 {
		t.Fatalf("%d APNs attempts with needs-you off", n)
	}
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("%d APNs pushes with needs-you off", n)
	}
	select {
	case raw := <-c.out:
		t.Fatalf("notify with needs-you off: %s", raw)
	default:
	}
}

// A decision's push without its to-do, or without the option, is refused: OK would have nothing to take.
func TestDecisionPushNeedsItsTodo(t *testing.T) {
	h, _, _ := decideHub(t, true)
	n := decideNotification(t)
	noOption := n
	noOption.Todo = &assistantwire.NotifyTodo{ID: n.Todo.ID, AskedAt: n.Todo.AskedAt}
	if err := h.deliverPush(noOption); err == nil {
		t.Fatal("a decide push without its option was taken")
	}
	raw, _ := json.Marshal(n)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	delete(m, "todo")
	raw, _ = json.Marshal(m)
	var bare assistantwire.Notification
	_ = json.Unmarshal(raw, &bare)
	if err := h.deliverPush(bare); err == nil {
		t.Fatal("a decide push without its to-do was taken")
	}
}

// decideAsk is the view's id of decideNotification's decision (projection.DecisionAskID).
const decideAsk = "todo:decision@2026-11-09T05:06:07.123456789Z"

// decidePushWith is decideNotification from a manager that says how its push may answer it (#10465), with options as
// the manager lists them.
func decidePushWith(t *testing.T, lockscreen string, options ...string) assistantwire.Notification {
	t.Helper()
	n := decideNotification(t)
	n.Todo.Ask, n.Todo.Lockscreen, n.Todo.Options = decideAsk, lockscreen, options
	n.Todo.Responses = []string{"decide_yourself", "not_what_i_asked", "explain_first"}
	return n
}

// decidePushCase is a case of spec/fixtures/todo-push/todo_decide_push.json.
type decidePushCase struct {
	Name  string
	Input struct {
		Owner string                   `json:"owner"`
		Todo  assistantwire.NotifyTodo `json:"todo"`
	}
	Expected struct {
		Category       string          `json:"category"`
		MutableContent bool            `json:"mutable_content"`
		Todo           json.RawMessage `json:"todo"`
	}
}

// The shared cases of a decision's push (spec/fixtures/todo-push, #10465): the category, mutable-content and `todo`
// the hub writes for what the manager sent, each `todo` exactly the schema's TodoDecidePush (no member it lacks, every
// value one it allows).
func TestDecisionPushFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "todo-push", "todo_decide_push.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Cases []decidePushCase }
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Cases) == 0 {
		t.Fatalf("%d cases: %v", len(doc.Cases), err)
	}
	schema, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "wire", "todo-view.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := jsonsubset.Compile(schema)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := root["$defs"].(map[string]any)["TodoDecidePush"].(map[string]any)
	if def == nil {
		t.Fatal("no TodoDecidePush in the schema")
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			p := decidePayloadIn(c.Input.Owner, "m", "Persona", "conv1", "Which ryokan?", "Hanami is recommended", c.Input.Todo)
			aps, _ := p["aps"].(map[string]any)
			mutable, set := aps["mutable-content"]
			if aps["category"] != c.Expected.Category || set != c.Expected.MutableContent || set && mutable != 1 {
				t.Errorf("aps %v", aps)
			}
			got, _ := json.Marshal(p["todo"])
			var want, have any
			if err := json.Unmarshal(c.Expected.Todo, &want); err != nil {
				t.Fatalf("expected.todo: %v", err)
			}
			if err := json.Unmarshal(got, &have); err != nil {
				t.Fatalf("todo %s: %v", got, err)
			}
			if !reflect.DeepEqual(want, have) {
				t.Errorf("todo %s\nwant %s", got, c.Expected.Todo)
			}
			v, err := jsonsubset.Decode(got)
			if err != nil {
				t.Fatal(err)
			}
			if bad := jsonsubset.ValidateAt(root, def, v, "$", 0); bad != "" {
				t.Errorf("todo %s: %s", got, bad)
			}
			d := json.NewDecoder(bytes.NewReader(got))
			d.DisallowUnknownFields()
			if err := d.Decode(new(assistantwire.TodoDecidePush)); err != nil {
				t.Errorf("todo %s: %v", got, err)
			}
		})
	}
}

// A manager from before the lock-screen rule gets the push it always got, byte for byte, though it sends its options
// and fixed answers: TODO_DECIDE, whose Take Recommendation the iOS app offers.
func TestOlderManagersDecisionPushIsUnchanged(t *testing.T) {
	n := decideNotification(t)
	n.Todo.Options, n.Todo.Responses = []string{"先灰度 10%", "直接全量"}, []string{"decide_yourself", "not_what_i_asked", "explain_first"}
	got, err := json.Marshal(decidePayloadIn(assistantwire.AssistantID, "m", "Persona", "conv1", n.Title, n.Body, *n.Todo))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"aps":{"alert":{"body":"🤔 要你拍板：先灰度还是直接全量\n推荐：先灰度 10%","subtitle":"Persona","title":"结论：修登录崩溃"},` +
		`"category":"TODO_DECIDE","interruption-level":"time-sensitive","sound":"default","thread-id":"conv1"},` +
		`"machine_id":"m","session_id":"conv1",` +
		`"todo":{"asked_at":"2026-11-09T05:06:07.123456789Z","id":"todo_1","manager":"assistant","recommend":"先灰度 10%"}}`
	if string(got) != want {
		t.Fatalf("an older manager's push changed:\n got %s\nwant %s", got, want)
	}
}

// From a manager that says the rule, the push goes out as it says on APNs and on the WebSocket alike: open_only as
// TODO_DECIDE_OPEN with no options, so no app offers an answer on the lock screen; answer as TODO_DECIDE with the
// options, the recommended first, and mutable-content for the app's extension.
func TestDecisionPushSaysHowItMayBeAnswered(t *testing.T) {
	for _, tc := range []struct {
		lockscreen, category string
		options              []any
	}{
		{assistantwire.OwnerAskLockscreenAnswer, decideCategory, []any{"先灰度 10%", "直接全量"}},
		{assistantwire.OwnerAskLockscreenOpenOnly, decideOpenCategory, nil},
	} {
		t.Run(tc.lockscreen, func(t *testing.T) {
			h, c, f := decideHub(t, true)
			if err := h.deliverPush(decidePushWith(t, tc.lockscreen, "直接全量", "先灰度 10%")); err != nil {
				t.Fatal(err)
			}
			var got struct {
				APS struct {
					Category string `json:"category"`
					Mutable  *int   `json:"mutable-content"`
				} `json:"aps"`
				Todo map[string]any `json:"todo"`
			}
			if err := json.Unmarshal(f.calls(t, 1)[0].Body, &got); err != nil {
				t.Fatal(err)
			}
			want := maps.Clone(decideTodo)
			want["ask"], want["lockscreen"] = decideAsk, tc.lockscreen
			if tc.options != nil {
				want["options"] = tc.options
			}
			if got.APS.Category != tc.category || (got.APS.Mutable != nil) != (tc.options != nil) ||
				got.APS.Mutable != nil && *got.APS.Mutable != 1 || !reflect.DeepEqual(got.Todo, want) {
				t.Fatalf("aps %+v todo %v, want %s and %v", got.APS, got.Todo, tc.category, want)
			}
			select {
			case raw := <-c.out:
				var ws map[string]any
				_ = json.Unmarshal(raw, &ws)
				if !reflect.DeepEqual(ws["todo"], want) {
					t.Fatalf("notify todo %v, want %v", ws["todo"], want)
				}
			case <-time.After(time.Second):
				t.Fatal("no notify on the WebSocket")
			}
		})
	}
}

// A decision push too big for APNs keeps what says how it may be answered: the alert is shortened first, down to
// decideAlertShortest, so the longest labels a manager writes (120 characters each, decision.go) still reach the app's
// extension, which offers those an action's title shows whole; labels that would cut the alert shorter go with
// mutable-content, and the push, its full alert fitted again, falls back to Take Recommendation, never losing its
// category, its lockscreen or its ask.
func TestDecisionPushFitKeepsItsRule(t *testing.T) {
	for _, huge := range []bool{false, true} {
		label := strings.Repeat("界", 120)
		if huge {
			label = strings.Repeat("界", 1500)
		}
		labels := []string{"先灰度 10%", label + "一", label + "二", label + "三"}
		n := decidePushWith(t, assistantwire.OwnerAskLockscreenAnswer, labels...)
		payload := decidePayloadIn(assistantwire.AssistantID, "m", "Persona", "conv1", n.Title, strings.Repeat("q", 8000), *n.Todo)
		body, err := fitAlert(maps.Clone(payload), apnsMaxAlert)
		if err != nil || len(body) > apnsMaxAlert || !utf8.Valid(body) {
			t.Fatalf("fit (huge=%v): %d bytes, %v", huge, len(body), err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		aps, _ := out["aps"].(map[string]any)
		td, _ := out["todo"].(map[string]any)
		if aps["category"] != decideCategory || td["lockscreen"] != assistantwire.OwnerAskLockscreenAnswer || td["ask"] != decideAsk ||
			td["recommend"] != "先灰度 10%" {
			t.Fatalf("fit (huge=%v) lost the rule: aps %v todo %v", huge, aps["category"], td)
		}
		_, mutable := aps["mutable-content"]
		if huge {
			if _, ok := td["options"]; ok || mutable {
				t.Fatalf("oversized options kept: mutable %v todo %v", mutable, td)
			}
			if b, _ := aps["alert"].(map[string]any)["body"].(string); utf8.RuneCountInString(b) < 1000 {
				t.Fatal("dropping the options did not restore the full alert before shortening")
			}
		} else if !mutable || !reflect.DeepEqual(td["options"], []any{labels[0], labels[1], labels[2], labels[3]}) {
			t.Fatalf("fitting clipped or lost the options: mutable %v %v", mutable, td["options"])
		}
	}
	// A decision only the app answers keeps its category however short its alert gets.
	n := decidePushWith(t, assistantwire.OwnerAskLockscreenOpenOnly, "先灰度 10%", "直接全量")
	body, err := fitAlert(decidePayloadIn(assistantwire.AssistantID, "m", "Persona", "conv1", n.Title, strings.Repeat("q", 8000), *n.Todo), 1000)
	var out map[string]any
	if err != nil || json.Unmarshal(body, &out) != nil {
		t.Fatalf("open_only fit: %v", err)
	}
	if aps, _ := out["aps"].(map[string]any); aps["category"] != decideOpenCategory || out["todo"].(map[string]any)["lockscreen"] != assistantwire.OwnerAskLockscreenOpenOnly {
		t.Fatalf("open_only fit: %v", out)
	}
}

// The relay's smaller budget may take a decision's options off where a direct push keeps them: the sealed push falls
// back to Take Recommendation, and the payload other devices share is left as it was. Short labels go sealed with
// the alert shortened instead; labels that would leave it under decideAlertShortest go, and the alert keeps its length.
func TestRelayDecisionBudgetDoesNotChangeDirectPayload(t *testing.T) {
	h, _, priv := relayHub(t)
	d, _ := h.push.device("phone")
	open := func(sealed string) map[string]any {
		t.Helper()
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
		return out
	}
	n := decidePushWith(t, assistantwire.OwnerAskLockscreenAnswer, "先灰度 10%", strings.Repeat("x", 2800))
	payload := decidePayloadIn(assistantwire.AssistantID, "m", "Persona", "conv1", n.Title, n.Body, *n.Todo)
	direct, err := fitAlert(payload, apnsMaxAlert)
	if err != nil || payload["todo"].(map[string]any)["options"] == nil {
		t.Fatalf("the options should fit a direct push: %v", err)
	}
	sealed, err := h.push.sealFor(d, payload)
	if err != nil {
		t.Fatal(err)
	}
	out := open(sealed)
	aps, _ := out["aps"].(map[string]any)
	td, _ := out["todo"].(map[string]any)
	if _, mutable := aps["mutable-content"]; mutable || td["options"] != nil || aps["category"] != decideCategory ||
		td["lockscreen"] != assistantwire.OwnerAskLockscreenAnswer || td["ask"] != decideAsk {
		t.Fatalf("relay must drop only the options and mutable-content: aps %v todo %v", aps, td)
	}
	if after, _ := json.Marshal(payload); !bytes.Equal(direct, after) {
		t.Fatal("relay fitting changed the payload shared with direct devices")
	}
	for _, tc := range []struct {
		name    string
		runes   int // each label's length
		options bool
	}{
		{"short labels", 40, true},
		// Not the body: the labels would leave the alert under decideAlertShortest.
		{"labels the alert would pay for", 120, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := strings.Repeat("界", tc.runes)
			n := decidePushWith(t, assistantwire.OwnerAskLockscreenAnswer, "先灰度 10%", label+"一", label+"二", label+"三")
			sealed, err := h.push.sealFor(d, decidePayloadIn(assistantwire.AssistantID, "m", "Persona", "conv1", n.Title,
				strings.Repeat("问", 4000), *n.Todo))
			if err != nil {
				t.Fatal(err)
			}
			out := open(sealed)
			aps, _ := out["aps"].(map[string]any)
			td, _ := out["todo"].(map[string]any)
			opts, _ := td["options"].([]any)
			_, mutable := aps["mutable-content"]
			if (len(opts) == 4) != tc.options || mutable != tc.options || td["lockscreen"] != assistantwire.OwnerAskLockscreenAnswer {
				t.Fatalf("options %d, mutable %v, todo %v; want options %v", len(opts), mutable, td, tc.options)
			}
			if body, _ := aps["alert"].(map[string]any)["body"].(string); utf8.RuneCountInString(body) < decideAlertShortest {
				t.Fatalf("the alert went down to %d characters for the labels", utf8.RuneCountInString(body))
			}
		})
	}
}
