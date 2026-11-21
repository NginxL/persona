package hub

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The primary device for phone calls (docs/apple/phone-tools.md "Which device takes a call"): the owner's choice goes
// first, is woken when it is away, and another connected device answers, saying so, only when it does not come.

// primaryQueueStore keeps queued writes in memory; meshStore has no durable phone write queue.
type primaryQueueStore struct {
	*meshStore
	queued []db.PhoneWrite
}

func (s *primaryQueueStore) EnqueuePhoneWrite(_ context.Context, id, tool string, args json.RawMessage) (*db.PhoneWrite, error) {
	w := db.PhoneWrite{ID: id, Tool: tool, Args: args, State: "queued"}
	s.mu.Lock()
	s.queued = append(s.queued, w)
	s.mu.Unlock()
	return &w, nil
}

func (s *primaryQueueStore) FindQueuedPhoneWrite(context.Context, string, json.RawMessage) (*db.PhoneWrite, error) {
	return nil, nil
}

func (s *primaryQueueStore) QueuedPhoneWrites(context.Context) ([]db.PhoneWrite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.PhoneWrite
	for _, w := range s.queued {
		if w.State == "queued" {
			out = append(out, w)
		}
	}
	return out, nil
}

func (s *primaryQueueStore) ClaimPhoneWrite(_ context.Context, id string) (bool, error) {
	return s.move(id, "queued", "delivering", nil), nil
}

func (s *primaryQueueStore) FinishPhoneWrite(_ context.Context, id, state string, result json.RawMessage) error {
	s.move(id, "delivering", state, result)
	return nil
}

func (s *primaryQueueStore) move(id, from, to string, result json.RawMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.queued {
		if s.queued[i].ID == id && s.queued[i].State == from {
			s.queued[i].State, s.queued[i].Result = to, result
			return true
		}
	}
	return false
}

func (s *primaryQueueStore) state(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.queued {
		if w.ID == id {
			return w.State
		}
	}
	return ""
}

// DeviceTokens marks revoked credentials as the database does (revoked_at); meshStore only stops their logins.
func (s *primaryQueueStore) DeviceTokens(ctx context.Context) ([]db.DeviceToken, error) {
	tokens, err := s.meshStore.DeviceTokens(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range tokens {
		if s.revoked[tokens[i].ID] {
			at := time.Now()
			tokens[i].RevokedAt = &at
		}
	}
	return tokens, err
}

// primaryPhoneHub is a hub with an iPhone (credential dev_iphone) and an iPad (dev_ipad) paired, registered for tools
// and for silent pushes, neither of them connected.
func primaryPhoneHub(t *testing.T, tools ...string) (*Hub, *primaryQueueStore, *fleetPushSink) {
	t.Helper()
	h := newTestHub(t)
	store := &primaryQueueStore{meshStore: newMeshStore()}
	h.store = store
	sink := &fleetPushSink{}
	h.push.relay = &http.Client{Transport: sink}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ id, name string }{{"iphone", "Test iPhone"}, {"ipad", "Test iPad"}} {
		if err := h.push.register(Device{DeviceID: d.id, Name: d.id, Kind: "iphone", APNSToken: d.id + "-device", Topic: "app.example",
			PushKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), RelayURL: "https://relay.example", RelayGrant: "grant-" + d.id,
			CredentialID: "dev_" + d.id, Credential: "dev_" + d.id}); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: "dev_" + d.id, Name: d.name, Kind: "phone", Role: roleFull},
			hashToken("tok-"+d.id)); err != nil {
			t.Fatal(err)
		}
		h.setPhoneTools(db.PhoneToolRegistration{DeviceID: d.id, Platform: "ios", CredentialID: "dev_" + d.id, Tools: phoneToolsNamed(tools...)})
	}
	waitHelloWrites(t, h)
	return h, store, sink
}

func phoneToolsNamed(names ...string) []proto.Tool {
	tools := make([]proto.Tool, 0, len(names))
	for _, n := range names {
		tools = append(tools, proto.Tool{Name: n})
	}
	return tools
}

