package extmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

func connectorTestPNG() []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		panic(err)
	}
	return b.Bytes()
}

type mediaContentConnector struct {
	*threadsConnector
	media connector.Media
}

func (c *mediaContentConnector) Media(context.Context, string) (connector.Media, error) {
	c.fetched.Add(1)
	return c.media, nil
}

func mediaContentHub(t *testing.T, media connector.Media) (*Manager, *mediaContentConnector, http.Handler) {
	t.Helper()
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(nil)
	c := &mediaContentConnector{threadsConnector: newThreadsConnector(), media: media}
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	return h, c, h.routes()
}

func requestConnectorMedia(mux http.Handler, token, ref string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/example/media?ref="+ref, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func checkConnectorMediaHeaders(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Errorf("missing media protections: %v", w.Header())
	}
}

func TestConnectorMediaRejectsActiveContent(t *testing.T) {
	for _, tc := range []struct {
		name, mime, data string
	}{
		{"html", "text/html", "<!DOCTYPE html><script>alert(1)</script>"},
		{"svg", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`},
		{"html_claiming_png", "image/png", "<!DOCTYPE html><script>alert(1)</script>"},
		{"svg_claiming_png", "image/png", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`},
		{"html_without_mime", "", "<!DOCTYPE html><script>alert(1)</script>"},
		{"svg_without_mime", "", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`},
	} {
		for _, cached := range []bool{false, true} {
			name := tc.name + "/fresh"
			if cached {
				name = tc.name + "/cached"
			}
			t.Run(name, func(t *testing.T) {
				m := connector.Media{Mime: tc.mime, Data: []byte(tc.data)}
				h, c, mux := mediaContentHub(t, m)
				const ref = "unsafe"
				if cached {
					// A file written by the old hub must pass the same byte check as a fresh fetch.
					if err := h.media.put(h.mediaDir(), c.ID(), ref, m); err != nil {
						t.Fatal(err)
					}
					c.state.Store(connector.StateNotSignedIn)
				}
				for range 2 {
					w := requestConnectorMedia(mux, h.cfg.Token, ref)
					checkConnectorMediaHeaders(t, w)
					if w.Code != http.StatusUnsupportedMediaType {
						t.Errorf("untrusted media returned %d, Content-Type %q, body %q; want 415", w.Code, w.Header().Get("Content-Type"), w.Body.String())
					}
					if strings.Contains(w.Header().Get("Cache-Control"), "immutable") || w.Body.String() == tc.data {
						t.Errorf("rejected media exposed or cached: %q %v", w.Body.String(), w.Header())
					}
				}
				if cached {
					if c.fetched.Load() != 0 {
						t.Fatal("cached rejection contacted the connector")
					}
				} else {
					if c.fetched.Load() != 2 {
						t.Errorf("failed response was reused: %d fetches", c.fetched.Load())
					}
					if _, err := os.Stat(mediaFile(h.mediaDir(), c.ID(), ref)); !os.IsNotExist(err) {
						t.Errorf("rejected bytes were stored: %v", err)
					}
				}
			})
		}
	}
}

// Preserve media bytes, canonical types and authenticated disk hits even when the connector's declared MIME lies.
// Container fixtures exercise recognition and transport, not full decoding or native playback.
func TestConnectorMediaPassiveTypes(t *testing.T) {
	var jpg, animation bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&animation, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mime string
		data       []byte
	}{
		{"png", "image/png", connectorTestPNG()},
		{"jpeg", "image/jpeg", jpg.Bytes()},
		{"gif", "image/gif", animation.Bytes()},
		{"webp", "image/webp", webp},
		{"mp3", "audio/mpeg", []byte("ID3\x03\x00\x00\x00\x00\x0f")},
		{"wav", "audio/wave", []byte("RIFF,\x00\x00\x00WAVEfmt \x12\x00\x00\x00\x06")},
		{"aiff", "audio/aiff", []byte("FORM\x00\x00\x00\x00AIFFCOMM")},
		{"midi", "audio/midi", []byte("MThd\x00\x00\x00\x06\x00\x01")},
		{"ogg", "application/ogg", []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00")},
		{"mp4", "video/mp4", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")},
		{"webm", "video/webm", []byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01")},
		{"avi", "video/avi", []byte("RIFF,\x00\x00\x00AVI LIST")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, mux := mediaContentHub(t, connector.Media{Mime: "text/html", Data: tc.data})
			const ref = "passive"
			check := func(w *httptest.ResponseRecorder) {
				t.Helper()
				checkConnectorMediaHeaders(t, w)
				if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), tc.data) || w.Header().Get("Content-Type") != tc.mime ||
					w.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
					t.Fatalf("media changed: status %d, headers %v, bytes %x", w.Code, w.Header(), w.Body.Bytes())
				}
			}
			if w := requestConnectorMedia(mux, "", ref); w.Code != http.StatusUnauthorized || c.fetched.Load() != 0 {
				t.Fatalf("unauthenticated fetch: %d, %d fetches", w.Code, c.fetched.Load())
			}
			check(requestConnectorMedia(mux, h.cfg.Token, ref))
			stored, ok := h.media.get(h.mediaDir(), c.ID(), ref)
			if !ok || stored.Mime != tc.mime || !bytes.Equal(stored.Data, tc.data) {
				t.Fatal("cache did not preserve canonical type and bytes")
			}
			// Simulate an old cache header claiming HTML over otherwise valid media bytes.
			if err := h.media.put(h.mediaDir(), c.ID(), ref, connector.Media{Mime: "text/html", Data: tc.data}); err != nil {
				t.Fatal(err)
			}
			c.state.Store(connector.StateNotSignedIn)
			if w := requestConnectorMedia(mux, "", ref); w.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated cache hit: %d", w.Code)
			}
			check(requestConnectorMedia(mux, h.cfg.Token, ref))
			if c.fetched.Load() != 1 {
				t.Fatalf("cache refetched %d times", c.fetched.Load())
			}
		})
	}
}

func TestConnectorMediaErrors(t *testing.T) {
	for _, tc := range []struct {
		name, state, ref string
		data             []byte
		code             int
	}{
		{"missing_ref", connector.StateOK, "", connectorTestPNG(), http.StatusBadRequest},
		{"signed_out", connector.StateNotSignedIn, "error", connectorTestPNG(), http.StatusConflict},
		{"rate_limited", connector.StateRateLimited, "error", connectorTestPNG(), http.StatusTooManyRequests},
		{"empty", connector.StateOK, "error", nil, http.StatusBadGateway},
		{"too_large", connector.StateOK, "error", bytes.Repeat([]byte{0}, connector.MediaMost+1), http.StatusBadGateway},
		{"text", connector.StateOK, "error", []byte("just text"), http.StatusUnsupportedMediaType},
		{"xml", connector.StateOK, "error", []byte("<?xml version=\"1.0\"?><example/>"), http.StatusUnsupportedMediaType},
		{"pdf", connector.StateOK, "error", []byte("%PDF-1.7"), http.StatusUnsupportedMediaType},
		{"zip", connector.StateOK, "error", []byte("PK\x03\x04\x00\x00"), http.StatusUnsupportedMediaType},
		{"binary", connector.StateOK, "error", []byte{0xff, 0, 0xff, 0, 0}, http.StatusUnsupportedMediaType},
		{"bmp", connector.StateOK, "error", []byte("BMbitmap"), http.StatusUnsupportedMediaType},
		{"ico", connector.StateOK, "error", []byte{0, 0, 1, 0, 1, 0}, http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, mux := mediaContentHub(t, connector.Media{Mime: "image/png", Data: tc.data})
			c.state.Store(tc.state)
			w := requestConnectorMedia(mux, h.cfg.Token, tc.ref)
			checkConnectorMediaHeaders(t, w)
			if w.Code != tc.code || strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
				t.Fatalf("error response: %d %v", w.Code, w.Header())
			}
			if _, ok := h.media.get(h.mediaDir(), c.ID(), tc.ref); ok {
				t.Fatal("error response was cached")
			}
		})
	}
	// The handler's own missing-connector failure also has the same protections.
	h, _, mux := mediaContentHub(t, connector.Media{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/missing/media?ref=missing", nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	checkConnectorMediaHeaders(t, w)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing connector: %d", w.Code)
	}
}

func TestConnectorMediaRequestHeadersDoNotBypassValidation(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, header := range []string{"Range", "If-None-Match", "If-Modified-Since"} {
			name := header + "/fresh"
			if cached {
				name = header + "/cached"
			}
			t.Run(name, func(t *testing.T) {
				m := connector.Media{Mime: "image/png", Data: []byte("<html><script>alert(1)</script></html>")}
				h, c, mux := mediaContentHub(t, m)
				if cached {
					if err := h.media.put(h.mediaDir(), c.ID(), "unsafe", m); err != nil {
						t.Fatal(err)
					}
				}
				req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/example/media?ref=unsafe", nil)
				req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
				value := "bytes=0-10"
				if header == "If-None-Match" {
					value = "*"
				} else if header == "If-Modified-Since" {
					value = "Wed, 31 Dec 2099 00:00:00 GMT"
				}
				req.Header.Set(header, value)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				checkConnectorMediaHeaders(t, w)
				if w.Code != http.StatusUnsupportedMediaType || w.Header().Get("Content-Range") != "" ||
					strings.Contains(w.Header().Get("Cache-Control"), "immutable") || bytes.Equal(w.Body.Bytes(), m.Data) {
					t.Fatalf("request header bypassed rejection: %d %v %q", w.Code, w.Header(), w.Body.String())
				}
			})
		}
	}
}
