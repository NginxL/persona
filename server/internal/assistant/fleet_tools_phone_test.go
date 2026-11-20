package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// flakyPhoneHub fails the phone's tool list while fails is not 0 (counting down; below 0: always), then lists tools.
type flakyPhoneHub struct {
	*fakeHub
	mu    sync.Mutex
	fails int
	asked int
	tools []proto.Tool
}

func (h *flakyPhoneHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID != assistantwire.AppPhone || p.Method != assistantwire.PhoneTools {
		return h.fakeHub.AppCall(ctx, p)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked++
	if h.fails != 0 {
		if h.fails > 0 {
			h.fails--
		}
		return nil, errors.New("the hub's link is down")
	}
	return json.Marshal(map[string]any{"tools": h.tools})
}

func (h *flakyPhoneHub) set(fails int, tools ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fails, h.asked, h.tools = fails, 0, []proto.Tool{}
	for _, name := range tools {
		h.tools = append(h.tools, proto.Tool{Name: name})
	}
}

func (h *flakyPhoneHub) askedTimes() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.asked
}

func flakyPhoneService(t *testing.T) (*Service, *flakyPhoneHub) {
	t.Helper()
	retry := phoneToolRetry
	phoneToolRetry = time.Millisecond
	t.Cleanup(func() { phoneToolRetry = retry })
	h := &flakyPhoneHub{fakeHub: newFakeHub()}
	return New(Options{Hub: h, Logf: t.Logf}), h
}