// connectPhone connects device's app (credential dev_<device>) and registers its tools.
func connectPhone(t *testing.T, h *Hub, device, name, platform string, tools ...string) *client {
	t.Helper()
	c := &client{hub: h, device: "dev_" + device, role: roleFull, out: make(chan []byte, 16)}
	addTestClient(h, c)
	reg, _ := json.Marshal(map[string]any{"type": "register_phone", "device_id": device, "device_name": name, "platform": platform,
		"tools": phoneToolsNamed(tools...)})
	h.onClientMessage(context.Background(), c, reg)
	waitHelloWrites(t, h)
	return c
}

// choosePrimary chooses credential as the primary device, as the setting route does.
func choosePrimary(h *Hub, credential, name string) {
	h.mu.Lock()
	h.primaryPhone = phonePrimary{CredentialID: credential, Name: name}
	h.mu.Unlock()
}

// answerPhoneCall waits for a tool call on c and answers it with text; "" fails when anything is sent to c.
func answerPhoneCall(t *testing.T, h *Hub, c *client, wantTool, text string) {
	t.Helper()
	select {
	case data := <-c.out:
		var call struct {
			Type, Name string
			CallID     string `json:"call_id"`
		}
		if err := json.Unmarshal(data, &call); err != nil || call.Type != "tool_call" || call.Name != wantTool {
			t.Fatalf("%s got %s (%v), want a %s call", c.device, data, err, wantTool)
		}
		reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID, "content": proto.TextResult(text, false).Content})
		h.onClientMessage(context.Background(), c, reply)
	case <-time.After(time.Minute):
		t.Fatalf("%s got no %s call", c.device, wantTool)
	}
}

func noCallTo(t *testing.T, c *client) {
	t.Helper()
	select {
	case data := <-c.out:
		t.Fatalf("%s got %s", c.device, data)
	default:
	}
}

func callPhoneAsync(h *Hub, name, args string) chan proto.ToolResult {
	done := make(chan proto.ToolResult, 1)
	go func() { done <- h.callPhone(context.Background(), name, json.RawMessage(args)) }()
	return done
}

func TestPhoneCallsFollowTheAutomaticOrderWithoutAPrimaryDevice(t *testing.T) {
	h, _, _ := primaryPhoneHub(t, "phone_location_current")
	iphone := connectPhone(t, h, "iphone", "Test iPhone", "ios", "phone_location_current")
	ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_location_current")
	h.onClientMessage(context.Background(), ipad, []byte(`{"type":"ping"}`)) // the iPad was active last
	if got := h.phoneClient("phone_location_current"); got != ipad {
		t.Fatalf("automatic order picked %s, want the iPad", got.device)
	}
	done := callPhoneAsync(h, "phone_location_current", `{}`)
	answerPhoneCall(t, h, ipad, "phone_location_current", `{"latitude":1}`)
	if got := <-done; got.IsError || textOf(got) != `{"latitude":1}` {
		t.Fatalf("result = %+v, want the iPad's answer untouched", got)
	}
	noCallTo(t, iphone)
}

func TestPhoneCallsGoToAConnectedPrimaryDevice(t *testing.T) {
	h, _, sink := primaryPhoneHub(t, "phone_location_current")
	iphone := connectPhone(t, h, "iphone", "Test iPhone", "ios", "phone_location_current")
	ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_location_current")
	h.onClientMessage(context.Background(), ipad, []byte(`{"type":"ping"}`))
	choosePrimary(h, "dev_iphone", "Test iPhone")
	done := callPhoneAsync(h, "phone_location_current", `{}`)
	answerPhoneCall(t, h, iphone, "phone_location_current", `{"latitude":2}`)
	if got := <-done; got.IsError || textOf(got) != `{"latitude":2}` {
		t.Fatalf("result = %+v, want the iPhone's answer untouched", got)
	}
	noCallTo(t, ipad)
	if n := sink.count("iphone", "background", "") + sink.count("ipad", "background", ""); n != 0 {
		t.Fatalf("a connected primary device was woken: %d pushes", n)
	}
}

