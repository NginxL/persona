package hub

import (
	"context"
	"sort"
	"time"
)

// Live Files / Git for the web panels (#2439, docs/web/web.md "Live refresh"). A browser tells the hub, over its
// WebSocket, which folders of one machine it has open: {type: "fs_watch", machine_id, cwd, paths, git}. Each call
// replaces that browser's set for the machine and session folder (cwd); no paths and git false (or the socket
// closing) releases it. The hub
// sends the node the union of every browser's set (fs.watch, replaced as a whole) and routes the node's fs.changed
// back as {type: "fs_changed", machine_id, cwd, dirs, git} to the browsers whose set it touches. Not in the event
// log: a browser that was away re-lists when it comes back, so there is nothing to replay.
//
// Full devices only: it tells a browser what happens in a computer's folders, which an answer-only device cannot even
// list (devicetokens.go answerReads).

const (
	maxFsWatchPaths = 32   // folders per browser and machine
	maxFsPathLen    = 4096 // one path
)

// fsWatchReq is one browser's open folders on one machine.
type fsWatchReq struct {
	Cwd   string
	Paths []string
	Git   bool
	// AskedID is the machine id the browser sent, which fs_changed echoes: it may be an id the computer absorbed,
	// and the page matches the event by the id it asked with (web/src/lib/fs-live.ts).
	AskedID string
}

func (r fsWatchReq) empty() bool { return len(r.Paths) == 0 && !r.Git }

// cleanFsWatch bounds what a browser sent.
func cleanFsWatch(cwd string, paths []string, git bool) fsWatchReq {
	r := fsWatchReq{Cwd: cwd, Git: git}
	if cwd == "" || len(cwd) > maxFsPathLen {
		return fsWatchReq{}
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || len(p) > maxFsPathLen || seen[p] || len(r.Paths) >= maxFsWatchPaths {
			continue
		}
		seen[p] = true
		r.Paths = append(r.Paths, p)
	}
	return r
}

// fsWatchSet records a browser's open folders for a machine and brings the node's set in line. The set is keyed by
// the computer's own id, which fsWatchSync and fsChanged look it up by, so an absorbed id reaches the node too; and by
// the id asked with, so panels of one page that watch a folder by both ids keep their own sets and events.
func (h *Hub) fsWatchSet(c *client, machineID string, req fsWatchReq) {
	m := h.localMachine(machineID)
	if m == nil {
		return
	}
	if req.Cwd == "" { // nothing to key it by (cleanFsWatch refused it)
		return
	}
	key := [3]string{m.cfg.ID, req.Cwd, machineID}
	req.AskedID = machineID
	c.mu.Lock()
	if req.empty() {
		delete(c.fsw, key)
	} else {
		if c.fsw == nil {
			c.fsw = map[[3]string]fsWatchReq{}
		}
		c.fsw[key] = req
	}
	c.mu.Unlock()
	h.queueFSWatchSync(m)
}

// queueFSWatchSync keeps one sync worker per machine. A change made while its node call is out marks one trailing
// sync; every later change coalesces into that same sync, which reads the union when its turn comes. The connection's
// read loop only records its own set and returns, and the hub's nodeWorkers join covers both the call and the tail.
func (h *Hub) queueFSWatchSync(m *Machine) {
	m.fswMu.Lock()
	if m.fswRunning {
		m.fswPending = true
		m.fswMu.Unlock()
		return
	}
	m.fswRunning = true
	h.nodeWorkers.Add(1)
	m.fswMu.Unlock()
	go func() {
		defer h.nodeWorkers.Done()
		for {
			h.fsWatchSync(m)
			m.fswMu.Lock()
			if !m.fswPending {
				m.fswRunning = false
				m.fswMu.Unlock()
				return
			}
			m.fswPending = false
			m.fswMu.Unlock()
		}
	}()
}

// fsWatchRelease drops everything a closing connection watched.
func (h *Hub) fsWatchRelease(c *client) {
	c.mu.Lock()
	ids := map[string]bool{}
	for key := range c.fsw {
		ids[key[0]] = true
	}
	c.fsw = nil
	c.mu.Unlock()
	for id := range ids {
		if m := h.localMachine(id); m != nil {
			h.queueFSWatchSync(m)
		}
	}
}

