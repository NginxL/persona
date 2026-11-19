package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/transcript"
)

// A gateway's impossible usage (transcript/usage.go, #3300): Claude Code sizes the context by the last reply's usage,
// so one reply that reports a whole turn's input added up makes it compact for nothing on the next message, or — when
// that compaction fails too — refuse every message with "Prompt is too long" for good. Tested on Claude Code 2.1.318
// with a copy of such a transcript and a fake API:
//
//   - as it was, a resume asked for a compaction first (pre_tokens 1.7M), and with an empty summary ended the turn
//     with "Prompt is too long · automatic compaction failed: no assistant message in summarization response";
//   - with that reply's usage set to zero, the same resume sent the message straight on, no compaction: a usage of 0
//     is no estimate, and it counts the messages itself. The failed messages stay in the transcript, their synthetic
//     error replies are left out of the request, and the next message is sent along with them in one user turn.
//
// So at the end of every claude turn the transcript is checked: an impossible usage is set to zero in place (same
// length, the file is not rewritten: readers keep their offsets), with a copy of the file kept next to it first; the
// process, which holds the number in memory, is let go so the next message resumes from the repaired file; and a turn
// that ended in "Prompt is too long" because of it is sent again, once.

// usageRetryEvery: a conversation's message is sent again after a repair at most this often (a gateway that keeps
// reporting nonsense, or a conversation that really is too long, does not loop).
const usageRetryEvery = 10 * time.Minute

// usageResend replaces sendAssistantUsageRetry for sending the message again (tests); nil: sendAssistantUsageRetry.
var usageResend func(ctx context.Context, n *Node, id, cwd, launcher, text string, execution modelExecution) error

func sendAssistantUsageRetry(ctx context.Context, n *Node, id, cwd, launcher, text string, execution modelExecution) error {
	execution.OriginDecisionRequired = true
	_, err := n.assistantSend(ctx, id, cwd, launcher, text, nil, "", "", execution)
	return err
}

// usageKeyRe is a usage count in a line ("input_tokens": 1706464): a key not inside a string (a quote inside a JSON
// string is escaped), and its digits (group 1).
var usageKeyRe = lazyre.New(`(?:^|[^\\])"(?:input_tokens|cache_read_input_tokens|cache_creation_input_tokens)"\s*:\s*(\d+)`)

// usageHintRe: a line that may carry an impossible usage, a count of six digits or more; only those are decoded.
var usageHintRe = lazyre.New(`"(?:input_tokens|cache_read_input_tokens|cache_creation_input_tokens)"\s*:\s*\d{6}`)

// usageFix is a line of the transcript whose usage is impossible.
type usageFix struct {
	off    int64 // where the line starts
	line   []byte
	tokens int
}

// usageReadSize is how much of the transcript implausibleUsage reads at a time. A process's first check reads a long
// conversation's transcript, tens of MB, from the start; holding all of it grew the node's heap by its size.
const usageReadSize = 256 << 10

// usageHintSpan is more than the longest hint in a transcript's compact JSON (`"cache_creation_input_tokens":` and
// six digits are 36 bytes): in a line longer than the read buffer, a hint split between two reads is found in this
// much of either.
const usageHintSpan = 64

// implausibleUsage finds the conversation's replies (not a subagent's) whose usage cannot be one request's in the
// transcript f of size bytes, from offset from (a line start) on, and where the last complete line ends: a line still
// being written is left for the next check. It reads a buffer at a time and keeps only the lines to fix; a line longer
// than the buffer is read whole only when it has a hint.
func implausibleUsage(f io.ReaderAt, from, size int64) ([]usageFix, int64, error) {
	// A turn's check reads only what the turn added: no buffer larger than that, nor smaller than a hint's span.
	br := bufio.NewReaderSize(io.NewSectionReader(f, from, size-from), int(min(usageReadSize, max(size-from, 4<<10))))
	var out []usageFix
	for off := from; ; {
		line, err := br.ReadSlice('\n')
		n, hinted := int64(len(line)), usageHintRe.Match(line)
		if err == bufio.ErrBufferFull {
			n, hinted, err = skimLine(br, line)
			if err == nil && hinted {
				line = make([]byte, n)
				if _, err := f.ReadAt(line, off); err != nil {
					return nil, from, err
				}
			}
		}
		switch {
		case err == io.EOF && off+n < size:
			return nil, from, io.ErrUnexpectedEOF // cut short while it was read
		case err == io.EOF:
			return out, off, nil // the end, or a line still being written
		case err != nil:
			return nil, from, err
		}
		if hinted {
			if fx, ok := implausibleLine(bytes.TrimSuffix(line, []byte("\n")), off, size); ok {
				out = append(out, fx)
			}
		}
		off += n
	}
}

// skimLine reads on to the end of a line longer than the read buffer, first being what was read of it, holding no more
// than a buffer of it: the line's length, whether a usage hint is in it, and the last read's error (nil: the line is
// complete).
func skimLine(br *bufio.Reader, first []byte) (int64, bool, error) {
	var edge [2 * usageHintSpan]byte // the end of one read and the start of the next: a hint split between them
	chunk, err := first, bufio.ErrBufferFull
	n, hinted := int64(len(first)), usageHintRe.Match(first)
	for err == bufio.ErrBufferFull {
		k := copy(edge[:], chunk[len(chunk)-min(len(chunk), usageHintSpan):])
		chunk, err = br.ReadSlice('\n')
		k += copy(edge[k:], chunk)
		n += int64(len(chunk))
		hinted = hinted || usageHintRe.Match(edge[:k]) || usageHintRe.Match(chunk)
	}
	return n, hinted, err
}