func phoneToolNames(tools []proto.Tool) []string {
	names := []string{}
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// A conversation that starts while the hub's link comes back still gets the phone's tools (#6422).
func TestPhoneToolsAskTheHubAgain(t *testing.T) {
	s, h := flakyPhoneService(t)
	h.set(2, "phone_reminders_create")
	if got := phoneToolNames(s.phoneTools(context.Background())); !slices.Equal(got, []string{"phone_reminders_create"}) {
		t.Fatalf("tools = %v", got)
	}
	if n := h.askedTimes(); n != 3 {
		t.Fatalf("asked %d times, want 3", n)
	}
	// The conversation's list itself carries them.
	h.set(1, "phone_reminders_create")
	if got := phoneToolNames(s.mcpTools(context.Background(), false)); !slices.Contains(got, "phone_reminders_create") {
		t.Fatalf("mcpTools = %v", got)
	}
}

// A hub that keeps failing: the list it last gave, not none for the conversation's whole life.
func TestPhoneToolsTakeTheHubsLastList(t *testing.T) {
	s, h := flakyPhoneService(t)
	h.set(0, "phone_reminders_create", "phone_location_current")
	s.phoneTools(context.Background())
	h.set(-1)
	if got := phoneToolNames(s.phoneTools(context.Background())); !slices.Equal(got, []string{"phone_reminders_create", "phone_location_current"}) {
		t.Fatalf("tools = %v", got)
	}
	if n := h.askedTimes(); n != phoneToolTries {
		t.Fatalf("asked %d times, want %d", n, phoneToolTries)
	}
}

// What the hub answers is what the phone offers now: an empty list (every source turned off) replaces the kept one.
func TestPhoneToolsAnEmptyAnswerIsKeptToo(t *testing.T) {
	s, h := flakyPhoneService(t)
	h.set(0, "phone_reminders_create")
	s.phoneTools(context.Background())
	h.set(0)
	if got := s.phoneTools(context.Background()); len(got) != 0 {
		t.Fatalf("tools = %v, want none", phoneToolNames(got))
	}
	h.set(-1)
	if got := s.phoneTools(context.Background()); len(got) != 0 {
		t.Fatalf("after a failure: %v, want the empty list kept", phoneToolNames(got))
	}
}

// A manager the hub never answered has nothing to fall back on; a request that stopped waiting is not retried.
func TestPhoneToolsNoneBeforeTheHubAnswered(t *testing.T) {
	s, h := flakyPhoneService(t)
	h.set(-1)
	if got := s.phoneTools(context.Background()); got != nil {
		t.Fatalf("tools = %v, want none", phoneToolNames(got))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.set(-1)
	s.phoneTools(ctx)
	if n := h.askedTimes(); n != 1 {
		t.Fatalf("a cancelled request asked %d times, want 1", n)
	}
}

// The model-facing create result names the attested phone that executed it, without losing the quick-result payload.
func TestPhoneCreateResultsNameExecutingDevice(t *testing.T) {
	for _, tc := range []struct {
		name, tool, payload        string
		sourceName, sourcePlatform string
	}{
		{
			name: "Android calendar", tool: "phone_calendar_create_event",
			payload:        `{"id":"E1","title":"Dentist","start":"2026-11-13T15:30:00Z","end":"2026-11-13T16:00:00Z","calendar":"Personal"}`,
			sourceName:     "Pixel 9",
			sourcePlatform: "android",
		},
		{
			name: "iOS reminder", tool: "phone_reminders_create",
			payload:        `{"id":"R1","title":"Call","due":"2026-11-13T15:30:00Z","list":"Personal"}`,
			sourceName:     "Work iPhone",
			sourcePlatform: "ios",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHub()
			h.answerApp(assistantwire.AppPhone, assistantwire.PhoneCall, func(json.RawMessage) (any, error) {
				return map[string]any{
					"content":                []proto.Content{{Type: "text", Text: tc.payload}},
					"is_error":               false,
					"source_device_name":     tc.sourceName,
					"source_device_platform": tc.sourcePlatform,
				}, nil
			})
			s := newTestService(t, h)
			result := s.mcpCall(context.Background(), "server", "s1", tc.tool, json.RawMessage(`{}`), false)
			if result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
				t.Fatalf("create result = %+v", result)
			}
			var got, want map[string]any
			if err := json.Unmarshal([]byte(toolText(result)), &got); err != nil {
				t.Fatalf("create result text is not JSON: %q: %v", toolText(result), err)
			}
			if err := json.Unmarshal([]byte(tc.payload), &want); err != nil {
				t.Fatal(err)
			}
			want["source_device_name"], want["source_device_platform"] = tc.sourceName, tc.sourcePlatform
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("create result text = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// Attribution never turns a failure, receipt, legacy response, or another tool's response into a successful create.
func TestPhoneCreateResultSourceLeavesOtherResultsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, tool, text  string
		isError, attested bool
	}{
		{"legacy create", "phone_calendar_create_event", `{"id":"E1","title":"Dentist"}`, false, false},
		{"malformed JSON", "phone_calendar_create_event", `{"id":`, false, true},
		{"non-object JSON", "phone_reminders_create", `["created"]`, false, true},
		{"failed create", "phone_reminders_create", `{"error":"denied"}`, true, true},
		{"queued create", "phone_reminders_create", `{"id":"queue-1","tool":"phone_reminders_create","args":{"title":"Later"},"state":"queued"}`, false, true},
		{"unrelated tool", "phone_device_info", `{"id":"phone-1","name":"Pixel 9"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHub()
			h.answerApp(assistantwire.AppPhone, assistantwire.PhoneCall, func(json.RawMessage) (any, error) {
				response := map[string]any{
					"content":  []proto.Content{{Type: "text", Text: tc.text}},
					"is_error": tc.isError,
				}
				if tc.attested {
					response["source_device_name"] = "Pixel 9"
					response["source_device_platform"] = "android"
				}
				return response, nil
			})
			s := newTestService(t, h)
			result := s.mcpCall(context.Background(), "server", "s1", tc.tool, json.RawMessage(`{}`), false)
			if result.IsError != tc.isError || len(result.Content) != 1 ||
				result.Content[0].Type != "text" || result.Content[0].Text != tc.text {
				t.Fatalf("result changed:\n got  %+v\n want text %q, is_error %t", result, tc.text, tc.isError)
			}
		})
	}
}

// Only the hub's selected-device attribution may name a source; payload claims are not independently trusted.
func TestPhoneCreateResultUsesOnlyAttestedSource(t *testing.T) {
	for _, tc := range []struct {
		name, tool, payload        string
		attested                   bool
		sourceName, sourcePlatform string
		wantName, wantPlatform     string
	}{
		{
			name: "platform replaces an unattested name", tool: "phone_calendar_create_event",
			payload: `{"id":"42","title":"Dentist","source_device_name":"Work iPhone"}`, attested: true,
			sourcePlatform: "android", wantPlatform: "android",
		},
		{
			name: "name replaces an unattested platform", tool: "phone_calendar_create_event",
			payload: `{"id":"42","title":"Dentist","source_device_platform":"ios"}`, attested: true,
			sourceName: "Pixel 9", wantName: "Pixel 9",
		},
		{
			name: "an old hub drops a claimed identity", tool: "phone_calendar_create_event",
			payload: `{"id":"42","title":"Dentist","source_device_name":"Work iPhone","source_device_platform":"ios"}`,
		},
		{
			name: "blank attribution drops a claimed identity", tool: "phone_calendar_create_event",
			payload: `{"id":"42","title":"Dentist","source_device_name":"Work iPhone","source_device_platform":"ios"}`, attested: true,
			sourceName: "  ", sourcePlatform: "\t",
		},
		{
			name: "a queued receipt drops a claimed identity", tool: "phone_reminders_create",
			payload: `{"id":"queue-1","state":"queued","args":{"title":"Later"},"source_device_name":"Work iPhone","source_device_platform":"ios"}`, attested: true,
			sourceName: "Pixel 9", sourcePlatform: "android",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHub()
			h.answerApp(assistantwire.AppPhone, assistantwire.PhoneCall, func(json.RawMessage) (any, error) {
				response := map[string]any{
					"content": []proto.Content{{Type: "text", Text: tc.payload}}, "is_error": false,
				}
				if tc.attested {
					response["source_device_name"] = tc.sourceName
					response["source_device_platform"] = tc.sourcePlatform
				}
				return response, nil
			})
			s := newTestService(t, h)
			result := s.mcpCall(context.Background(), "server", "s1", tc.tool, json.RawMessage(`{}`), false)
			var got, original map[string]any
			if err := json.Unmarshal([]byte(toolText(result)), &got); err != nil {
				t.Fatalf("result text is not JSON: %q: %v", toolText(result), err)
			}
			if err := json.Unmarshal([]byte(tc.payload), &original); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{}
			for key, value := range original {
				if key != "source_device_name" && key != "source_device_platform" {
					want[key] = value
				}
			}
			if tc.wantName != "" {
				want["source_device_name"] = tc.wantName
			}
			if tc.wantPlatform != "" {
				want["source_device_platform"] = tc.wantPlatform
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("result text = %s, want only attested source fields %s", gotJSON, wantJSON)
			}
		})
	}
}