type fsWatchDirParam struct {
	Path string `json:"path"`
	Cwd  string `json:"cwd"`
}

type fsWatchParam struct {
	Dirs []fsWatchDirParam `json:"dirs"`
	Git  []string          `json:"git"`
}

// fsWatchUnion is every connected browser's folders on the machine, deduplicated.
func (h *Hub) fsWatchUnion(machineID string) fsWatchParam {
	var out fsWatchParam
	dirs := map[fsWatchDirParam]bool{}
	gits := map[string]bool{}
	for _, c := range h.fsWatchClients() {
		for _, r := range c.fsWatching(machineID) {
			for _, p := range r.Paths {
				d := fsWatchDirParam{Path: p, Cwd: r.Cwd}
				if !dirs[d] {
					dirs[d] = true
					out.Dirs = append(out.Dirs, d)
				}
			}
			if r.Git && !gits[r.Cwd] {
				gits[r.Cwd] = true
				out.Git = append(out.Git, r.Cwd)
			}
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return out.Dirs[i].Path < out.Dirs[j].Path })
	sort.Strings(out.Git)
	return out
}

// fsWatching is what the browser has open on a machine, one entry per session folder.
func (c *client) fsWatching(machineID string) []fsWatchReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []fsWatchReq
	for key, r := range c.fsw {
		if key[0] == machineID {
			out = append(out, r)
		}
	}
	return out
}

func (h *Hub) fsWatchClients() []*client {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		list = append(list, c)
	}
	return list
}

// fsWatchSync sends the machine's node the current union. Only queueFSWatchSync's worker calls it, so there is one
// call in flight. Nothing is sent while there is nothing to watch and the node was told nothing before, unless a new
// node connection forced a resend. A node from before this (no fs.watch) answers with an error that is only logged,
// the panels then refresh by hand as they always did.
func (h *Hub) fsWatchSync(m *Machine) {
	m.fswMu.Lock()
	force := m.fswForce
	m.fswForce = false
	sent := m.fswSent
	m.fswMu.Unlock()
	p := h.fsWatchUnion(m.cfg.ID)
	has := len(p.Dirs)+len(p.Git) > 0
	if !has && !sent && !force {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if p.Dirs == nil {
		p.Dirs = []fsWatchDirParam{}
	}
	err := m.call(ctx, "fs.watch", p, nil)
	m.fswMu.Lock()
	defer m.fswMu.Unlock()
	if err != nil {
		// A reconnect used to leave fswSent set on this path, so the next empty set tried again. Consuming fswForce
		// must not lose that recovery when the node may have kept the previous hub's watches.
		if force {
			m.fswSent = true
		}
		if !m.fswWarned {
			m.fswWarned = true
			h.logf("[%s] fs.watch: %v", m.cfg.ID, err)
		}
		return
	}
	m.fswSent = has
}

// fsWatchReconnected runs when a node's connection is new to the hub (a reconnect, or the hub just started): the
// node knows only what an earlier hub told it, which may be a set nobody shows any more, so it is told the union
// again, an empty one included. The forced sync joins any call already out instead of overtaking it.
func (h *Hub) fsWatchReconnected(m *Machine) {
	m.fswMu.Lock()
	m.fswForce = true
	m.fswMu.Unlock()
	h.queueFSWatchSync(m)
}

// fsChanged routes a node's fs.changed to the browsers that have the folders (or the Git directory) open.
func (h *Hub) fsChanged(machineID string, dirs, git []string) {
	if len(dirs)+len(git) == 0 {
		return
	}
	changedDir := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		changedDir[d] = true
	}
	changedGit := make(map[string]bool, len(git))
	for _, g := range git {
		changedGit[g] = true
	}
	for _, c := range h.fsWatchClients() {
		for _, r := range c.fsWatching(machineID) {
			hit := []string{}
			for _, p := range r.Paths {
				if changedDir[p] {
					hit = append(hit, p)
				}
			}
			gitHit := r.Git && changedGit[r.Cwd]
			if len(hit) == 0 && !gitHit {
				continue
			}
			c.sendJSON(map[string]any{"type": "fs_changed", "machine_id": r.AskedID, "cwd": r.Cwd, "dirs": hit, "git": gitHit})
		}
	}
}
