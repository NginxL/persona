package bus

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/coder/websocket"
)

// A body stream carries one request body from the hub to a manager on a relay of its own, beside the bus
// (assistantwire.MethodHTTPStreamed, #6574): binary messages of at most BodyChunk with exactly the body's bytes, then a
// normal close. The bus then carries no message of the body's size, and other calls are not held up behind it.

// BodyChunk is the largest binary message of a body stream.
const BodyChunk = 256 << 10

// WriteBody writes body to c as a body stream and closes c. The reader sends nothing but its close; a reader that
// closes early ends the write.
func WriteBody(ctx context.Context, c *websocket.Conn, body []byte) error {
	ctx = c.CloseRead(ctx)
	for off := 0; off < len(body); off += BodyChunk {
		if err := c.Write(ctx, websocket.MessageBinary, body[off:min(off+BodyChunk, len(body))]); err != nil {
			_ = c.CloseNow()
			return err
		}
	}
	return c.Close(websocket.StatusNormalClosure, "")
}

// ReadBody reads a body stream of exactly size bytes from c into one buffer, and closes c. Fewer or more bytes, a
// text message, a message over BodyChunk or any other end than a normal close is an error.
func ReadBody(ctx context.Context, c *websocket.Conn, size int64) ([]byte, error) {
	defer func() { _ = c.CloseNow() }()
	if size < 0 || size > MaxMessage {
		return nil, fmt.Errorf("body stream: size %d out of range", size)
	}
	c.SetReadLimit(BodyChunk)
	body := make([]byte, size)
	n := 0
	var one [1]byte
	for {
		typ, r, err := c.Reader(ctx)
		if err != nil {
			if n == len(body) && websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return body, nil
			}
			return nil, fmt.Errorf("body stream: ended after %d of %d bytes: %w", n, len(body), err)
		}
		if typ != websocket.MessageBinary {
			return nil, errors.New("body stream: a text message")
		}
		k, err := io.ReadFull(r, body[n:])
		n += k
		switch {
		case err == nil: // the body is full: the message must end here
			if m, _ := io.ReadFull(r, one[:]); m > 0 {
				return nil, fmt.Errorf("body stream: more than %d bytes", len(body))
			}
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF): // the message ended first
		default:
			return nil, fmt.Errorf("body stream: after %d of %d bytes: %w", n, len(body), err)
		}
	}
}