func TestPhoneCallsWakeAnOfflinePrimaryDevice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, sink := primaryPhoneHub(t, "phone_health_summary")
		ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_health_summary")
		choosePrimary(h, "dev_iphone", "Test iPhone")
		start := time.Now()
		done := callPhoneAsync(h, "phone_health_summary", `{}`)
		synctest.Wait() // the wake push is out, and the call waits for the iPhone's app
		if got := sink.count("iphone", "background", ""); got != 1 {
			t.Fatalf("pushes to the iPhone = %d, want 1 wake", got)
		}
		if got := sink.count("ipad", "background", ""); got != 0 {
			t.Fatalf("the connected iPad was pushed %d times", got)
		}
		noCallTo(t, ipad) // a connected iPad does not end the wait for the primary device
		iphone := connectPhone(t, h, "iphone", "Test iPhone", "ios", "phone_health_summary")
		answerPhoneCall(t, h, iphone, "phone_health_summary", `{"days":[]}`)
		got := <-done
		if got.IsError || textOf(got) != `{"days":[]}` {
			t.Fatalf("result = %+v, want the woken iPhone's answer untouched", got)
		}
		if waited := time.Since(start); waited >= 20*time.Second {
			t.Fatalf("waited %v: the call should go as soon as the primary device connects", waited)
		}
		noCallTo(t, ipad)
	})
}

func TestAnotherDeviceStandsInWhenThePrimaryDeviceDoesNotWake(t *testing.T) {
	for _, tc := range []struct{ name, answer string }{
		{"object", `{"latitude":3,"longitude":4}`},
		{"plain text", "Health is off on this device."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, _, sink := primaryPhoneHub(t, "phone_location_current")
				ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_location_current")
				choosePrimary(h, "dev_iphone", "Test iPhone")
				start := time.Now()
				done := callPhoneAsync(h, "phone_location_current", `{}`)
				answerPhoneCall(t, h, ipad, "phone_location_current", tc.answer) // after the wait for the iPhone
				got := <-done
				if waited := time.Since(start); waited < 20*time.Second {
					t.Fatalf("the iPad answered after %v, before the wait for the primary device ended", waited)
				}
				if sink.count("iphone", "background", "") != 1 || sink.count("ipad", "background", "") != 0 {
					t.Fatalf("pushes: iPhone %d, iPad %d; want one wake for the iPhone only",
						sink.count("iphone", "background", ""), sink.count("ipad", "background", ""))
				}
				note := prompts.RenderPhoneStandIn(prompts.PhoneStandIn{Device: "Test iPad", Primary: "Test iPhone"})
				if !strings.Contains(note, "Test iPad") || !strings.Contains(note, "Test iPhone") {
					t.Fatalf("note = %q", note)
				}
				if tc.name == "object" {
					// One JSON object stays one: the map card still reads it, with the note as a field.
					var object map[string]any
					if len(got.Content) != 1 || json.Unmarshal([]byte(textOf(got)), &object) != nil || object["device_note"] != note ||
						object["latitude"] != float64(3) || object["longitude"] != float64(4) {
						t.Fatalf("result = %+v, want the object with device_note", got)
					}
					return
				}
				if got.IsError || len(got.Content) != 2 || got.Content[0].Text != tc.answer || got.Content[1].Text != note {
					t.Fatalf("result = %+v, want the answer then the note", got)
				}
			})
		})
	}
}

// A caller that goes away while the hub waits for the primary device leaves nothing on another device.
func TestNoStandInForACallerThatLeftDuringTheWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := primaryPhoneHub(t, "phone_reminders_create")
		ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_reminders_create")
		choosePrimary(h, "dev_iphone", "Test iPhone")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan proto.ToolResult, 1)
		go func() { done <- h.callPhone(ctx, "phone_reminders_create", json.RawMessage(`{"title":"x"}`)) }()
		synctest.Wait() // the call waits for the iPhone
		cancel()
		got := <-done
		synctest.Wait()
		if !got.IsError {
			t.Fatalf("result = %+v, want an error for the gone caller", got)
		}
		noCallTo(t, ipad)
	})
}

