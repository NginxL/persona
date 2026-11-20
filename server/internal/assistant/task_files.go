package assistant

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Task files (#10511): a task's Agent names the files it made for the user by their absolute paths in its final reply
// (prompts/agents/task_files.md), and the manager copies them from the Agent's computer to the computer of the
// conversation that hears of the task. The assistant then reads them with its own file tools and can give the user
// their paths, instead of the file's content being pasted into a reply that every hop clips: the task notice keeps
// 1500 characters of the reply, the receipt 600, triage 1500, and a paged message is cut at 24 KiB.
//
// The closeout's `artifact: file <path>` is accepted too (closeout.Example). A copy goes through the same node RPCs
// as the attachments in the other direction (attachments.go): file.chunk reads it on the Agent's computer, with the
// node's path guard refusing credentials and iCloud, and file.upload dest=file writes it on the conversation's
// computer, where it is kept (not swept).

// taskFilesMost is how many files one turn hands over; taskFileMostBytes is one file, taskFilesMostBytes all of them.
const (
	taskFilesMost      = 5
	taskFileMostBytes  = proto.MaxFileUploadBytes
	taskFilesMostBytes = 100 << 20
)

// taskFileLine is a hand-over line in a reply: `file: /a/b`, or `文件：/a/b`.
// i18n-ignore: the marker matched in an Agent's reply (prompts/agents/task_files.md), not text anyone reads.
var taskFileLine = lazyre.New(`(?mi)^[ \t]*(?:file|文件)[ \t]*[:：][ \t]*(.+?)[ \t]*$`)

// taskFilePath is p as a file path to copy, or "": absolute or ~/, without the quotes or backticks a model likes to
// wrap it in.
func taskFilePath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "`'\"「」") // i18n-ignore: quote marks trimmed off a path in an Agent's reply
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "/") {
		return p
	}
	return ""
}

