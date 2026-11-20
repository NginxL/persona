package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex-compatible CLI can run in herdr panes without being recognized by the host. The node therefore finds
// it itself in every host's panes:
//   - <CLI data>/thread-writer-locks/<thread id>.owner.json names the pid writing each thread (session);
//   - that pid's environment holds the host's pane variable (host.PaneEnv: HERDR_PANE_ID), so pane → session is
//     exact, no guessing by directory;
//   - its conversation is a rollout under <CLI data>/sessions/YYYY/MM/DD, the newer Codex format (transcript/codex.go);
//   - herdr has no state for the pane: a spinner in the terminal title or "esc to interrupt" on screen is working,
//     a numbered choice on screen is blocked, anything else is a finished turn (the hub turns that into "Unread" / "Seen").

const traexAgent = "traex"

func (n *Node) traexHome() string {
	if n.sessionScope != nil {
		return filepath.Join(n.dir, "traex")
	}
	return filepath.Join(n.home, ".trae", "cli")
}

func (n *Node) traexPath(id string) string {
	if _, ok := n.traexThread(id); ok {
		p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: id})
		if fileExists(p) {
			return p
		}
	}
	return rolloutPath(n.traexHome(), id)
}

func (n *Node) traexTitle(id string) string {
	if t, ok := n.traexThread(id); ok && t.Title != "" {
		return t.Title
	}
	return threadTitle(n.traexHome(), id)
}

var traexCache = struct {
	sync.Mutex
	panes  map[int]string // pid → its pane, a stored PaneRef (from its environment; a pid never moves)
	status map[string]traexState
}{panes: map[int]string{}, status: map[string]traexState{}}

type traexState struct {
	status string
	at     time.Time
}

// traexOwners maps each live compatible CLI process to its newest main conversation.
func (n *Node) traexOwners() map[int]string {
	out := map[int]string{}
	for pid, threads := range n.traexOwnerThreads(n.traexThreads()) {
		out[pid] = threads[0]
	}
	return out
}

// Fresh starts need positive writer evidence, including when old IDs were archived,
// outside the rollout scan window, or absent from a cached metadata list.
func (n *Node) traexLiveThread(pane, preferred string) string {
	owners := map[int][]string{}
	for pid, ids := range n.traexOwnerThreads(n.traexThreads()) {
		if n.paneOfPID(pid) == pane {
			owners[pid] = ids
		}
	}
	return traexPaneThread(owners, preferred, parentPID)
}

// traexOwnerThreads keeps every main writer owned by a process, newest first: a host's existing session may still
// have its own writer even when another thread is newer. Known subagents never fall back to their own id. Legacy
// subagents may supply a root confirmed by rollout metadata; older CLIs without metadata keep the owner-file fallback.
func (n *Node) traexOwnerThreads(threads map[string]codexAppThread) map[int][]string {
	dir := filepath.Join(n.traexHome(), "thread-writer-locks")
	files, _ := filepath.Glob(filepath.Join(dir, "*.owner.json"))
	type owner struct {
		thread string
		mtime  time.Time
	}
	byPID := map[int][]owner{}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var o struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(b, &o) != nil || o.PID <= 0 || !alive(o.PID) {
			continue
		}
		thread := strings.TrimSuffix(filepath.Base(f), ".owner.json")
		if validID(thread) {
			byPID[o.PID] = append(byPID[o.PID], owner{thread, st.ModTime()})
		}
	}
	home := n.traexHome()
	paginated := codexHistoryDBPath(home) != ""
	out := make(map[int][]string, len(byPID))
	for pid, owners := range byPID {
		sort.Slice(owners, func(i, j int) bool {
			if owners[i].mtime.Equal(owners[j].mtime) {
				return owners[i].thread < owners[j].thread
			}
			return owners[i].mtime.After(owners[j].mtime)
		})
		var main, fallback []string
		for _, o := range owners {
			t, known := threads[o.thread]
			if known && traexConversation(t) {
				main = append(main, o.thread)
				continue
			}
			p := rolloutPath(home, o.thread)
			if p == "" {
				// A new paginated owner can precede its state row. Wait for its source.
				if !known && !paginated {
					fallback = append(fallback, o.thread)
				}
				continue
			}
			m := metaOf(home, p)
			if !known && !m.sub {
				main = append(main, o.thread)
				continue
			}
			root := m.root
			if root == o.thread || !validID(root) {
				continue
			}
			parent, listed := threads[root]
			if listed && !traexConversation(parent) {
				continue
			}
			rootPath := rolloutPath(home, root)
			if (listed || rootPath != "") && (rootPath == "" || !metaOf(home, rootPath).sub) {
				fallback = append(fallback, root)
			}
		}
		if len(main) > 0 {
			out[pid] = main
		} else if len(fallback) > 0 {
			out[pid] = fallback
		}
	}
	return out
}

