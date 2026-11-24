package node

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// readCounts is how many thread/read calls each thread has had, counted in one pass.
func (f *remoteFixture) readCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[string]int{}
	for _, m := range f.got {
		var p struct {
			ThreadID string `json:"threadId"`
		}
		if m.Method == "thread/read" && json.Unmarshal(m.Params, &p) == nil {
			counts[p.ThreadID]++
		}
	}
	return counts
}

// readsSince is the threads read after before was counted, sorted.
func readsSince(before, after map[string]int) []string {
	var read []string
	for raw, n := range after {
		if n > before[raw] {
			read = append(read, raw)
		}
	}
	slices.Sort(read)
	return read
}

// ephemeralReads answers thread/read for helpers as an app-server-compatible daemon answers for a finished sub-Agent
// helper it keeps loaded: ephemeral, with no rollout.
func ephemeralReads(home string, helpers map[string]bool) func(string, map[string]any) (any, bool) {
	return func(method string, p map[string]any) (any, bool) {
		thread, _ := p["threadId"].(string)
		if method != "thread/read" || !helpers[thread] {
			return nil, false
		}
		return map[string]any{"thread": map[string]any{"id": thread, "ephemeral": true, "path": nil, "cwd": home,
			"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{}}},
			"status": map[string]any{"type": "idle"}}}, true
	}
}

