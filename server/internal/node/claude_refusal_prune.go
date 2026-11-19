package node

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// A refused turn poisons everything Claude Code resumes after it. The CLI rebuilds the messages of a resume from the
// newest message chain in the transcript and ignores last-prompt.leafUuid (measured on 2.1.322): the text of the
// refused prompt is merged into every later prompt, so the next turn is refused again. Truncating the trailing refused
// turn(s) back to the end of the last clean turn makes the CLI recover, and leaves every earlier turn byte-for-byte
// intact.
//
// Only the file's tail is cut, never a chain pointer: the newest chain still ends on the last clean turn's final line
// (last-prompt, cost-state, mode, turn_duration, attachment or assistant), which is what a resume reads.

// refusalBackupSuffix is the backup's suffix. It must not end in .jsonl: the projects scan (globProjects("*.jsonl"))
// and claude --resume both take any file named <id>.jsonl as a session.
const refusalBackupSuffix = ".refusal.bak"

// refusalPruneTurn is one prompt turn of a transcript: where it starts and what its last assistant entry says. A turn is a
// real prompt line (roundLine.prompt) and the lines after it, up to the next prompt.
type refusalPruneTurn struct {
	startOffset   int64 // the turn's first line, its queue-operation prefix included
	sawAssistant  bool
	lastSynthetic bool
	lastRefusal   bool
	noFallback    bool // a model_refusal_no_fallback line is in the turn
	fallback      bool // a model_refusal_fallback line is in the turn
}

// terminalRefusal: the turn ends on a synthetic refusal reply.
func (t refusalPruneTurn) terminalRefusal() bool {
	return t.sawAssistant && t.lastSynthetic && t.lastRefusal
}

