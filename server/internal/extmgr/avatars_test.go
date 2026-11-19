package extmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extwire"
)

// pictureConnector is a chat app whose people have pictures (made-up ones): a.u1.501 a real PNG, a.svg and a.html
// what a careless or hostile extension might hand over as a picture.
type pictureConnector struct {
	state   atomic.Value // string
	fetched atomic.Int32
}

func (*pictureConnector) ID() string   { return "example" }
func (*pictureConnector) Name() string { return "Example" }
func (*pictureConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapThreads}}
}
func (c *pictureConnector) Status(context.Context) connector.Status {
	return connector.Status{State: c.state.Load().(string)}
}
func (*pictureConnector) Thread(context.Context, string, string, int) (connector.ThreadPage, error) {
	return connector.ThreadPage{}, errors.New("no conversations here")
}
func (c *pictureConnector) Media(_ context.Context, ref string) (connector.Media, error) {
	c.fetched.Add(1)
	switch ref {
	case "a.u1.501":
		return connector.Media{Mime: "image/png", Data: avatarPNG(300, 300)}, nil
	case "a.svg":
		return connector.Media{Mime: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)}, nil
	case "a.html":
		return connector.Media{Mime: "image/png", Data: []byte("<!doctype html><script>alert(1)</script>")}, nil
	}
	return connector.Media{}, errors.New("no such picture")
}

func avatarPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func avatarsHub(t *testing.T) (*Manager, *pictureConnector, http.Handler) {
	t.Helper()
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(nil)
	c := &pictureConnector{}
	c.state.Store(connector.StateOK)
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	return h, c, h.routes()
}

// The avatars route (#10923): the lists' path (extwire.AvatarURL) gives the picture drawn again at the size asked,
// never the bytes as an extension handed them, to the user's own devices only.
func TestAvatarsRoute(t *testing.T) {
	h, c, mux := avatarsHub(t)
	phone := deviceToken("phone", roleFull)
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	pic := extwire.AvatarURL("example", "a.u1.501")
	if pic != "/api/v1/avatars/example/YS51MS41MDE" {
		t.Fatalf("path %q", pic)
	}
	// A screen of rows asks at once: one Media call, every row its picture.
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := get(phone, pic+"?size=120")
			hd := w.Header()
			if w.Code != 200 || hd.Get("Content-Type") != "image/jpeg" || hd.Get("X-Content-Type-Options") != "nosniff" ||
				!strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(hd.Get("Cache-Control"), "immutable") {
				t.Errorf("%d %v", w.Code, hd)
				return
			}
			if cfg, _, err := image.DecodeConfig(w.Body); err != nil || cfg.Width != 128 {
				t.Errorf("drawn %v %v, want 128 px wide", cfg, err)
			}
		}()
	}
	wg.Wait()
	if n := c.fetched.Load(); n != 1 {
		t.Fatalf("Media called %d times, want once", n)
	}
	// Another size of a kept source asks the connector nothing, even signed out.
	c.state.Store(connector.StateNotSignedIn)
	if w := get(phone, pic+"?size=64"); w.Code != 200 || c.fetched.Load() != 1 {
		t.Fatalf("another size: %d, %d Media calls", w.Code, c.fetched.Load())
	}
	// A picture its conversation page kept is there signed out too; one never seen is not.
	if err := h.media.put(h.mediaDir(), "example", "a.u2.7", connector.Media{Mime: "image/png", Data: avatarPNG(40, 40)}); err != nil {
		t.Fatal(err)
	}
	if w := get(phone, extwire.AvatarURL("example", "a.u2.7")); w.Code != 200 {
		t.Fatalf("kept from its page: %d %s", w.Code, w.Body)
	}
	if w := get(phone, extwire.AvatarURL("example", "a.u3.9")); w.Code != http.StatusBadGateway || c.fetched.Load() != 1 {
		t.Fatalf("signed out: %d, %d Media calls", w.Code, c.fetched.Load())
	}
	c.state.Store(connector.StateOK)
	for ref, want := range map[string]int{"a.svg": http.StatusUnsupportedMediaType, "a.html": http.StatusUnsupportedMediaType, "a.gone": http.StatusBadGateway} {
		w := get(phone, extwire.AvatarURL("example", ref))
		if w.Code != want || strings.Contains(w.Body.String(), "<svg") || strings.Contains(w.Body.String(), "<script") {
			t.Errorf("%s: %d %q, want %d and none of its bytes", ref, w.Code, w.Body, want)
		}
	}
	// Addresses: https on the open internet only.
	for _, addr := range []string{"https://127.0.0.1/a.png", "https://[::1]/a.png"} {
		if w := get(phone, extwire.AvatarURL("example", addr)); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "address_not_allowed") {
			t.Errorf("%s: %d %s", addr, w.Code, w.Body)
		}
	}
	for path, want := range map[string]int{
		"/api/v1/avatars/example/" + b64("http://avatars.example.com/u/1"): http.StatusNotFound, // not a value AvatarURL makes
		"/api/v1/avatars/example/not*base64":                               http.StatusNotFound,
		"/api/v1/avatars/nosuch/YS51MS41MDE":                               http.StatusNotFound,
	} {
		if w := get(phone, path); w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
	}
	// Only the user's own devices: a limited one draws no such rows.
	if w := get(deviceToken("watch", roleAnswer), pic); w.Code != http.StatusForbidden {
		t.Fatalf("a limited device: %d", w.Code)
	}
	// Kept under the source's own directory, which goes with the source's data.
	dir := filepath.Join(h.cfg.dir, "cache", "avatars", "example")
	if files, _ := os.ReadDir(dir); len(files) == 0 {
		t.Fatalf("nothing kept in %s", dir)
	}
	h.avatars.Drop("example")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dropped source's pictures stay: %v", err)
	}
}

// b64 is the path segment a value would have, for values AvatarURL refuses to make one of.
func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