// An ephemeral thread is read once: a thread is ephemeral or not from its creation, so later refreshes read only the
// other threads, however many ephemeral ones the app-server keeps loaded (#12517). One that leaves the loaded list is
// forgotten, and read once more if it comes back.
func TestCodexRemoteRemembersEphemeralThreads(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	normal := []string{remoteRaw}
	for i := range 19 {
		normal = append(normal, fmt.Sprintf("cccccccc-0000-0000-0000-%012d", i))
	}
	helpers := map[string]bool{}
	var loaded []string
	for i := range 600 {
		raw := fmt.Sprintf("dddddddd-0000-0000-0000-%012d", i)
		helpers[raw] = true
		loaded = append(loaded, raw)
		if i%30 == 29 {
			loaded = append(loaded, normal[i/30])
		}
	}
	f.mu.Lock()
	f.threads, f.response = loaded, ephemeralReads(home, helpers)
	f.mu.Unlock()
	want := slices.Sorted(slices.Values(normal))

	before := f.readCounts()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	if read := readsSince(before, f.readCounts()); len(read) != len(loaded) {
		t.Fatalf("the first refresh read %d threads, want all %d", len(read), len(loaded))
	}
	for i := range 2 {
		before = f.readCounts()
		if err := c.refresh(); err != nil {
			t.Fatal(err)
		}
		if read := readsSince(before, f.readCounts()); !slices.Equal(read, want) {
			t.Fatalf("refresh %d after the first read %d threads, want only the %d that are not ephemeral", i+1, len(read), len(want))
		}
	}
	var listed []string
	for _, s := range n.codexSessions(map[string]bool{}) {
		listed = append(listed, s.SourceThreadID)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, want) {
		t.Fatalf("sessions = %v, want the %d threads that are not ephemeral", listed, len(want))
	}

	// One leaves the loaded list: forgotten. Back in it: read once more, then remembered again.
	gone := loaded[0]
	f.mu.Lock()
	f.threads = loaded[1:]
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, kept := c.seen[gone]
	remembered := len(c.seen)
	c.mu.Unlock()
	if kept || remembered != len(loaded)-1 {
		t.Fatalf("after a thread left the loaded list: still remembered %v, %d threads remembered, want %d", kept, remembered, len(loaded)-1)
	}
	f.mu.Lock()
	f.threads = loaded
	f.mu.Unlock()
	before = f.readCounts()
	for range 2 {
		if err := c.refresh(); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.readCounts()[gone] - before[gone]; got != 1 {
		t.Fatalf("an ephemeral thread back in the loaded list was read %d times in two refreshes, want once", got)
	}
}

// When a refresh's time cannot cover every read, the thread with a turn running is asked first, and the threads left
// unasked are asked first on the next refresh, ahead of those asked in their place: a long loaded list no longer cuts
// the same threads every time (#12517). A thread known to be ephemeral takes none of that time.
func TestCodexRemoteShortBudgetReadsBusyAndLeftOutThreadsFirst(t *testing.T) {
	n := remoteTestNode(t)
	logs := &syncBuffer{}
	home := t.TempDir()
	const helper = "dddddddd-0000-0000-0000-000000000001"
	var idle []string
	for i := range 2 * codexReadWorkers {
		idle = append(idle, fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", i))
	}
	busy := remoteRaw
	f, c := connectRemoteFixture(t, n, home, "one", slices.Concat(idle, []string{busy})...)
	n.logger = log.New(logs, "", 0)
	ephemeral := ephemeralReads(home, map[string]bool{helper: true})
	f.mu.Lock()
	// The busy thread comes last in the loaded list: reads in list order would reach it only after every idle one.
	f.threads = slices.Concat([]string{helper}, idle, []string{busy})
	f.response = func(method string, p map[string]any) (any, bool) {
		if thread, _ := p["threadId"].(string); method == "thread/read" && thread == busy {
			return map[string]any{"thread": map[string]any{"id": busy, "cwd": home, "status": map[string]any{"type": "active", "activeFlags": []string{}}}}, true
		}
		return ephemeral(method, p)
	}
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	status := c.threads[busy].status
	c.mu.Unlock()
	if status != proto.StatusWorking {
		t.Fatalf("busy thread is %q, want working", status)
	}

	// From here no idle thread's read is answered, and the refresh's second runs out before any read's own three:
	// an unanswered read is cut short by the refresh, not timed out and backed off.
	c.mu.Lock()
	c.readWait, c.refreshWait = 3*time.Second, time.Second
	c.mu.Unlock()
	f.mu.Lock()
	f.slowRead = map[string]bool{}
	for _, raw := range idle {
		f.slowRead[raw] = true
	}
	f.mu.Unlock()
	// Four workers: the busy thread's answer frees one of them for a fourth idle thread, then the time runs out.
	for round, want := range [][]string{idle[:codexReadWorkers], idle[codexReadWorkers:]} {
		before := f.readCounts()
		if err := c.refresh(); err != nil {
			t.Fatal(err)
		}
		after := f.readCounts()
		if got := after[busy] - before[busy]; got != 1 {
			t.Fatalf("budget refresh %d read the busy thread %d times, want once", round+1, got)
		}
		if got := after[helper] - before[helper]; got != 0 {
			t.Fatalf("budget refresh %d read the ephemeral thread %d times", round+1, got)
		}
		var asked []string
		for _, raw := range idle {
			if after[raw] > before[raw] {
				asked = append(asked, raw)
			}
		}
		if !slices.Equal(asked, want) {
			t.Fatalf("budget refresh %d asked for %v, want %v", round+1, asked, want)
		}
	}
	line := fmt.Sprintf("0 of %d threads (0 timed out, %d not read in time, 0 waiting after a timeout, 1 ephemeral skipped)", len(idle)+1, len(idle))
	if got := strings.Count(logs.String(), line); got != 2 {
		t.Fatalf("log = %q, want %q once per budget refresh", logs.String(), line)
	}
}

// A read the refresh's time cuts short is not a read: a thread new to the loaded list whose first read was cut goes
// ahead of the threads read in that refresh, and is read and listed on the next one even when every other read hangs
// (#12517). Counted as read, it went behind them in the loaded list's order, and was cut again or never asked.
func TestCodexRemoteCutShortReadGoesAheadOfThreadsRead(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	var idle []string
	for i := range 2 * codexReadWorkers {
		idle = append(idle, fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", i))
	}
	const fresh = "ffffffff-0000-0000-0000-000000000001"
	f, c := connectRemoteFixture(t, n, home, "one", idle...)
	c.mu.Lock()
	c.readWait, c.refreshWait = 3*time.Second, time.Second
	c.mu.Unlock()

	// The new thread comes last in the loaded list and only its read hangs: every other thread is read, then the
	// refresh's second runs out before the read's own three.
	f.mu.Lock()
	f.threads = slices.Concat(idle, []string{fresh})
	f.slowRead = map[string]bool{fresh: true}
	f.mu.Unlock()
	before := f.readCounts()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	after := f.readCounts()
	if read := readsSince(before, after); len(read) != len(idle)+1 {
		t.Fatalf("the first refresh asked for %d threads, want all %d", len(read), len(idle)+1)
	}
	c.mu.Lock()
	_, listed := c.threads[fresh]
	_, backedOff := c.slow[fresh]
	c.mu.Unlock()
	if listed || backedOff {
		t.Fatalf("a thread whose read was cut short: listed %v, backed off %v", listed, backedOff)
	}

	// Now every other read hangs and the new thread's is answered.
	f.mu.Lock()
	f.slowRead = map[string]bool{}
	for _, raw := range idle {
		f.slowRead[raw] = true
	}
	from := len(f.got)
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	var order []string
	for _, m := range f.got[from:] {
		var p struct {
			ThreadID string `json:"threadId"`
		}
		if m.Method == "thread/read" && json.Unmarshal(m.Params, &p) == nil {
			order = append(order, p.ThreadID)
		}
	}
	f.mu.Unlock()
	if i := slices.Index(order, fresh); i < 0 || i >= codexReadWorkers {
		t.Fatalf("the second refresh asked for %v, want the new thread among the first %d", order, codexReadWorkers)
	}
	listed = false
	for _, s := range n.codexSessions(map[string]bool{}) {
		listed = listed || s.SourceThreadID == fresh
	}
	if !listed {
		t.Fatal("the new thread is not listed after its read was answered")
	}
}

// The read order: threads with a turn running or a request waiting first, then by when each was last read, never read
// first; a read cut short does not count as read, but goes behind threads not asked; the loaded list's order breaks
// ties.
func TestCodexReadOrder(t *testing.T) {
	at := time.Now()
	c := &codexRemote{
		threads: map[string]remoteThread{
			"working":  {status: proto.StatusWorking},
			"blocked":  {status: proto.StatusBlocked},
			"done-old": {status: proto.StatusDone},
			"done-new": {status: proto.StatusDone},
		},
		seen: map[string]codexSeen{
			"working":  {read: at},
			"blocked":  {read: at.Add(-time.Minute)},
			"done-old": {read: at.Add(-time.Hour)},
			"done-new": {read: at},
			"cut-once": {cut: at.Add(-time.Second)}, // asked once, cut short by the refresh's time: not listed yet
		},
	}
	reads := []string{"done-new", "never-a", "working", "done-old", "cut-once", "never-b", "blocked"}
	c.readOrder(reads)
	want := []string{"blocked", "working", "never-a", "never-b", "cut-once", "done-old", "done-new"}
	if !slices.Equal(reads, want) {
		t.Fatalf("order = %v, want %v", reads, want)
	}
}
