package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/botcard"
)

// fakeDetourAPI answers reply with replyCode, chat sends with sendCardCode for cards and 0 for rich text. A client
// built with failReplyTransport gets a transport error for the reply path instead.
type fakeDetourAPI struct {
	srv          *httptest.Server
	mu           sync.Mutex
	replyCode    int
	sendCardCode int
	replies      []map[string]any
	sends        []map[string]any
}

func newFakeDetourAPI(t *testing.T) *fakeDetourAPI {
	f := &fakeDetourAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The machine's localhost port discovery probes ephemeral ports; only real Feishu POSTs are recorded.
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tok", "expire": 7200})
			return
		}
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		switch {
		case strings.HasSuffix(r.URL.Path, "/reply"):
			f.mu.Lock()
			f.replies = append(f.replies, in)
			code := f.replyCode
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": map[string]any{"message_id": "om_reply"}})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			f.mu.Lock()
			f.sends = append(f.sends, in)
			code := 0
			if in["msg_type"] == botcard.TypeInteractive {
				code = f.sendCardCode
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": map[string]any{"message_id": "om_send"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// client returns a client pointed at the fake; with replyFail its transport errors on reply paths (a dropped
// connection: no business answer, so the detour must not trigger).
func (f *fakeDetourAPI) client(replyFail bool) botport.Client {
	if !replyFail {
		return botport.New("cli", "sec", f.srv.URL)
	}
	base := f.srv.Client().Transport
	return botport.NewWithHTTP("cli", "sec", f.srv.URL, &http.Client{Transport: roundTripFunc2(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/reply") {
			return nil, errors.New("connection reset by peer")
		}
		return base.RoundTrip(req)
	})})
}

type roundTripFunc2 func(*http.Request) (*http.Response, error)

func (fn roundTripFunc2) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func detourCard() string {
	return botcard.NewCard("title", "green", "summary").Markdown("card body 4175").JSON()
}

func (f *fakeDetourAPI) snapshot() (replies, sends []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any{}, f.replies...), append([]map[string]any{}, f.sends...)
}

// The parent is there: one reply, no chat send.
func TestBotPostRepliesWhenParentPresent(t *testing.T) {
	needsLarkModule(t)
	f := newFakeDetourAPI(t)
	h := newTestManager(t)
	card := detourCard()
	id, err := h.botPostJSON(context.Background(), f.client(false), "chat1", "om_p", card, "fb")
	if err != nil || id != "om_reply" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	replies, sends := f.snapshot()
	if len(replies) != 1 || len(sends) != 0 || replies[0]["content"] != card {
		t.Fatalf("replies=%v sends=%v", replies, sends)
	}
}

// The parent was recalled (230011) or deleted (230110): the same card lands as a chat send with a fresh one-shot key.
func TestBotPostDetoursWithdrawnReplyToSend(t *testing.T) {
	needsLarkModule(t)
	for _, code := range []int{230011, 230110} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := newFakeDetourAPI(t)
			f.replyCode = code
			h := newTestManager(t)
			card := detourCard()
			id, err := h.botPostJSON(context.Background(), f.client(false), "chat1", "om_p", card, "fb")
			if err != nil || id != "om_send" {
				t.Fatalf("code %d: id=%q err=%v", code, id, err)
			}
			replies, sends := f.snapshot()
			if len(replies) != 1 || len(sends) != 1 {
				t.Fatalf("code %d: replies=%v sends=%v", code, replies, sends)
			}
			if sends[0]["content"] != card || sends[0]["msg_type"] != botcard.TypeInteractive {
				t.Fatalf("code %d: detoured send changed the content: %v", code, sends[0])
			}
			if u, _ := replies[0]["uuid"].(string); !strings.HasPrefix(u, "r:") {
				t.Fatalf("reply uuid %q is not the anchored key", u)
			}
			if u, _ := sends[0]["uuid"].(string); !strings.HasPrefix(u, "s:") {
				t.Fatalf("detour send uuid %q is not a one-shot key", u)
			}
		})
	}
}