// alive says whether the process behind a pid file this user's claude or a compatible CLI wrote still runs. A pid now held by
// another user's process is a reused one, not the program that wrote the file (#2141). The cost: a claude run as
// another user that still writes into this user's home (`sudo claude`, sudo keeping HOME on macOS) counts as gone.
func alive(pid int) bool { return osproc.AliveMine(pid) }

// paneEnvRe finds a host's pane variable (host.PaneEnv) in a process's environment, NUL- (Linux) or space-separated
// (ps -E).
func paneEnvRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\x00\s])` + regexp.QuoteMeta(name) + `=([^\x00\s]+)`)
}

var herdrPaneEnvRe = paneEnvRe(herdrPaneEnv)

// paneEnvRes caches paneEnvRe per variable.
var paneEnvRes sync.Map // name -> *regexp.Regexp

// paneOfPID reads the pane a process runs in from its environment (Linux: /proc; macOS: ps -E): the variable of the
// first host whose PaneEnv it holds, as a stored PaneRef. Cached per pid.
func (n *Node) paneOfPID(pid int) string {
	traexCache.Lock()
	p, ok := traexCache.panes[pid]
	traexCache.Unlock()
	if ok {
		return p
	}
	if len(n.hosts.All()) == 0 {
		return "" // nothing to look for; not cached, the pid's pane is not known to be none
	}
	var env []byte
	if runtime.GOOS == "linux" {
		env, _ = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		env, _ = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		cancel()
	}
	for _, h := range n.hosts.All() {
		re, ok := paneEnvRes.Load(h.PaneEnv())
		if !ok {
			re, _ = paneEnvRes.LoadOrStore(h.PaneEnv(), paneEnvRe(h.PaneEnv()))
		}
		if m := re.(*regexp.Regexp).FindSubmatch(env); m != nil {
			p = host.PaneRef{Kind: h.Kind(), ID: string(m[1])}.String()
			if h.Kind() == host.Herdr { // which server's pane: a named session's is qualified (#2196)
				picked := pickedEnv(string(env))
				p = n.envPane(func(name string) string { return picked[name] })
			}
			break
		}
	}
	traexCache.Lock()
	traexCache.panes[pid] = p
	traexCache.Unlock()
	return p
}

// traexAgents turns the panes running a compatible CLI into agents, with their session id and a state of our own.
func (n *Node) traexAgents(ctx context.Context, panes []host.Agent, agents []host.Agent) []host.Agent {
	threads := n.traexThreads()
	owners := n.traexOwnerThreads(threads)
	isAgent := map[string]bool{}
	compatible := map[string]int{}
	for i, a := range agents {
		// PaneReportResume claims a compatible-CLI pane in herdr so resume_argv persists, which makes herdr list a
		// session-less traex entry in snap.Agents; keep filling its session id and screen status from the owner file.
		if a.Agent != traexAgent {
			isAgent[a.PaneID] = true
		} else {
			compatible[a.PaneID] = i
			if t, known := threads[a.SessionID()]; known && !traexConversation(t) {
				agents[i].AgentSession = nil // a stale host claim cannot promote a known guardian to a terminal chat
			}
		}
	}
	byPane := map[string]map[int][]string{}
	for pid, ids := range owners {
		pane := n.paneOfPID(pid)
		if byPane[pane] == nil {
			byPane[pane] = map[int][]string{}
		}
		byPane[pane][pid] = ids
	}
	var out []host.Agent
	for _, p := range panes {
		pane := p.PaneID
		if isAgent[pane] || len(byPane[pane]) == 0 {
			continue
		}
		preferred := ""
		if i, ok := compatible[pane]; ok {
			preferred = agents[i].SessionID()
		}
		thread := traexPaneThread(byPane[pane], preferred, parentPID)
		if thread == "" {
			continue
		}
		p.Agent = traexAgent
		p.AgentSession = &host.AgentSession{Kind: "id", Value: thread}
		// A host workspace is a terminal container, which may be unrelated to where
		// Run started this CLI. Keep its placement, but report the conversation's cwd.
		if t, ok := threads[thread]; ok && t.Cwd != "" {
			p.Cwd = t.Cwd
		} else if cwd := metaOf(n.traexHome(), rolloutPath(n.traexHome(), thread)).cwd; cwd != "" {
			p.Cwd = cwd
		}
		p.AgentStatus = n.traexStatus(ctx, p)
		if i, ok := compatible[pane]; ok {
			agents[i] = p
			continue
		}
		out = append(out, p)
	}
	return out
}

// traexPaneThread picks one terminal conversation per pane. Nested exec writers inherit the pane environment,
// but cannot replace an ancestor's conversation. Among independent roots a still-owned host id wins, else the
// lowest pid makes the choice stable. Without ancestry and a host claim, ambiguous writers are left unclaimed.
func traexPaneThread(owners map[int][]string, preferred string, parent func(int) (int, error)) string {
	pids := make([]int, 0, len(owners))
	for pid := range owners {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	roots := pids
	complete := true
	if len(pids) > 1 { // most panes have one writer process: no process-tree reads needed
		roots = nil
		for _, pid := range pids {
			ancestors := lineage(pid, func(pid int) (int, error) {
				next, err := parent(pid)
				if err != nil {
					complete = false
				}
				return next, err
			})
			if len(ancestors) == ancestorsMost {
				complete = false
			}
			nested := false
			for _, ancestor := range ancestors[1:] {
				if len(owners[ancestor]) > 0 {
					nested = true
					break
				}
			}
			if !nested {
				roots = append(roots, pid)
			}
		}
	}
	for _, pid := range roots {
		for _, id := range owners[pid] {
			if id == preferred {
				return id
			}
		}
	}
	if len(roots) == 0 || !complete && len(roots) > 1 {
		return ""
	}
	return owners[roots[0]][0]
}

var traexChoiceRe = lazyre.New(`(?m)^\s*[›❯>]\s*1\.\s+\S`)

// traexStatus: working from the title's spinner at once; otherwise the screen, read at most every 2s.
func (n *Node) traexStatus(ctx context.Context, p host.Agent) string {
	if r := []rune(strings.TrimSpace(p.RawTitle)); len(r) > 0 && r[0] >= 0x2800 && r[0] <= 0x28FF {
		return proto.StatusWorking // braille spinner: a turn is running
	}
	traexCache.Lock()
	s, ok := traexCache.status[p.PaneID]
	traexCache.Unlock()
	if ok && time.Since(s.at) < 2*time.Second {
		return s.status
	}
	status := proto.StatusDone
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	text, err := n.readPane(rctx, p.PaneID, "visible", 40)
	cancel()
	switch {
	case err != nil:
		status = s.status // keep the last known
		if status == "" {
			status = proto.StatusUnknown
		}
	case strings.Contains(text, "esc to interrupt"):
		status = proto.StatusWorking
	case traexChoiceRe.MatchString(text):
		status = proto.StatusBlocked
	}
	traexCache.Lock()
	traexCache.status[p.PaneID] = traexState{status, time.Now()}
	traexCache.Unlock()
	return status
}
