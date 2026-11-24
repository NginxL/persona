package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// picturesBody is a JSON body with fields and n pictures whose data are size bytes of base64 each.
func picturesBody(t *testing.T, fields map[string]any, n, size int) string {
	t.Helper()
	images := make([]proto.Image, n)
	for i := range images {
		images[i] = proto.Image{Mime: "image/jpeg", Data: strings.Repeat("A", size)}
	}
	fields["images"] = images
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A message's pictures (#12973): the hub refuses more than a message holds, with the limits as numbers and before the
// node hears of it; it lets through what older clients send (an 8 MB original from the web: the bytes of one picture
// are a client's target, not refused), and a new Agent's first message may be as large as any message.
func TestMessagePicturesAreCountedNotWeighed(t *testing.T) {
	h := newTestHub(t)
	if !h.loadMachineTrust(context.Background()) {
		t.Fatal("machine trust could not be initialized")
	}
	calls := make(chan map[string]any, 16)
	modelsNode(t, h, MachineConfig{ID: "test-node"}, func(method string, p map[string]any) (any, error) {
		switch method {
		case "session.send":
			calls <- p
			return map[string]any{"ok": true}, nil
		case "session.start":
			calls <- p
			return map[string]any{"session": proto.Session{SessionID: "new", Cwd: "/work", Launcher: "agy"}}, nil
		}
		return map[string]any{}, nil
	})
	m := h.localMachine("test-node")
	m.mu.Lock()
	m.sessions = []proto.Session{{SessionID: "s1", Status: proto.StatusIdle}}
	m.mu.Unlock()
	connectAssistant(t, h)
	images := func(p map[string]any) int {
		list, _ := p["images"].([]any)
		return len(list)
	}
	refused := func(what string, r interface {
		Result() *http.Response
	}, body string) {
		t.Helper()
		var b struct {
			Code      string            `json:"code"`
			Error     string            `json:"error"`
			Delivered string            `json:"delivered"`
			Retryable *bool             `json:"retryable"`
			Limit     proto.ImageLimits `json:"limit"`
			Got       struct {
				Images int `json:"images"`
			} `json:"got"`
		}
		if err := json.Unmarshal([]byte(body), &b); r.Result().StatusCode != http.StatusRequestEntityTooLarge || err != nil ||
			b.Code != proto.CodeImagesOverLimit || b.Delivered != "none" || b.Retryable == nil || *b.Retryable ||
			b.Limit != proto.DefaultImageLimits || b.Got.Images != proto.ImagesPerMessage+1 ||
			!strings.Contains(b.Error, "21") || !strings.Contains(b.Error, "20") {
			t.Fatalf("%s: %d %s", what, r.Result().StatusCode, body)
		}
		select {
		case p := <-calls:
			t.Fatalf("%s reached the node: %d pictures", what, images(p))
		default:
		}
	}

	over := picturesBody(t, map[string]any{"text": "look"}, proto.ImagesPerMessage+1, 4)
	w := modelsRequest(h, "POST", "/api/v1/sessions/test-node/s1/messages", over)
	refused("a message of 21 pictures", w, w.Body.String())
	if w := modelsRequest(h, "POST", "/api/v1/sessions/test-node/s1/messages", picturesBody(t, map[string]any{"text": "look"}, proto.ImagesPerMessage, 4)); w.Code != http.StatusOK {
		t.Fatalf("a message of 20 pictures: %d %s", w.Code, w.Body)
	}
	if p := <-calls; images(p) != proto.ImagesPerMessage {
		t.Fatalf("the node got %d pictures", images(p))
	}
	// An older web client's original: 8 MB, 10.7 MB of base64, over image_bytes and well under the body's limit.
	if w := modelsRequest(h, "POST", "/api/v1/sessions/test-node/s1/messages", picturesBody(t, map[string]any{"text": "look"}, 1, 8_000_000/3*4)); w.Code != http.StatusOK {
		t.Fatalf("an 8 MB original: %d %.300s", w.Code, w.Body)
	}
	if p := <-calls; images(p) != 1 {
		t.Fatalf("the node got %d pictures", images(p))
	}

	start := map[string]any{"machine_id": "test-node", "cwd": "/work", "launcher": "agy", "prompt": "look"}
	w = modelsRequest(h, "POST", "/api/v1/sessions", picturesBody(t, start, proto.ImagesPerMessage+1, 4))
	refused("a first message of 21 pictures", w, w.Body.String())
	// 20 pictures of 400 KB: 8 MB, twice the 4 MiB a start took before.
	if w := modelsRequest(h, "POST", "/api/v1/sessions", picturesBody(t, start, proto.ImagesPerMessage, 400_000)); w.Code != http.StatusOK {
		t.Fatalf("a first message of 20 pictures, 8 MB: %d %.300s", w.Code, w.Body)
	}
	if p := <-calls; images(p) != proto.ImagesPerMessage {
		t.Fatalf("the node's start got %d pictures", images(p))
	}
}

// hello publishes the limits the send routes check.
func TestHelloPublishesImageLimits(t *testing.T) {
	b, err := json.Marshal((&Hub{cfg: &Config{}}).hello()["image_limits"])
	if err != nil {
		t.Fatal(err)
	}
	var got proto.ImageLimitsTable
	if err := json.Unmarshal(b, &got); err != nil || got.ImageLimits != proto.DefaultImageLimits || len(got.Agents) != 0 {
		t.Fatalf("hello's image_limits: %s", b)
	}
	if want := `{"request_bytes":33554432,"image_bytes":3750000,"max_side":1600,"per_message":20}`; string(b) != want {
		t.Fatalf("hello's image_limits: %s, want %s", b, want)
	}
}
