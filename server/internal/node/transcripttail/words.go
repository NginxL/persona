// Package transcripttail recovers an agent's last words when they fall outside a transcript's quick tail.
package transcripttail

import (
	"bytes"
	"os"

	"github.com/nwverse/persona/server/internal/transcript"
)

const (
	// Window is how much of a transcript's end the quick reader parses. One line can fill it.
	Window = 384 * 1024
	// Reach is how far before the tail LastWordsBefore looks for an agent's last words.
	Reach = 8 << 20
)

// LastWordsBefore returns the last assistant text in the transcript before end, read in expanding suffixes up to
// Reach, and whether their latest turn's close confirms success; "" when there is no text in that reach.
func LastWordsBefore(f *os.File, end int64) (string, bool) {
	var tm transcript.Meta
	for chunk := int64(Window); end > 0; chunk = min(chunk*2, Reach) {
		start := max(end-chunk, 0)
		buf := make([]byte, end-start)
		if n, err := f.ReadAt(buf, start); err != nil && n < len(buf) {
			return "", false
		}
		lines := bytes.Split(buf, []byte("\n"))
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // a partial line: whole in the next, larger window
		}
		tm = transcript.Meta{}
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			if bytes.Contains(line, []byte(`"type":"user"`)) && len(line) > 8<<10 && !notAPrompt(line) {
				if !bytes.Contains(line, []byte(`"tool_result"`)) && !tm.Codex {
					tm.TurnOpen, tm.TurnClosed = true, false
					tm.ClaudeTurnStarted(bytes.Contains(line, []byte(`<task-notification>`)))
				}
				continue
			}
			transcript.ParseLine(0, line, &tm)
		}
		if tm.LastText != "" {
			return tm.LastText, tm.TurnSucceeded()
		}
		if start == 0 || chunk == Reach {
			break
		}
		// Read the newer lines again in the larger window: a prompt after a close, or a close after its reply, changes
		// whether those words are the current turn's outcome rather than an older error preview.
	}
	return "", tm.TurnSucceeded()
}

// notAPrompt: a user line that is not a message to the agent whatever its size: a compaction's summary or a meta line.
func notAPrompt(line []byte) bool {
	return bytes.Contains(line, []byte(`"isCompactSummary":true`)) || bytes.Contains(line, []byte(`"isMeta":true`))
}
