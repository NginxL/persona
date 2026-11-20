package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/screen"
)

// screenCheck is screen.CheckClaude's refusal code for typing into the box now ("" when it may).
func screenCheck(ansi string) (string, screen.Input) { return screen.CheckClaude(ansi) }

// screenDraft: the input box holds something (a paste placeholder counts).
func screenDraft(ansi string) bool { return screen.ClaudeInput(ansi).Draft }

// saveTUIImage writes a message's base64 image into dir, private to the bridge, and returns its path.
func saveTUIImage(c map[string]any, dir string, n, i int) (string, error) {
	src, _ := c["source"].(map[string]any)
	data, _ := src["data"].(string)
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("image: %w", err)
	}
	ext := ".png"
	switch src["media_type"] {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	path := filepath.Join(dir, fmt.Sprintf("image-%d-%d%s", n, i, ext))
	return path, os.WriteFile(path, raw, 0o600)
}

// tuiTranscript finds session's transcript in the profiles it may be in: the newest when there are several.
func tuiTranscript(dirs []string, session string) string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude"))
	}
	best, bestAt := "", time.Time{}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(d, "projects", "*", session+".jsonl"))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.ModTime().After(bestAt) {
				best, bestAt = m, st.ModTime()
			}
		}
	}
	return best
}

// openTranscript notes where a resumed conversation's new lines start: what is in its transcript already was said
// before this process.
func (b *tuiBridge) openTranscript() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rowAt = time.Now()
	if !b.spec.Resume {
		return nil
	}
	if b.path = tuiTranscript(b.spec.ConfigDirs, b.spec.Session); b.path != "" {
		if st, err := os.Stat(b.path); err == nil {
			b.offset = st.Size()
		}
	}
	return nil
}

// readTranscript translates what the transcript gained since the last read.
func (b *tuiBridge) readTranscript() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.path == "" {
		if b.path = tuiTranscript(b.spec.ConfigDirs, b.spec.Session); b.path == "" {
			return
		}
		if b.spec.Resume { // not there at start: only what comes from now on
			if st, err := os.Stat(b.path); err == nil {
				b.offset = st.Size()
			}
			return
		}
	}
	f, err := os.Open(b.path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(b.offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return // a line still being written
	}
	b.offset += int64(end + 1)
	b.rowAt = time.Now()
	for _, line := range bytes.Split(data[:end], []byte("\n")) {
		for _, ev := range b.tr.row(line) {
			b.out.emit(ev)
		}
	}
}

// tuiSpinnerRe is the line Claude Code shows while a turn runs ("· Zesting…", "✻ Zesting… (3s · ↓ 120 tokens)"), not
// the one after it ("✻ Brewed for 4s · done").
var tuiSpinnerRe = lazyre.New(`(?m)^\s*[·✢✳✶✻✽*]\s+\S[^\n]*…`)

// tuiTasksRe counts what runs in the background, on the mode line ("⏵⏵ bypass permissions on · 1 shell").
var tuiTasksRe = lazyre.New(`·\s+(\d+)\s+(?:shells?|monitors?|background tasks?)\b`)

// tuiTaskRowRe is one of them listed above the mode line ("◐ Bash: sleep 40").
var tuiTaskRowRe = lazyre.New(`(?m)^\s*[◐◓◑◒]\s+(.+?)\s*$`)

// tuiIdleQuiet is how long a turn's transcript must be quiet, its screen showing no turn running, before the bridge
// ends the turn without a line that says so (a local command such as /compact writes none).
var tuiIdleQuiet = 15 * time.Second

// backgroundTasks reads the running background work off the screen, nil when the screen shows no mode line.
func backgroundTasks(plain string) []string {
	count := 0
	for _, m := range tuiTasksRe.FindAllStringSubmatch(plain, -1) {
		n, _ := strconv.Atoi(m[1])
		count += n
	}
	rows := tuiTaskRowRe.FindAllStringSubmatch(plain, -1)
	tasks := make([]string, 0, count)
	for i := range count {
		desc := "background task"
		if i < len(rows) {
			desc = rows[i][1]
		}
		tasks = append(tasks, desc)
	}
	return tasks
}

// watch follows the transcript and the screen until Claude Code exits; it returns its exit status.
func (b *tuiBridge) watch(ctx context.Context) int {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return 0
		case <-tick.C:
		}
		b.readTranscript()
		if i%4 != 0 {
			continue
		}
		plain, _, err := b.screens(ctx)
		if dead, status := b.dead(ctx); dead || err != nil {
			b.readTranscript()
			fmt.Fprintf(b.stderr, "assistant-tui: %s\n", b.ended(ctx, plain, status, false))
			return status
		}
		b.screenState(plain)
	}
}

// screenState reports background work as it changes, and ends a turn the screen shows over that no line ended.
func (b *tuiBridge) screenState(plain string) {
	tasks := backgroundTasks(plain)
	key := strings.Join(tasks, "\x00")
	b.mu.Lock()
	defer b.mu.Unlock()
	if key != b.tasks || b.tasks == "" && len(tasks) == 0 && !b.tasksSent {
		b.tasks, b.tasksSent = key, true
		list := make([]map[string]any, len(tasks))
		for i, t := range tasks {
			list[i] = map[string]any{"description": t}
		}
		b.out.emit(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": list})
	}
	if b.tr.busy && time.Since(b.rowAt) > tuiIdleQuiet && inputBoxVisible(plain) && !screen.Choosing(plain) &&
		!tuiSpinnerRe.MatchString(plain) {
		for _, ev := range b.tr.idle() {
			b.out.emit(ev)
		}
	}
}