// A network failure on reply must not detour: nothing is sent to the chat, and the transport error comes back.
func TestBotPostNetworkFailureDoesNotDetour(t *testing.T) {
	needsLarkModule(t)
	f := newFakeDetourAPI(t)
	h := newTestManager(t)
	id, err := h.botPostJSON(context.Background(), f.client(true), "chat1", "om_p", detourCard(), "fb")
	if err == nil || id != "" || !strings.Contains(err.Error(), "reset") {
		t.Fatalf("id=%q err=%v, want the transport error and no message", id, err)
	}
	replies, sends := f.snapshot() // the server never saw a reply body; no send may happen either
	if len(replies) != 0 || len(sends) != 0 {
		t.Fatalf("replies=%v sends=%v", replies, sends)
	}
}

// After the detour, a card the gateway still refuses goes out as a rich-text send; the dead reply is not retried.
func TestBotPostDetourThenCardRejectedFallsBackBySend(t *testing.T) {
	needsLarkModule(t)
	f := newFakeDetourAPI(t)
	f.replyCode = 230011    // parent recalled
	f.sendCardCode = 230099 // CardRejected
	h := newTestManager(t)
	id, err := h.botPostJSON(context.Background(), f.client(false), "chat1", "om_p", detourCard(), "plain fallback 4175")
	if err != nil || id != "om_send" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	replies, sends := f.snapshot()
	if len(replies) != 1 || len(sends) != 2 {
		t.Fatalf("replies=%v sends=%v", replies, sends)
	}
	if sends[0]["msg_type"] != botcard.TypeInteractive || sends[1]["msg_type"] != botcard.TypePost {
		t.Fatalf("want card then rich-text send, got %v then %v", sends[0]["msg_type"], sends[1]["msg_type"])
	}
	if content, _ := sends[1]["content"].(string); !strings.Contains(content, "plain fallback 4175") {
		t.Fatalf("fallback send content %q", content)
	}
}

// A card refused while the parent is alive keeps the existing behavior: the fallback replies too, never a send.
func TestBotPostCardRejectedFallbackStillReplies(t *testing.T) {
	needsLarkModule(t)
	f := newFakeDetourAPI(t)
	f.replyCode = 230099 // the card reply is refused with a card error; the code is not 230011, so no detour
	h := newTestManager(t)
	if id, err := h.botPostJSON(context.Background(), f.client(false), "chat1", "om_p", detourCard(), "plain fallback 4175"); err == nil || id != "" {
		t.Fatalf("id=%q err=%v, want the fallback reply refused too", id, err)
	}
	replies, sends := f.snapshot()
	if len(replies) != 2 || len(sends) != 0 {
		t.Fatalf("replies=%v sends=%v", replies, sends)
	}
	if replies[1]["msg_type"] != botcard.TypePost {
		t.Fatalf("second reply %v is not the rich-text fallback", replies[1])
	}
}

// botNote to a withdrawn parent lands in the chat as a text send with the same text and a fresh one-shot key.
func TestBotNoteDetoursWithdrawnReplyToSend(t *testing.T) {
	needsLarkModule(t)
	f := newFakeDetourAPI(t)
	f.replyCode = 230011
	h := newTestManager(t)
	const note = "note after withdrawn 4457"
	h.botNote(context.Background(), f.client(false), "chat1", "om_n", note)
	replies, sends := f.snapshot()
	wantContent := botcard.TextContent(note)
	if len(replies) != 1 || replies[0]["content"] != wantContent || len(sends) != 1 || sends[0]["content"] != wantContent || sends[0]["msg_type"] != botcard.TypeText {
		t.Fatalf("replies=%v sends=%v", replies, sends)
	}
	if u, _ := sends[0]["uuid"].(string); !strings.HasPrefix(u, "s:") {
		t.Fatalf("detoured send uuid %q is not a one-shot key", u)
	}
}

// A dropped connection on the note's reply must not detour: nothing reaches the chat.
func TestBotNoteNetworkFailureDoesNotDetour(t *testing.T) {
	needsLarkModule(t)
	f := newFakeDetourAPI(t)
	newTestManager(t).botNote(context.Background(), f.client(true), "chat1", "om_n", "note 4457")
	if replies, sends := f.snapshot(); len(replies) != 0 || len(sends) != 0 {
		t.Fatalf("replies=%v sends=%v, want no traffic after a transport error", replies, sends)
	}
}