func TestPhoneCallsNameThePrimaryDeviceWhenNothingCanAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, sink := primaryPhoneHub(t, "phone_reminders_list")
		choosePrimary(h, "dev_iphone", "Test iPhone")
		got := h.callPhone(context.Background(), "phone_reminders_list", json.RawMessage(`{}`))
		want := prompts.RenderPhonePrimaryOffline(prompts.PhonePrimaryOffline{Primary: "Test iPhone"})
		if !got.IsError || textOf(got) != want || !strings.Contains(want, "Test iPhone") {
			t.Fatalf("result = %+v, want %q", got, want)
		}
		synctest.Wait()
		if sink.count("iphone", "background", "") != 1 || sink.count("ipad", "background", "") != 0 {
			t.Fatalf("pushes: iPhone %d, iPad %d; want one wake for the iPhone only",
				sink.count("iphone", "background", ""), sink.count("ipad", "background", ""))
		}
	})
}

func TestQueuedWritesWaitForThePrimaryDevice(t *testing.T) {
	h, store, _ := primaryPhoneHub(t, "phone_reminders_create")
	ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_reminders_create")
	choosePrimary(h, "dev_iphone", "Test iPhone")
	got := h.callPhone(context.Background(), "phone_reminders_create", json.RawMessage(`{"title":"call back","queue_if_offline":true}`))
	var receipt db.PhoneWrite
	if got.IsError || json.Unmarshal([]byte(textOf(got)), &receipt) != nil || receipt.State != "queued" || store.state(receipt.ID) != "queued" {
		t.Fatalf("result = %+v, want a queue receipt while the primary device is away", got)
	}
	noCallTo(t, ipad)
	// The delivery loop leaves it queued while only the iPad is connected...
	h.deliverPhoneWrites(context.Background())
	noCallTo(t, ipad)
	if state := store.state(receipt.ID); state != "queued" {
		t.Fatalf("with only the iPad connected the write is %s", state)
	}
	// ...and delivers it to the primary device once it connects.
	iphone := connectPhone(t, h, "iphone", "Test iPhone", "ios", "phone_reminders_create")
	delivered := make(chan struct{})
	go func() { h.deliverPhoneWrites(context.Background()); close(delivered) }()
	answerPhoneCall(t, h, iphone, "phone_reminders_create", `{"id":"r1"}`)
	<-delivered
	if state := store.state(receipt.ID); state != "succeeded" {
		t.Fatalf("after the iPhone answered the write is %s", state)
	}
	noCallTo(t, ipad)
}

// With the primary device connected a queued write is not queued: it goes to the primary device at once.
func TestQueueIfOfflineCallsAConnectedPrimaryDevice(t *testing.T) {
	h, store, _ := primaryPhoneHub(t, "phone_reminders_create")
	iphone := connectPhone(t, h, "iphone", "Test iPhone", "ios", "phone_reminders_create")
	ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_reminders_create")
	h.onClientMessage(context.Background(), ipad, []byte(`{"type":"ping"}`))
	choosePrimary(h, "dev_iphone", "Test iPhone")
	done := callPhoneAsync(h, "phone_reminders_create", `{"title":"now","queue_if_offline":true}`)
	answerPhoneCall(t, h, iphone, "phone_reminders_create", `{"id":"r2"}`)
	if got := <-done; got.IsError || textOf(got) != `{"id":"r2"}` {
		t.Fatalf("result = %+v, want the iPhone's answer", got)
	}
	if n := len(store.queued); n != 0 {
		t.Fatalf("%d writes queued with the primary device connected", n)
	}
	noCallTo(t, ipad)
}

