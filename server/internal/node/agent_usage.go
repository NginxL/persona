package node

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
	"github.com/nwverse/persona/server/internal/transcript"
)

// A finished subagent's or workflow run's usage on the items that report it (#5426). A sync Agent call's result, a
// background agent's completion notice or hand-back (Task kind "agent" with its id, in a final status) carry what the
// subagent's own transcript used, and what each of its nested subagents' did (parentAgentId, subagents.go); a
// workflow's final report (Task kind "workflow") what each agent of its run used. Every agent's entries (one per model
// and speed) have ID "agent:<agent id>": a client counts an agent reported on more than one item once (a hand-back and
// its notice; a nested background agent, which reports to the session itself too). The session's transcript has none
// of it (its sidechain lines carry no usage). Pages and pushes of a Claude Code transcript get it (session.items,
// pushItems); a batch with no such item reads nothing, and an agent's file is read once, then only for the lines it
// gained. A team's in-process teammate (its meta's taskKind) is left out, as an item and as a nested subagent: a
// long-lived worker with its own transcript in the Team panel, whose usage is not the turn that spawned it (and would
// keep growing a past turn's number while it runs).

// agentUsage puts each finished subagent's or workflow run's usage on the items of f's batch that report it.
func agentUsage(f *transcript.File, items []proto.Item) {
	dir := ""
	for i := range items {
		t := items[i].Task
		if t == nil || t.Kind != "agent" && t.Kind != "workflow" || !transcript.AgentTerminal(t.Status) || !agentFileID(t.TaskID) {
			continue
		}
		if dir == "" {
			if f == nil {
				return
			}
			if dir = subagentsDirOf(f.Where()); dir == "" {
				return
			}
		}
		if t.Kind == "workflow" {
			items[i].Usage = workflowUsage(dir, t.TaskID)
		} else {
			items[i].Usage = subagentUsage(dir, t.TaskID)
		}
	}
}

// agentFileID says whether id can name a subagent's file (agent-<id>.jsonl) in its directory.
func agentFileID(id string) bool { return id != "" && !strings.ContainsAny(id, "/.\\") }

// subagentsDirOf is the directory of the subagents of the Claude Code transcript at path: <session>/subagents for a
// session's, the one it is in for a subagent's own (its nested ones are beside it). "" for a Codex rollout.
func subagentsDirOf(path string) string {
	if !strings.HasSuffix(path, ".jsonl") || strings.HasPrefix(filepath.Base(path), "rollout-") {
		return ""
	}
	if dir := filepath.Dir(path); filepath.Base(dir) == "subagents" {
		return dir
	}
	return filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
}

// subagentUsage is what subagent id of dir and each of its nested subagents used, each under its own id and counted
// once (a cycle in the metas stops there); nil when their transcripts say nothing, or it is a teammate.
func subagentUsage(dir, id string) []proto.TokenUsage {
	if teammate(dir, id) {
		return nil
	}
	kids := agentChildren(dir)
	var out []proto.TokenUsage
	var seen map[string]bool
	var walk func(string)
	walk = func(a string) {
		out = appendAgentUsage(out, a, agentFileUsage(filepath.Join(dir, "agent-"+a+".jsonl")))
		for _, k := range kids[a] {
			if seen == nil {
				seen = map[string]bool{id: true}
			}
			if !seen[k] {
				seen[k] = true
				if !teammate(dir, k) {
					walk(k)
				}
			}
		}
	}
	walk(id)
	return out
}

// appendAgentUsage appends agent id's own entries, under its id.
func appendAgentUsage(out []proto.TokenUsage, id string, own []proto.TokenUsage) []proto.TokenUsage {
	for _, u := range own {
		u.ID = "agent:" + id
		out = append(out, u)
	}
	return out
}

