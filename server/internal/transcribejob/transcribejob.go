// Package transcribejob is one job of a transcription contribution (docs/assistant/meeting-minutes.md "The
// `transcription` extension point"): the socket the manager talks to it over, for a meeting's live job and final
// pass (the recording module) and for an extension's own audio (transcription for extensions, in the core). The
// manager writes the live log and the hub's transcript; the extension stores nothing. It holds no state.
package transcribejob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
)

// WriteWait bounds one frame written to a job.
const WriteWait = 10 * time.Second

// ErrNoTranscribers: this process reaches no transcription contribution (no extensions layer).
var ErrNoTranscribers = errors.New("no transcription contribution in this process")

// Conn is one job socket.
type Conn struct {
	c    *websocket.Conn
	done func()
}

// New is the job on socket c; done ends the extension's open request (nil: nothing to end).
func New(c *websocket.Conn, done func()) *Conn {
	if done == nil {
		done = func() {}
	}
	return &Conn{c: c, done: done}
}

// Send writes text frame f.
func (j *Conn) Send(ctx context.Context, f extwire.TranscriptionFrame) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, WriteWait)
	defer cancel()
	return j.c.Write(ctx, websocket.MessageText, raw)
}

// SendAudio writes a binary frame of PCM16 audio.
func (j *Conn) SendAudio(ctx context.Context, pcm []byte) error {
	ctx, cancel := context.WithTimeout(ctx, WriteWait)
	defer cancel()
	return j.c.Write(ctx, websocket.MessageBinary, pcm)
}

// Read is the job's next text frame (binary frames from it are skipped; a text frame that is not JSON is an error).
func (j *Conn) Read(ctx context.Context) (extwire.TranscriptionFrame, error) {
	for {
		typ, raw, err := j.c.Read(ctx)
		if err != nil {
			return extwire.TranscriptionFrame{}, err
		}
		if typ != websocket.MessageText {
			continue
		}
		var f extwire.TranscriptionFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			return f, fmt.Errorf("a frame that is not JSON: %w", err)
		}
		return f, nil
	}
}

// AwaitReady reads until the job's ready; a fatal error before it is an *Error.
func (j *Conn) AwaitReady(ctx context.Context) (extwire.TranscriptionFrame, error) {
	for {
		f, err := j.Read(ctx)
		if err != nil {
			return f, err
		}
		switch f.Type {
		case extwire.TFReady:
			return f, nil
		case extwire.TFError:
			if f.Fatal {
				return f, &Error{Code: f.Code}
			}
		case extwire.TFDone:
			return f, &Error{Code: extwire.TEUpstreamFailed}
		}
	}
}

// Close ends the job socket and the extension's open request.
func (j *Conn) Close() {
	_ = j.c.Close(websocket.StatusNormalClosure, "")
	j.done()
}

// Error is a job's fatal error.
type Error struct{ Code string }

func (e *Error) Error() string { return "transcription job: " + e.Code }