func TestOnScreenToolsGoToAConnectedDeviceWhileThePrimaryDeviceIsAway(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, sink := primaryPhoneHub(t, "phone_clipboard_set")
		ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_clipboard_set")
		choosePrimary(h, "dev_iphone", "Test iPhone")
		start := time.Now()
		done := callPhoneAsync(h, "phone_clipboard_set", `{"text":"hello"}`)
		answerPhoneCall(t, h, ipad, "phone_clipboard_set", `{"ok":true}`)
		got := <-done
		if waited := time.Since(start); waited != 0 {
			t.Fatalf("a clipboard call waited %v for the primary device", waited)
		}
		synctest.Wait()
		if n := sink.count("iphone", "background", ""); n != 0 {
			t.Fatalf("a clipboard call woke the iPhone (%d pushes)", n)
		}
		var object map[string]any
		note := prompts.RenderPhoneStandIn(prompts.PhoneStandIn{Device: "Test iPad", Primary: "Test iPhone"})
		if json.Unmarshal([]byte(textOf(got)), &object) != nil || object["device_note"] != note || object["ok"] != true {
			t.Fatalf("result = %+v, want the answer with the note", got)
		}
	})
}

func TestOnScreenToolsGoToAConnectedPrimaryDevice(t *testing.T) {
	h, _, _ := primaryPhoneHub(t, "phone_clipboard_set")
	iphone := connectPhone(t, h, "iphone", "Test iPhone", "ios", "phone_clipboard_set")
	ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_clipboard_set")
	h.onClientMessage(context.Background(), ipad, []byte(`{"type":"ping"}`))
	choosePrimary(h, "dev_iphone", "Test iPhone")
	done := callPhoneAsync(h, "phone_clipboard_set", `{"text":"hello"}`)
	answerPhoneCall(t, h, iphone, "phone_clipboard_set", `{"ok":true}`)
	if got := <-done; got.IsError || textOf(got) != `{"ok":true}` {
		t.Fatalf("result = %+v, want the iPhone's answer untouched", got)
	}
	noCallTo(t, ipad)
}

func TestOnScreenToolsWithNothingConnectedAnswerAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, sink := primaryPhoneHub(t, "phone_clipboard_get")
		choosePrimary(h, "dev_iphone", "Test iPhone")
		start := time.Now()
		got := h.callPhone(context.Background(), "phone_clipboard_get", json.RawMessage(`{}`))
		if !got.IsError || textOf(got) != phoneNotOnScreen || time.Since(start) != 0 {
			t.Fatalf("result = %+v after %v, want the open-Persona answer at once", got, time.Since(start))
		}
		synctest.Wait()
		if n := sink.count("iphone", "background", "") + sink.count("ipad", "background", ""); n != 0 {
			t.Fatalf("a clipboard call woke a device (%d pushes)", n)
		}
	})
}

// What the primary device offers is judged by what its app offers now while it is connected, and by its latest
// registration while it is away: a stale record never sends a call into a 20-second wait.
func TestPrimaryDeviceOffersWhatItOffersNow(t *testing.T) {
	t.Run("connected", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, _, sink := primaryPhoneHub(t, "phone_health_summary")
			connectPhone(t, h, "iphone", "Test iPhone", "ios") // Health turned off: the app offers nothing now
			ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_health_summary")
			h.mu.Lock() // the registration still lists the tool, as when its write is late or failed
			h.phoneTools["iphone"] = db.PhoneToolRegistration{DeviceID: "iphone", Platform: "ios", CredentialID: "dev_iphone",
				Tools: phoneToolsNamed("phone_health_summary"), UpdatedAt: time.Now()}
			h.mu.Unlock()
			choosePrimary(h, "dev_iphone", "Test iPhone")
			start := time.Now()
			done := callPhoneAsync(h, "phone_health_summary", `{}`)
			answerPhoneCall(t, h, ipad, "phone_health_summary", `{"days":[]}`)
			got := <-done
			if time.Since(start) != 0 {
				t.Fatalf("waited %v for a connected primary device that does not offer the tool", time.Since(start))
			}
			synctest.Wait()
			if n := sink.count("iphone", "background", ""); n != 0 {
				t.Fatalf("a connected iPhone was pushed %d times", n)
			}
			var object map[string]any
			if json.Unmarshal([]byte(textOf(got)), &object) != nil || object["device_note"] == nil {
				t.Fatalf("result = %+v, want the note on the iPad's answer", got)
			}
		})
	})
	t.Run("away", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, _, sink := primaryPhoneHub(t, "phone_health_summary")
			ipad := connectPhone(t, h, "ipad", "Test iPad", "ios", "phone_health_summary")
			h.mu.Lock() // an older app's registration under the credential, and the newer one without the tool
			h.phoneTools["dev_iphone"] = db.PhoneToolRegistration{DeviceID: "dev_iphone", CredentialID: "dev_iphone",
				Tools: phoneToolsNamed("phone_health_summary"), UpdatedAt: time.Now().Add(-time.Hour)}
			h.phoneTools["iphone"] = db.PhoneToolRegistration{DeviceID: "iphone", Platform: "ios", CredentialID: "dev_iphone",
				Tools: phoneToolsNamed("phone_location_current"), UpdatedAt: time.Now()}
			h.mu.Unlock()
			choosePrimary(h, "dev_iphone", "Test iPhone")
			start := time.Now()
			done := callPhoneAsync(h, "phone_health_summary", `{}`)
			answerPhoneCall(t, h, ipad, "phone_health_summary", `{"days":[]}`)
			<-done
			if time.Since(start) != 0 {
				t.Fatalf("waited %v on a stale registration", time.Since(start))
			}
			synctest.Wait()
			if n := sink.count("iphone", "background", ""); n != 0 {
				t.Fatalf("the iPhone was woken for a tool its latest registration does not offer (%d pushes)", n)
			}
		})
	})
}

