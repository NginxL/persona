package bus

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// bodyPair runs write on the server's side of a new WebSocket and returns the client's side.
func bodyPair(t *testing.T, write func(ctx context.Context, c *websocket.Conn)) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		write(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func randomBody(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// What WriteBody writes, ReadBody reads back byte for byte, at sizes around the chunk's.
func TestBodyStreamRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, BodyChunk - 1, BodyChunk, BodyChunk + 1, 3*BodyChunk + 7} {
		body := randomBody(t, n)
		written := make(chan error, 1)
		c := bodyPair(t, func(ctx context.Context, c *websocket.Conn) { written <- WriteBody(ctx, c, body) })
		got, err := ReadBody(context.Background(), c, int64(n))
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("%d bytes: read %d different bytes", n, len(got))
		}
		if err := <-written; err != nil {
			t.Fatalf("%d bytes: the writer: %v", n, err)
		}
	}
}

// A stream that is not exactly the body told is an error, never a body.
func TestBodyStreamRefusesAnythingElse(t *testing.T) {
	const size = BodyChunk + 10
	body := randomBody(t, size)
	cases := map[string]func(ctx context.Context, c *websocket.Conn){
		"fewer bytes": func(ctx context.Context, c *websocket.Conn) { _ = WriteBody(ctx, c, body[:size-1]) },
		"more bytes": func(ctx context.Context, c *websocket.Conn) {
			_ = WriteBody(ctx, c, append(append([]byte{}, body...), 0))
		},
		"more bytes in a later message": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, body)
			_ = c.Write(ctx, websocket.MessageBinary, []byte{1})
			_ = c.Close(websocket.StatusNormalClosure, "")
		},
		"a text message": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageText, body)
			_ = c.Close(websocket.StatusNormalClosure, "")
		},
		"a message over the chunk": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, body)
			_ = c.Close(websocket.StatusNormalClosure, "")
		},
		"all of it, then another close": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, body[:BodyChunk])
			_ = c.Write(ctx, websocket.MessageBinary, body[BodyChunk:])
			_ = c.Close(websocket.StatusGoingAway, "not wanted any more")
		},
		"cut off": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, body[:BodyChunk])
			_ = c.CloseNow()
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			c := bodyPair(t, write)
			if got, err := ReadBody(context.Background(), c, size); err == nil {
				t.Fatalf("read %d bytes as the body", len(got))
			}
		})
	}
	if _, err := ReadBody(context.Background(), bodyPair(t, func(context.Context, *websocket.Conn) {}), MaxMessage+1); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Fatalf("a size over the bus's largest message: %v", err)
	}
}

// A reader that goes away ends the writer's write instead of leaving it blocked.
func TestBodyStreamWriterStopsWhenTheReaderCloses(t *testing.T) {
	written := make(chan error, 1)
	c := bodyPair(t, func(ctx context.Context, c *websocket.Conn) {
		written <- WriteBody(ctx, c, make([]byte, 64<<20))
	})
	c.SetReadLimit(BodyChunk)
	if _, _, err := c.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Close(websocket.StatusGoingAway, "not wanted any more")
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("the write went through to a reader that closed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the writer is still writing to a reader that closed")
	}
}