// workflowUsage is what each agent of the workflow run that background task taskID launched used, under its own id
// (an agent id names one subagent of the session, a workflow's too), the attempts a retry replaced included: they ran.
// nil when no run of the session says it was the task's (a run's state file names its task; Claude Code writes it as
// the run ends, before the report).
func workflowUsage(subagents, taskID string) []proto.TokenUsage {
	run := workflowRunOf(filepath.Dir(subagents), taskID)
	if run == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(subagents, "workflows", run, "agent-*.jsonl"))
	var out []proto.TokenUsage
	for _, p := range paths {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "agent-"), ".jsonl")
		out = appendAgentUsage(out, id, agentFileUsage(p))
	}
	return out
}

// wfRunsCache remembers each session's workflows directory (<session>/workflows) read for the task each run's state
// file names, by the directory's mtime like agentTreeCache; a state file is read again only when it changed.
var wfRunsCache = struct {
	sync.Mutex
	m *lru[string, wfRuns]
}{m: newLRU[string, wfRuns](agentTreeMax)}

type wfRuns struct {
	mtime, readAt time.Time
	byTask        map[string]string // task id → run id
	files         map[string]wfRunFile
	complete      bool // every state file named its task
}

// wfRunFile is what a run's state file (<run id>.json) said, as of its size and mtime.
type wfRunFile struct {
	size  int64
	mtime time.Time
	task  string
}

// workflowRunOf is the run ("wf_…") of session's that background task taskID launched, "" when no state file says.
func workflowRunOf(session, taskID string) string {
	dir := filepath.Join(session, "workflows")
	st, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	wfRunsCache.Lock()
	r, ok := wfRunsCache.m.get(dir)
	wfRunsCache.Unlock()
	if ok && r.complete && r.mtime.Equal(st.ModTime()) && r.readAt.Sub(r.mtime) > agentTreeSettle {
		return r.byTask[taskID]
	}
	next := wfRuns{mtime: st.ModTime(), readAt: time.Now(), byTask: map[string]string{}, files: map[string]wfRunFile{}, complete: true}
	paths, _ := filepath.Glob(filepath.Join(dir, "wf_*.json"))
	for _, p := range paths {
		run := strings.TrimSuffix(filepath.Base(p), ".json")
		fst, err := os.Stat(p)
		if err != nil || strings.ContainsAny(run, "/\\") || strings.Contains(run, "..") {
			continue
		}
		e, ok := r.files[p]
		if !ok || e.size != fst.Size() || !e.mtime.Equal(fst.ModTime()) {
			e = wfRunFile{size: fst.Size(), mtime: fst.ModTime(), task: workflowStateTask(p)}
		}
		next.files[p] = e
		if e.task == "" {
			next.complete = false // one being written: read again next time
			continue
		}
		next.byTask[e.task] = run
	}
	wfRunsCache.Lock()
	wfRunsCache.m.put(dir, next)
	wfRunsCache.Unlock()
	return next.byTask[taskID]
}

// workflowStateTask is the task a run's state file names (taskId), read only as far as that key: the file goes on with
// the run's whole script and result.
func workflowStateTask(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ""
		}
		if key == "taskId" {
			var task string
			if dec.Decode(&task) != nil {
				return ""
			}
			return task
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return ""
		}
	}
	return ""
}

// teammate says whether subagent id of dir is a team's in-process teammate, by its meta (cached, subagents.go); one
// without a meta is not.
func teammate(dir, id string) bool {
	m, ok := subagentMetaCached(filepath.Join(dir, "agent-"+id+".meta.json"))
	return ok && m.TaskKind == "in_process_teammate"
}

// agentTreeCache remembers each subagents directory's launches (agentChildren) by the directory's mtime: a new
// subagent's files change it, and a meta's launcher never changes. Bounded, the least recently asked for forgotten first.
var agentTreeCache = struct {
	sync.Mutex
	m *lru[string, agentTree]
}{m: newLRU[string, agentTree](agentTreeMax)}

const agentTreeMax = 256

type agentTree struct {
	mtime, readAt time.Time
	kids          map[string][]string // launcher → the subagents it launched
	complete      bool                // every meta parsed
}

