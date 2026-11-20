package feishubot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

// cardKitCall is one request the CardKit calls made, as the server read it.
type cardKitCall struct {
	method, path string
	body         map[string]any
}

func cardKitClient(t *testing.T, answer func(w http.ResponseWriter, c cardKitCall)) (*Client, *[]cardKitCall) {
	t.Helper()
	var calls []cardKitCall
	c, _, _, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
		raw, _ := io.ReadAll(r.Body)
		k := cardKitCall{method: r.Method, path: r.URL.Path}
		_ = json.Unmarshal(raw, &k.body)
		calls = append(calls, k)
		answer(w, k)
	})
	return c, &calls
}

// A card entity is made from the card's JSON, and sent by its id.
func TestCardCreate(t *testing.T) {
	c, calls := cardKitClient(t, func(w http.ResponseWriter, _ cardKitCall) {
		okJSON(w, map[string]any{"code": 0, "data": map[string]any{"card_id": "c1"}})
	})
	card := NewCard("t", Blue, "s").Streaming().JSON()
	id, err := c.CardCreate(context.Background(), card)
	if err != nil || id != "c1" {
		t.Fatalf("CardCreate = %q, %v", id, err)
	}
	got := (*calls)[0]
	if got.method != http.MethodPost || got.path != "/open-apis/cardkit/v1/cards" || got.body["type"] != "card_json" || got.body["data"] != card {
		t.Fatalf("call = %+v", got)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(CardEntityContent("c1")), &content); err != nil {
		t.Fatal(err)
	}
	if content["type"] != "card" || content["data"].(map[string]any)["card_id"] != "c1" {
		t.Fatalf("content = %v", content)
	}
}

// The writes into an entity: the whole card, one element's text (streamed), its settings; each with its sequence and
// a uuid made of the card and the sequence, so a retry lands once.
func TestCardWrites(t *testing.T) {
	c, calls := cardKitClient(t, func(w http.ResponseWriter, _ cardKitCall) { okJSON(w, map[string]any{"code": 0}) })
	ctx := context.Background()
	if err := c.CardUpdate(ctx, "c1", `{"schema":"2.0"}`, 7); err != nil {
		t.Fatal(err)
	}
	if err := c.CardStream(ctx, "c1", "live", "hello", 8); err != nil {
		t.Fatal(err)
	}
	if err := c.CardSettings(ctx, "c1", false, 9); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		method, path string
		seq          float64
	}{
		{http.MethodPut, "/open-apis/cardkit/v1/cards/c1", 7},
		{http.MethodPut, "/open-apis/cardkit/v1/cards/c1/elements/live/content", 8},
		{http.MethodPatch, "/open-apis/cardkit/v1/cards/c1/settings", 9},
	}
	for i, w := range want {
		got := (*calls)[i]
		if got.method != w.method || got.path != w.path || got.body["sequence"] != w.seq || got.body["uuid"] != cardUUID("c1", int64(w.seq)) {
			t.Errorf("call %d = %+v, want %+v", i, got, w)
		}
	}
	if card := (*calls)[0].body["card"].(map[string]any); card["type"] != "card_json" || card["data"] != `{"schema":"2.0"}` {
		t.Errorf("update body = %v", (*calls)[0].body)
	}
	if (*calls)[1].body["content"] != "hello" {
		t.Errorf("stream body = %v", (*calls)[1].body)
	}
	var settings map[string]any
	_ = json.Unmarshal([]byte((*calls)[2].body["settings"].(string)), &settings)
	if settings["config"].(map[string]any)["streaming_mode"] != false {
		t.Errorf("settings = %v", settings)
	}
}

// A write whose answer was lost and that is sent again finds its uuid used: it landed, which is no error. Streaming
// that ended by itself (ten minutes after it was turned on) says so.
func TestCardWriteAnswers(t *testing.T) {
	code := 200770
	c, _ := cardKitClient(t, func(w http.ResponseWriter, _ cardKitCall) {
		okJSON(w, map[string]any{"code": code, "msg": "this UUID has been recently consumed"})
	})
	if err := c.CardStream(context.Background(), "c1", "live", "x", 1); err != nil {
		t.Fatalf("a used uuid = %v", err)
	}
	code = 300309
	err := c.CardStream(context.Background(), "c1", "live", "x", 2)
	if !StreamClosed(err) {
		t.Fatalf("closed = %v", err)
	}
	if StreamClosed(errors.New("feishu stream card: 300309 streaming mode is closed")) != true {
		t.Error("the code over the bus is not read")
	}
	if StreamClosed(nil) || StreamClosed(&APIError{Code: 230020}) {
		t.Error("another error read as closed")
	}
}