// refusalLine is the part of a transcript line this pruning reads.
type refusalLine struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	Sidechain      bool   `json:"isSidechain"`
	Meta           bool   `json:"isMeta"`
	Virtual        bool   `json:"isVirtual"`
	CompactSummary bool   `json:"isCompactSummary"`
	// Error is raw: an older line's error is a code string, a newer one's may be an object, and neither may cost the
	// whole line's other fields.
	Error   json.RawMessage `json:"error"`
	Message *struct {
		Model       string `json:"model"`
		StopReason  string `json:"stop_reason"`
		StopDetails *struct {
			Type string `json:"type"`
		} `json:"stop_details"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// prompt says the line is a message someone wrote to the conversation (the same rule as roundLine.prompt).
func (l refusalLine) prompt() bool {
	if l.Type != "user" || l.Sidechain || l.Meta || l.Virtual || l.CompactSummary || l.Message == nil {
		return false
	}
	c := bytes.TrimSpace(l.Message.Content)
	if len(c) > 0 && c[0] == '"' {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(c, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false
		}
	}
	return len(blocks) > 0
}

// refusalText: the text of a message's text blocks, for the safeguard refusal's words.
func refusalText(content json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// refusalTailPeekBytes is the tail window checked before scanning the whole transcript from byte 0. A terminal refusal
// writes a <synthetic> assistant entry followed by only short metadata rows (last-prompt, mode, cost-state), so when
// the final 512 KiB holds an assistant entry and no "<synthetic>" marker, the transcript cannot end on a synthetic
// refusal.
const refusalTailPeekBytes = 512 << 10

// fileMayEndInSyntheticRefusal reports whether f could end on a synthetic refusal turn without scanning earlier megabytes.
func fileMayEndInSyntheticRefusal(f *os.File) (bool, error) {
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := st.Size()
	if size == 0 {
		return false, nil
	}
	readLen := size
	start := int64(0)
	if size > refusalTailPeekBytes {
		readLen = refusalTailPeekBytes
		start = size - refusalTailPeekBytes
	}
	buf := make([]byte, readLen)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	buf = buf[:n]
	if bytes.Contains(buf, []byte(`"<synthetic>"`)) || bytes.Contains(buf, []byte(`"model_refusal_no_fallback"`)) {
		return true, nil
	}
	// If we read the entire file, or saw at least one assistant record in the tail window without "<synthetic>", the
	// final assistant entry is guaranteed not to be synthetic.
	if start == 0 || bytes.Contains(buf, []byte(`"type":"assistant"`)) || bytes.Contains(buf, []byte(`"type": "assistant"`)) {
		return false, nil
	}
	return true, nil
}

// hasTrailingClaudeRefusalFile checks (read-only) whether path currently ends on a terminal synthetic refusal turn.
func hasTrailingClaudeRefusalFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	may, err := fileMayEndInSyntheticRefusal(f)
	if err != nil || !may {
		return false
	}
	turns, err := refusalTurns(f)
	if err != nil || len(turns) == 0 {
		return false
	}
	return turns[len(turns)-1].terminalRefusal()
}

// transcriptHasTrailingClaudeRefusal reports whether conversation id's transcript ends on a terminal synthetic refusal.
func (n *Node) transcriptHasTrailingClaudeRefusal(id string) bool {
	if id == "" {
		return false
	}
	path := n.transcriptPath(id)
	if path == "" {
		return false
	}
	return hasTrailingClaudeRefusalFile(path)
}

// pruneClaudeRefusalFile cuts the trailing refused turn(s) of transcript path back to the end of its last clean turn.
// pruned says whether the file changed; hasCleanTurns says whether turns before the refused one remain, and is false
// when the very first turn was refused and the file was removed. The full original is copied to path+".refusal.bak"
// before anything is changed, so a removal is always recoverable.
func pruneClaudeRefusalFile(path string) (pruned bool, hasCleanTurns bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, false, err
	}
	defer f.Close()

	may, err := fileMayEndInSyntheticRefusal(f)
	if err != nil {
		return false, true, err
	}
	if !may {
		return false, true, nil
	}

	turns, err := refusalTurns(f)
	if err != nil {
		return false, len(turns) > 0, err
	}
	if len(turns) == 0 || !turns[len(turns)-1].terminalRefusal() {
		return false, len(turns) > 0, nil
	}
	firstRefused := len(turns) - 1
	for firstRefused > 0 && turns[firstRefused-1].terminalRefusal() {
		firstRefused-- // a session that already accumulated several refused turns heals to its last clean one
	}
	if err := backupRefusalFile(f, path); err != nil {
		return false, true, err
	}
	if firstRefused > 0 {
		if err := f.Truncate(turns[firstRefused].startOffset); err != nil {
			return false, true, err
		}
		if err := f.Sync(); err != nil {
			return false, true, err
		}
		return true, true, nil
	}
	// The very first turn was refused: no clean turn follows it, so there is nothing to resume. The backup is the
	// session's only copy of the file.
	if err := f.Close(); err != nil {
		return false, false, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	return true, false, nil
}

// refusalTurns reads f into its prompt turns, with each turn's start offset.
func refusalTurns(f *os.File) ([]refusalPruneTurn, error) {
	var turns []refusalPruneTurn
	var offset int64
	queueStart := int64(-1) // the offset of a queue-operation run right before a prompt, -1 when there is none
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		start, end := offset, offset+int64(len(line))
		offset = end
		if len(bytes.TrimSpace(line)) > 0 {
			var l refusalLine
			if json.Unmarshal(line, &l) == nil {
				switch {
				case l.prompt():
					begin := start
					if queueStart >= 0 {
						begin = queueStart
					}
					turns = append(turns, refusalPruneTurn{startOffset: begin})
					queueStart = -1
				case l.Type == "queue-operation":
					if queueStart < 0 {
						queueStart = start
					}
				default:
					queueStart = -1
				}
				if !l.Sidechain && len(turns) > 0 {
					t := &turns[len(turns)-1]
					switch {
					case l.Type == "system" && l.Subtype == "model_refusal_no_fallback":
						t.noFallback = true
						t.lastRefusal = false
					case l.Type == "system" && l.Subtype == "model_refusal_fallback":
						t.fallback = true
					case l.Type == "assistant" && l.Message != nil:
						t.sawAssistant = true
						t.lastSynthetic = l.Message.Model == "<synthetic>"
						t.lastRefusal = t.lastSynthetic && refusalReply(l, t)
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return turns, err
		}
	}
	return turns, nil
}

// refusalReply says whether an assistant entry is a refusal: a stop reason or stop details that say refusal, the
// safeguard refusal's words, or the turn having been translated to a model with no fallback and not ending in a 404.
// A turn switched to a fallback model is the fallback's own unless it says refusal, and its 404 (the fallback model
// the gateway does not serve) is no refusal the CLI can resume past. "Prompt is too long" is no refusal either.
func refusalReply(l refusalLine, t *refusalPruneTurn) bool {
	if t.fallback && l.errorIs("model_not_found") {
		return false
	}
	if l.Message.StopReason == "refusal" || l.Message.StopDetails != nil && l.Message.StopDetails.Type == "refusal" {
		return true
	}
	if assistantSafeguardsRefusal(refusalText(l.Message.Content)) {
		return true
	}
	return t.noFallback && !l.errorIs("model_not_found")
}

// errorIs: the entry's error names code.
func (l refusalLine) errorIs(code string) bool { return strings.Trim(string(l.Error), `"`) == code }

// backupRefusalFile copies all of f to path+".refusal.bak" with mode 0600.
func backupRefusalFile(f *os.File, path string) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	backup, err := os.OpenFile(path+refusalBackupSuffix, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(backup, f); err != nil {
		backup.Close()
		return err
	}
	if err := backup.Close(); err != nil {
		return err
	}
	return os.Chmod(path+refusalBackupSuffix, 0o600) // an older backup must not keep its own mode
}

// healClaudeRefusalTranscript prunes a trailing refused turn from conversation id's transcript before it is resumed.
// A process already warm holds the refused turn in its own context, so the caller stops it; this only cuts the file.
func (n *Node) healClaudeRefusalTranscript(id string) (pruned bool, hasCleanTurns bool) {
	if id == "" {
		return false, true
	}
	path := n.transcriptPath(id)
	if path == "" {
		return false, true
	}
	if _, _, ok := n.claudeOpenElsewhere(id); ok {
		return false, true // the CLI that has it open writes the same file
	}
	pruned, hasCleanTurns, err := pruneClaudeRefusalFile(path)
	if err != nil {
		n.logf("claude %s: could not prune a refused turn from the transcript: %v", short(id), err)
		return false, hasCleanTurns
	}
	if !pruned {
		return false, hasCleanTurns
	}
	n.mu.Lock()
	delete(n.metas, path)
	if !hasCleanTurns { // the file is gone: no path to resume or list
		delete(n.paths, id)
		delete(n.pathsAt, id)
	}
	n.mu.Unlock()
	forgetOfflineScan()
	n.logf("claude %s: pruned trailing synthetic refusal turn from transcript before resume", short(id))
	return true, hasCleanTurns
}