func TestAPrimaryDeviceWithoutTheToolLeavesTheCallToTheAutomaticOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, sink := primaryPhoneHub(t, "phone_location_current") // the iPhone has no phone_notifications_list
		android := connectPhone(t, h, "android", "Test Android", "android", "phone_notifications_list")
		choosePrimary(h, "dev_iphone", "Test iPhone")
		start := time.Now()
		done := callPhoneAsync(h, "phone_notifications_list", `{}`)
		answerPhoneCall(t, h, android, "phone_notifications_list", "[]")
		got := <-done
		if waited := time.Since(start); waited != 0 {
			t.Fatalf("waited %v for a primary device that does not offer the tool", waited)
		}
		synctest.Wait()
		if n := sink.count("iphone", "background", ""); n != 0 {
			t.Fatalf("the iPhone was woken for a tool it does not offer (%d pushes)", n)
		}
		// Another device than the chosen one answered: the model is told, as for any stand-in.
		if len(got.Content) != 2 || got.Content[0].Text != "[]" || !strings.Contains(got.Content[1].Text, "Test Android") {
			t.Fatalf("result = %+v, want the answer and the note", got)
		}
	})
}

func TestPrimaryPhoneSetting(t *testing.T) {
	h, store, _ := primaryPhoneHub(t, "phone_location_current")
	if err := store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: "dev_watch", Name: "Test Watch", Kind: "watch", Role: roleAnswer},
		hashToken("tok-watch")); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	master := h.cfg.Token
	phone := func(out map[string]any) (string, []string) {
		t.Helper()
		p, ok := out["phone"].(map[string]any)
		if !ok {
			p = out // PUT answers the object itself
		}
		var candidates []string
		for _, c := range p["candidates"].([]any) {
			candidates = append(candidates, c.(string))
		}
		return p["primary"].(string), candidates
	}

	code, out := call(t, mux, "GET", "/api/v1/auth/tokens", master, "")
	if primary, candidates := phone(out); code != 200 || primary != "" || !slices.Equal(candidates, []string{"dev_ipad", "dev_iphone"}) {
		t.Fatalf("GET = %d %v, want Automatic and the two devices that offer phone tools", code, out["phone"])
	}

	code, out = call(t, mux, "PUT", "/api/v1/phone/primary", "tok-iphone", `{"credential_id":"dev_iphone"}`)
	if primary, _ := phone(out); code != 200 || primary != "dev_iphone" {
		t.Fatalf("PUT = %d %v", code, out)
	}
	var saved phonePrimary
	if ok, err := store.Setting(context.Background(), phonePrimaryKey, &saved); !ok || err != nil || saved != (phonePrimary{CredentialID: "dev_iphone", Name: "Test iPhone"}) {
		t.Fatalf("saved = %+v (%v, %v)", saved, ok, err)
	}
	if credential, _, offers := h.primaryPhoneFor("phone_location_current"); credential != "dev_iphone" || !offers {
		t.Fatalf("calls read %q (%v)", credential, offers)
	}

	// A hub started later reads the choice back.
	h2 := newTestHub(t)
	h2.store = store
	h2.phoneTools = map[string]db.PhoneToolRegistration{"iphone": {DeviceID: "iphone", CredentialID: "dev_iphone", Tools: phoneToolsNamed("phone_location_current")}}
	h2.loadPhonePrimary(context.Background())
	if credential, _, _ := h2.primaryPhoneFor("phone_location_current"); credential != "dev_iphone" {
		t.Fatalf("after a restart the primary device is %q", credential)
	}

	// Only a device that offers phone tools can be chosen; the watch (answer-only) cannot choose at all.
	if code, out = call(t, mux, "PUT", "/api/v1/phone/primary", master, `{"credential_id":"dev_watch"}`); code != http.StatusConflict || out["code"] != "not_a_phone" {
		t.Fatalf("choosing the watch = %d %v", code, out)
	}
	if code, _ = call(t, mux, "PUT", "/api/v1/phone/primary", master, `{"credential_id":"dev_unknown"}`); code != http.StatusConflict {
		t.Fatalf("choosing an unknown credential = %d", code)
	}
	if code, _ = call(t, mux, "PUT", "/api/v1/phone/primary", "tok-watch", `{"credential_id":""}`); code != http.StatusForbidden {
		t.Fatalf("an answer-only device changed the choice: %d", code)
	}
	if code, _ = call(t, mux, "PUT", "/api/v1/phone/primary", master, `{}`); code != http.StatusBadRequest {
		t.Fatalf("PUT without credential_id = %d", code)
	}

	// Revoking the primary device's credential ends the choice: the list says Automatic, calls follow the automatic order.
	if code, _ = call(t, mux, "DELETE", "/api/v1/auth/tokens/dev_iphone", master, ""); code != 200 {
		t.Fatalf("revoke = %d", code)
	}
	code, out = call(t, mux, "GET", "/api/v1/auth/tokens", master, "")
	if primary, candidates := phone(out); code != 200 || primary != "" || !slices.Equal(candidates, []string{"dev_ipad"}) {
		t.Fatalf("after revoking = %d %v", code, out["phone"])
	}
	if credential, _, _ := h.primaryPhoneFor("phone_location_current"); credential != "" {
		t.Fatalf("calls still go first to the revoked %q", credential)
	}

	if code, out = call(t, mux, "PUT", "/api/v1/phone/primary", master, `{"credential_id":"dev_iphone"}`); code != http.StatusConflict || out["code"] != "not_a_phone" {
		t.Fatalf("choosing a revoked credential = %d %v", code, out)
	}

	// Automatic again, an id of only spaces included.
	code, out = call(t, mux, "PUT", "/api/v1/phone/primary", master, `{"credential_id":"dev_ipad"}`)
	if primary, _ := phone(out); code != 200 || primary != "dev_ipad" {
		t.Fatalf("PUT the iPad = %d %v", code, out)
	}
	code, out = call(t, mux, "PUT", "/api/v1/phone/primary", master, `{"credential_id":"  "}`)
	if primary, _ := phone(out); code != 200 || primary != "" {
		t.Fatalf("PUT Automatic = %d %v", code, out)
	}
}

func TestGoldenPhonePrimaryPrompts(t *testing.T) {
	golden(t, "phone_stand_in", prompts.RenderPhoneStandIn(prompts.PhoneStandIn{Device: "iPad", Primary: "iPhone"}))
	golden(t, "phone_stand_in.unnamed", prompts.RenderPhoneStandIn(prompts.PhoneStandIn{}))
	golden(t, "phone_primary_offline", prompts.RenderPhonePrimaryOffline(prompts.PhonePrimaryOffline{Primary: "iPhone"}))
}
