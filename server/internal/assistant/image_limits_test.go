package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A new conversation's first message holds the pictures any message holds (#12973): more is a 413 with the limits as
// numbers, before anything starts; a body over the limit is a 413 too, not the 400 of a malformed one.
func TestStartCountsTheFirstMessagesPictures(t *testing.T) {
	s, f := newQuietService(t)
	start := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	pictures := func(n int) string {
		images := make([]proto.Image, n)
		for i := range images {
			images[i] = proto.Image{Mime: "image/jpeg", Data: "AAEC"}
		}
		b, _ := json.Marshal(map[string]any{"prompt": "look", "images": images})
		return string(b)
	}
	f.mu.Lock()
	made := f.made
	f.mu.Unlock()
	w := start(pictures(proto.ImagesPerMessage + 1))
	var body struct {
		Code      string            `json:"code"`
		Error     string            `json:"error"`
		Delivered string            `json:"delivered"`
		Limit     proto.ImageLimits `json:"limit"`
		Got       struct{ Images int }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusRequestEntityTooLarge || err != nil ||
		body.Code != proto.CodeImagesOverLimit || body.Delivered != "none" || body.Limit != proto.DefaultImageLimits ||
		body.Got.Images != proto.ImagesPerMessage+1 || !strings.Contains(body.Error, "21") {
		t.Fatalf("21 pictures: %d %s", w.Code, w.Body)
	}
	f.mu.Lock()
	if f.made != made {
		t.Errorf("a refused start made a session")
	}
	f.mu.Unlock()
	if w := start(pictures(proto.ImagesPerMessage)); w.Code != http.StatusOK {
		t.Fatalf("20 pictures: %d %s", w.Code, w.Body)
	}
	over := `{"prompt":"` + strings.Repeat("a", proto.MessageBodyLimit) + `"}`
	if w := start(over); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), `"code":"too_large"`) {
		t.Fatalf("a body over the limit: %d %.200s", w.Code, w.Body)
	}
}