// taskFilePaths are the files reply hands over: its `file:` lines, then what its closeout names as `artifact: file`,
// distinct, at most taskFilesMost, in the order it wrote them.
func taskFilePaths(reply string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p = taskFilePath(p); p == "" || seen[p] || len(out) >= taskFilesMost {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, m := range taskFileLine.FindAllStringSubmatch(reply, -1) {
		add(m[1])
	}
	if _, arts, ok := closeout.Parse(reply); ok {
		for _, a := range arts {
			if a.Kind == "file" {
				add(a.Ref)
			}
		}
	}
	return out
}

// taskFile is one file a task handed over: where the Agent had it, and where it is now on the conversation's computer
// (Local "" when there was nothing to copy: the same computer, or a copy that failed, with Error saying why).
type taskFile struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Local string `json:"local,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
}

// local is where the conversation finds the file: its copy, else the original (the same computer, or one that could
// not come).
func (f taskFile) local() string {
	if f.Local != "" {
		return f.Local
	}
	return f.Path
}

// prompt is f as a notice or a supervision message lists it (prompts/assistant/task_notice.md, supervise.md): where the
// conversation finds it, or why it did not come and where it stayed.
func (f taskFile) prompt() prompts.TaskFile {
	if f.Local == "" && f.Error != "" {
		return prompts.TaskFile{Name: f.Name, Path: f.Path, Error: clip(f.Error, 160)}
	}
	return prompts.TaskFile{Name: f.Name, Path: f.local()}
}

// taskFileBook is what each task handed over, by todo id. The copies themselves live on the conversation's computer;
// this is only what a notice, a receipt or a supervision message says about them.
type taskFileBook struct {
	mu    sync.Mutex
	files map[string][]taskFile
}

// taskFilesNow is task id's files, oldest first.
func (s *Service) taskFilesNow(id string) []taskFile {
	b := &s.taskFileBook
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]taskFile(nil), b.files[id]...)
}

// keepTaskFiles merges files into task id's list, replacing an entry with the same path: a copy replaces a failed
// entry, and a failure never replaces a copy.
func (s *Service) keepTaskFiles(id string, files []taskFile) []taskFile {
	if id == "" || len(files) == 0 {
		return s.taskFilesNow(id)
	}
	b := &s.taskFileBook
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files = map[string][]taskFile{}
	}
	cur := b.files[id]
	for _, f := range files {
		replaced := false
		for i := range cur {
			if cur[i].Path != f.Path {
				continue
			}
			if f.Local != "" || cur[i].Local == "" {
				cur[i] = f
			}
			replaced = true
			break
		}
		if !replaced {
			cur = append(cur, f)
		}
	}
	b.files[id] = cur
	return append([]taskFile(nil), cur...)
}

// taskFilePrompts is files as a notice or a supervision message lists them.
func taskFilePrompts(files []taskFile) []prompts.TaskFile {
	out := make([]prompts.TaskFile, 0, len(files))
	for _, f := range files {
		out = append(out, f.prompt())
	}
	return out
}

// taskReceiver is the conversation told of task t's result: its supervisor's, the reporting conversation that opened
// it, the main conversation for a quiet one, else the one that opened it. It is the one whose computer the files are
// copied to.
func (s *Service) taskReceiver(ctx context.Context, t Todo) string {
	if t.Supervision != nil && t.Supervision.Supervisor != "" {
		return t.Supervision.Supervisor
	}
	if _, ok := s.reporterOf(ctx, t.Origin); ok {
		return t.Origin
	}
	if t.Quiet {
		if here := s.settings(ctx); here.Main != "" {
			return here.Main
		}
	}
	return t.Origin
}

// deliverTaskFiles copies the files reply hands over from the Agent's computer (from) to the conversation's (to), and
// answers everything the task handed over so far. It never fails: a file that cannot come keeps its path and says why,
// and the task goes on. A task that names no file keeps what it already had.
func (s *Service) deliverTaskFiles(ctx context.Context, t Todo, from, to, reply string) []taskFile {
	paths := taskFilePaths(reply)
	if len(paths) == 0 {
		return s.taskFilesNow(t.ID)
	}
	return s.keepTaskFiles(t.ID, s.copyTaskFiles(ctx, t, from, to, paths))
}

// copyTaskFiles copies paths (on from, the Agent's computer) to to (the conversation's), skipping the ones the task
// already handed over. Empty from or to, or one computer for both, copies nothing: those files are already where the
// conversation is.
func (s *Service) copyTaskFiles(ctx context.Context, t Todo, from, to string, paths []string) []taskFile {
	prior := s.taskFilesNow(t.ID)
	done := map[string]bool{}
	for _, f := range prior {
		if f.Error == "" { // a failed entry is tried again when a later turn names the file
			done[f.Path] = true
		}
	}
	same := from == "" || to == "" || s.sameMachine(from, to)
	var home string
	if m, ok := s.fleetMachine(from); ok {
		home = m.Home
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	var out []taskFile
	total := int64(0)
	for _, p := range paths {
		if done[p] || len(out) >= taskFilesMost {
			continue
		}
		f := taskFile{Name: path.Base(p), Path: p}
		if same {
			out = append(out, f)
			continue
		}
		src := p
		if home != "" {
			src = proto.ExpandHome(p, home)
		}
		name, _, data, err := s.readAttachment(cctx, from, src)
		if err != nil {
			f.Error = err.Error()
			out = append(out, f)
			continue
		}
		if len(data) > taskFileMostBytes || total+int64(len(data)) > taskFilesMostBytes {
			f.Error = fmt.Sprintf("over %d MB", taskFilesMostBytes>>20)
			out = append(out, f)
			continue
		}
		total += int64(len(data))
		at, err := s.uploadBytesInto(cctx, to, name, data, proto.UploadDestFile)
		if err != nil && oldNodeUpload(err) {
			// A node from before dest=file: a terminal paste is what it knows, and swept after 7 days.
			at, err = s.uploadBytesInto(cctx, to, name, data, proto.UploadDestTerminal)
		}
		if err != nil {
			f.Error = err.Error()
			out = append(out, f)
			continue
		}
		f.Name, f.Local, f.Size = name, at, int64(len(data))
		out = append(out, f)
		s.logf("task %s: file %s copied from %s to %s", t.ID, src, from, at)
	}
	return out
}

// oldNodeUpload: a node that does not know the upload destination (it was built before dest=file).
func oldNodeUpload(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unknown upload destination")
}

// taskAnswer is a turn's last assistant text whole: the page's text, or the item again when the node cut it
// (FullSize set) and session.item can give the whole one. A second read that fails keeps what the page had.
func (s *Service) taskAnswer(ctx context.Context, machine, sid string, items []proto.Item, text string) string {
	var id string
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Role == "assistant" && it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "" {
			if it.FullSize > 0 {
				id = it.ID
			}
			break
		}
	}
	if id == "" || sid == "" {
		return text
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var full proto.Item
	if err := s.call(cctx, machine, "session.item", map[string]any{"session_id": sid, "item_id": id}, &full); err != nil ||
		full.FullSize > 0 || strings.TrimSpace(full.Text) == "" {
		return text
	}
	return turnReplyText(full.Text, false)
}

// lastTurnAnswer is the latest turn's answer whole (lastTurn with the paged tail read again).
func (s *Service) lastTurnAnswer(ctx context.Context, machine, sid string) string {
	items, ok := s.lastTurnItems(ctx, machine, sid)
	if !ok {
		return ""
	}
	_, answer := turnExchange(items)
	return s.taskAnswer(ctx, machine, sid, items, answer)
}