// implausibleLine is the fix for line, at off in a transcript of size bytes, when it is a reply of the conversation's
// whose usage cannot be one request's. line is only borrowed: the fix keeps a copy.
func implausibleLine(line []byte, off, size int64) (usageFix, bool) {
	var e struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Message     struct {
			Usage *struct {
				Input       int `json:"input_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.IsSidechain || e.Message.Usage == nil {
		return usageFix{}, false
	}
	u := e.Message.Usage
	tokens := u.Input + u.CacheRead + u.CacheCreate
	if transcript.PlausibleContext(tokens, size) {
		return usageFix{}, false
	}
	return usageFix{off: off, line: bytes.Clone(line), tokens: tokens}, true
}

// zeroUsage is line with every usage count set to 0, padded with spaces to the same length; ok false when the result
// is not valid JSON (then nothing is written).
func zeroUsage(line []byte) ([]byte, bool) {
	out := bytes.Clone(line)
	for _, m := range usageKeyRe.FindAllSubmatchIndex(line, -1) {
		out[m[2]] = '0'
		for i := m[2] + 1; i < m[3]; i++ {
			out[i] = ' '
		}
	}
	return out, json.Valid(out)
}

// usageRepair is what repairUsage did.
type usageRepair struct {
	lines   int    // replies whose usage was set to 0
	largest int    // the largest number replaced
	backup  string // the copy of the file from before
	end     int64  // the file's size it read up to: where the next check starts
}

// repairUsage sets the impossible usage in the transcript at path to zero, looking at the lines from offset from on
// (a line start: the end of an earlier check, 0 for the whole file). It keeps a copy of the file first
// (<path>.usage-backup-<time>) and writes the lines in place: same length, Claude Code only appends.
func repairUsage(path string, from int64) (usageRepair, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return usageRepair{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return usageRepair{}, err
	}
	size := st.Size()
	if from < 0 || from > size {
		from = 0 // replaced or cut short since: all of it
	}
	if from > 0 {
		var b [1]byte
		if _, err := f.ReadAt(b[:], from-1); err != nil || b[0] != '\n' {
			from = 0
		}
	}
	fixes, end, err := implausibleUsage(f, from, size)
	if err != nil {
		return usageRepair{}, err
	}
	r := usageRepair{end: end}
	if len(fixes) == 0 {
		return r, nil
	}
	r.backup = path + ".usage-backup-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := backupCopy(f, r.backup, size); err != nil {
		return usageRepair{end: from}, fmt.Errorf("back up the transcript: %w", err)
	}
	for _, fx := range fixes {
		fixed, ok := zeroUsage(fx.line)
		if !ok {
			continue
		}
		if _, err := f.WriteAt(fixed, fx.off); err != nil {
			return r, err
		}
		r.lines++
		r.largest = max(r.largest, fx.tokens)
	}
	return r, nil
}

// backupCopy writes the first size bytes of src to a new file at path (0600: a transcript is private). A copy that
// failed, on a full disk for one, is removed: it is no backup, and it holds space.
func backupCopy(src *os.File, path string, size int64) error {
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, io.NewSectionReader(src, 0, size))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if rerr := os.Remove(path); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}
	return err
}

// promptTooLong: a turn's result is Claude Code refusing the message as over the context window.
func promptTooLong(result string) bool {
	return strings.Contains(result, "Prompt is too long")
}

// checkTurnUsage runs after a claude turn ended (readAssistant's result): it repairs an impossible usage in the
// transcript, lets the process go (it holds the number) unless background tasks run in it, and sends a message that
// was refused because of it again (tooLong), once per usageRetryEvery.
func (n *Node) checkTurnUsage(p *assistantProc, tooLong bool) {
	path := n.transcriptPath(p.id)
	if path == "" {
		return
	}
	p.mu.Lock()
	from := p.usageChecked
	p.mu.Unlock()
	r, err := repairUsage(path, from)
	p.mu.Lock()
	p.usageChecked = r.end
	p.mu.Unlock()
	if err != nil {
		n.logger.Printf("assistant %s: repair the transcript's usage: %v", p.id[:8], err)
		return
	}
	if r.lines == 0 {
		return
	}
	n.logger.Printf("assistant %s: %d replies reported an impossible usage (up to %d tokens), set to 0 (copy: %s)", p.id[:8], r.lines, r.largest, r.backup)
	p.mu.Lock()
	tasks, busy := len(p.tasks) > 0, p.busy
	launcher, cwd, execution := p.launcher, p.cwd, p.model
	p.mu.Unlock()
	if busy || tasks && !tooLong {
		// A message sent meanwhile is being answered, or its tasks would go with it: it stays, and the file is
		// repaired for its next start.
		return
	}
	n.mu.Lock()
	mine := n.procs[p.id] == p
	retry := tooLong && mine && time.Since(n.usageRetried[p.id]) > usageRetryEvery
	if retry {
		if n.usageRetried == nil {
			n.usageRetried = map[string]time.Time{}
		}
		n.usageRetried[p.id] = time.Now()
	}
	n.mu.Unlock()
	if !mine {
		return // a newer process took over: it read the file after this turn
	}
	n.stopAssistant(p.id)
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
	}
	if !retry {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	send := usageResend
	if send == nil {
		send = sendAssistantUsageRetry
	}
	if err := send(ctx, n, p.id, cwd, launcher, prompts.RenderUsageRetry(), execution); err != nil {
		n.logger.Printf("assistant %s: send the refused message again: %v", p.id[:8], err)
		return
	}
	n.logger.Printf("assistant %s: refused as too long because of it, sent again", p.id[:8])
}