// agentTreeSettle: a directory changed this close to when it was read may have changed again within its mtime's
// granularity; it is read again.
const agentTreeSettle = 2 * time.Second

// agentChildren is dir's nested subagents by the subagent that launched them (their metas' parentAgentId).
func agentChildren(dir string) map[string][]string {
	st, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	agentTreeCache.Lock()
	t, ok := agentTreeCache.m.get(dir)
	agentTreeCache.Unlock()
	if ok && t.complete && t.mtime.Equal(st.ModTime()) && t.readAt.Sub(t.mtime) > agentTreeSettle {
		return t.kids
	}
	t = agentTree{mtime: st.ModTime(), readAt: time.Now(), complete: true}
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	for _, mp := range metas {
		m, ok := subagentMetaCached(mp)
		if !ok {
			t.complete = false // one being written: asked again next time
			continue
		}
		if m.ParentID == "" {
			continue
		}
		if t.kids == nil {
			t.kids = map[string][]string{}
		}
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(mp), "agent-"), ".meta.json")
		t.kids[m.ParentID] = append(t.kids[m.ParentID], id)
	}
	agentTreeCache.Lock()
	agentTreeCache.m.put(dir, t)
	agentTreeCache.Unlock()
	return t.kids
}

// agentUsageCache remembers what each subagent transcript used so far, by path; a file is read once, then only for
// the lines it gained. Bounded like metaCache.
var agentUsageCache = struct {
	sync.Mutex
	m *lru[string, *agentUsageEntry]
}{m: newLRU[string, *agentUsageEntry](metaCacheMax)}

// agentUsageEntry is one file's; its lock keeps two batches from reading it at once.
type agentUsageEntry struct {
	mu    sync.Mutex
	info  os.FileInfo
	size  int64
	mtime time.Time
	off   int64  // where the complete lines read so far end
	head  []byte // the file's first bytes read: a rewrite even to a larger size starts over
	// started: a user line was read. A fork's transcript starts with its launcher's reply that launched it, copied
	// (that request is the launcher's); the subagent's own work starts with its prompt.
	started bool
	parser  tokenuse.Parser
	sum     []proto.TokenUsage
}

// agentFileUsage is what the subagent transcript at path used, per model and speed (a copy: callers may keep it).
func agentFileUsage(path string) []proto.TokenUsage {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	agentUsageCache.Lock()
	e, ok := agentUsageCache.m.get(path)
	if !ok {
		e = &agentUsageEntry{}
		agentUsageCache.m.put(path, e)
	}
	agentUsageCache.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.info != nil && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return slices.Clone(e.sum)
	}
	f, err := os.Open(path)
	if err != nil {
		return slices.Clone(e.sum)
	}
	defer f.Close()
	if now, err := f.Stat(); err == nil {
		st = now
	}
	var buf [256]byte
	n, _ := f.ReadAt(buf[:min(st.Size(), int64(len(buf)))], 0)
	head := buf[:n]
	if e.info == nil || !os.SameFile(e.info, st) || st.Size() <= e.size || !bytes.HasPrefix(head, e.head) {
		e.info, e.size, e.off, e.head, e.started, e.parser, e.sum = nil, 0, 0, e.head[:0], false, tokenuse.ClaudeParser(), nil
	}
	e.off += eachLine(io.NewSectionReader(f, e.off, st.Size()-e.off), e.line)
	e.head = append(e.head[:0], head[:min(int64(len(head)), e.off)]...)
	e.info, e.size, e.mtime = st, st.Size(), st.ModTime()
	return slices.Clone(e.sum)
}

// line counts one line of the transcript: each request once, at the most its lines say (tokenuse.ClaudeParser, the
// usage meter's rules).
func (e *agentUsageEntry) line(line []byte) {
	switch lineKind(line) {
	case 'u':
		e.started = true
	case 'a':
		if !e.started {
			return
		}
		if r, ok := e.parser.Line(line); ok {
			e.sum = transcript.AddUsage(e.sum, r.Item())
		}
	}
}
