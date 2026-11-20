package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// writeOmpSession writes the head of an omp session file and returns its path.
func writeOmpSession(t *testing.T, sessions, dir, id, title, cwd string, modified time.Time) string {
	t.Helper()
	d := filepath.Join(sessions, dir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	body := ""
	if title != "" {
		body += `{"type":"title","v":1,"title":` + q(title) + "}\n"
	}
	body += `{"type":"session","version":3,"id":"` + id + `","cwd":` + q(cwd) + `,"title":` + q(title) + "}\n"
	p := filepath.Join(d, "2026-11-09T00-00-00-000Z_"+id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, modified, modified); err != nil {
		t.Fatal(err)
	}
	return p
}

func ompSessionsDir(home string) string { return filepath.Join(home, ".omp", "agent", "sessions") }

// The recent stopped sessions are listed with their id, cwd, title and mtime, newest first; an old one is left
// out, and a second list within the cache window does not read the files again.
func TestOmpDiskScan(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	sessions := ompSessionsDir(home)
	now := time.Now()
	writeOmpSession(t, sessions, "-w-app", "01a10000-0000-7000-8000-000000000001", "fix the parser", "/w/app", now.Add(-time.Minute))
	writeOmpSession(t, sessions, "-w-lib", "01a10000-0000-7000-8000-000000000002", "", "/w/lib", now.Add(-2*time.Minute))
	writeOmpSession(t, sessions, "-w-old", "01a10000-0000-7000-8000-000000000003", "old", "/w/old", now.Add(-8*24*time.Hour))

	rows := n.ompSessions()
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].ID != "01a10000-0000-7000-8000-000000000001" || rows[0].Title != "fix the parser" ||
		rows[0].Cwd != "/w/app" || rows[1].ID != "01a10000-0000-7000-8000-000000000002" {
		t.Fatalf("rows %+v", rows)
	}
	reads := n.omps().reads
	_ = n.ompSessions()
	if n.omps().reads != reads {
		t.Fatalf("the files were read again within the cache window")
	}

	// A changed file is read again; an unchanged one is not.
	p := writeOmpSession(t, sessions, "-w-app", "01a10000-0000-7000-8000-000000000001", "fix the parser", "/w/app", now)
	n.omps().at = time.Time{}
	_ = n.ompSessions()
	if n.omps().reads != reads+1 {
		t.Fatalf("one changed file read %d times", n.omps().reads-reads)
	}
	_ = p
}

// A file with a cut last line or a corrupt line does not lose the session: what parses is used.
func TestOmpDiskToleratesBadLines(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	sessions := ompSessionsDir(home)
	d := filepath.Join(sessions, "-w-x")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "01a10000-0000-7000-8000-000000000004"
	body := "not json\n" +
		`{"type":"session","version":3,"id":"` + id + `","cwd":"/w/x"}` + "\n" +
		`{"type":"custom","customType":"persona-host","data":{"host":"herdr"` + "\n" // cut by the writer
	if err := os.WriteFile(filepath.Join(d, "t_"+id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	n.omps().at = time.Time{}
	rows := n.ompSessions()
	if len(rows) != 1 || rows[0].ID != id || rows[0].Cwd != "/w/x" {
		t.Fatalf("rows %+v", rows)
	}
}

// At most ompDiskMax rows, newest first; no directory at all is an empty list, no error.
func TestOmpDiskCapAndMissingDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	if rows := n.ompSessions(); len(rows) != 0 {
		t.Fatalf("no directory: %+v", rows)
	}
	n.omps().at = time.Time{} // the directory appears: the next list scans it
	sessions := ompSessionsDir(home)
	now := time.Now()
	for i := range ompDiskMax + 5 {
		id := fmt.Sprintf("01a10000-0000-7000-8000-%012d", i)
		writeOmpSession(t, sessions, "-w-many", id, "", "/w/many", now.Add(-time.Duration(i)*time.Second))
	}
	rows := n.ompSessions()
	if len(rows) != ompDiskMax {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0].ID != "01a10000-0000-7000-8000-000000000000" || !rows[0].Modified.After(rows[1].Modified) {
		t.Fatalf("not newest first: %+v", rows[:2])
	}
}

// A stopped session's row: no words, a notice in their place, and a send refused until it can be resumed.
func TestOmpOfflineRowAndCalls(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	now := time.Now()
	id := "01a10000-0000-7000-8000-000000000005"
	writeOmpSession(t, ompSessionsDir(home), "-w-off", id, "stopped work", "/w/off", now)

	list := listSessions(t, n)
	s := onlyRow(t, list, id)
	f.reset()
	if s.Agent != ompAgent || s.Status != "offline" || s.Title != "stopped work" || s.Launcher != ompAgent {
		t.Fatalf("row %+v", s)
	}
	if s.LastText != "" {
		t.Fatalf("the disk row carries words: %+v", s)
	}
	// A live row of the same id hides the disk one.
	if hidden := n.ompOfflineRows(map[string]bool{id: true}); len(hidden) != 0 {
		t.Fatalf("a live id was listed from disk: %+v", hidden)
	}

	res, err := request(t, n, "session.items", map[string]any{"session_id": id, "turns": 2})
	if err != nil || !strings.Contains(fmt.Sprint(res), transcript.OmpStoppedNotice) {
		t.Fatalf("items %v %v", res, err)
	}
	// A send resumes it in herdr (ompresume.go); this session's directory is gone, so it is refused before a tab.
	if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-1"}); !isRefusal(err, "not_running") {
		t.Fatalf("send: %v", err)
	}
	if got := f.trace(); len(got) != 0 {
		t.Fatalf("a stopped session asked herdr: %v", got)
	}
}

// The same live omp opened through a symlink to the session's directory still hides it: directories are compared as
// physical paths.
func TestOmpDiskHidesALivePaneThroughASymlink(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	id := "01a10000-0000-7000-8000-000000000008"
	writeOmpSession(t, ompSessionsDir(home), "-w-real", id, "live", real, time.Now())
	seedAgents(t, n, f, ompPaneAgent("w1:p8", link, ""))
	for _, s := range listSessions(t, n) {
		if s.SessionID == id {
			t.Fatalf("a live pane behind a symlink had its file listed as stopped: %+v", s)
		}
	}
	if _, ok := n.ompSession(id); ok {
		t.Fatal("a live pane behind a symlink had its file taken as stopped")
	}
}

// A pi pane in the same directory is not an omp: it never writes omp's session files, so the stopped omp stays listed.
func TestOmpDiskIgnoresAPiPane(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	cwd := t.TempDir()
	id := "01a10000-0000-7000-8000-000000000009"
	writeOmpSession(t, ompSessionsDir(home), "-w-pi", id, "stopped", cwd, time.Now())
	seedAgents(t, n, f, piPaneAgent("w1:p9", cwd, ""))
	onlyRow(t, listSessions(t, n), id)
	if _, ok := n.ompSession(id); !ok {
		t.Fatal("a pi pane hid a stopped omp session")
	}
}

// A live omp the node knows only by its fallback id (no extension, no herdr integration) hides its session's file:
// that file is the one the running process is writing, not a stopped session to resume.
func TestOmpDiskHidesALivePane(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	cwd := t.TempDir()
	id := "01a10000-0000-7000-8000-000000000007"
	writeOmpSession(t, ompSessionsDir(home), "-w-live", id, "live", cwd, time.Now())
	seedAgents(t, n, f, ompPaneAgent("w1:p7", cwd, ""))
	list := listSessions(t, n)
	onlyRow(t, list, otherSession(host.Agent{Agent: ompAgent, PaneID: "w1:p7"}))
	for _, s := range list {
		if s.SessionID == id {
			t.Fatalf("a live pane's file was listed as stopped: %+v", s)
		}
	}
	if _, ok := n.ompSession(id); ok {
		t.Fatal("a live pane's file was taken as resumable")
	}
}

// A scan that runs out of time keeps the heads it read, so the next scan reads only the files it has not seen: a slow
// disk gets through them in a few scans instead of stopping at the same file every time.
func TestOmpDiskSlowScanGoesOn(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	old := ompDiskTimeout
	ompDiskTimeout = time.Nanosecond // every scan runs out of time after its first read
	t.Cleanup(func() { ompDiskTimeout = old })
	n, _, home := herdrNode(t)
	now := time.Now()
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("01a10000-0000-7000-8000-00000000002%d", i)
		writeOmpSession(t, ompSessionsDir(home), fmt.Sprintf("-w-slow%d", i), id, "slow", "/w/slow", now.Add(-time.Duration(i)*time.Minute))
	}
	var rows []ompDiskSession
	for scan := 0; scan < 3 && len(rows) == 0; scan++ {
		n.omps().at = time.Time{}
		rows = n.ompSessions()
	}
	if len(rows) != 3 {
		t.Fatalf("three scans of a slow disk listed %d sessions, want 3", len(rows))
	}
	if r := n.omps().reads; r != 3 {
		t.Fatalf("read %d heads for 3 files", r)
	}
}

// An omp whose session id went missing again (its extension reconnecting, herdr's report gone) is back on its fallback
// id, but the node still knows which session that pane runs: its file is not listed as stopped.
func TestOmpDiskKeepsAPaneWhoseIDWentMissing(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	cwd := t.TempDir()
	id := "01a10000-0000-7000-8000-000000000031"
	file := writeOmpSession(t, ompSessionsDir(home), "-w-back", id, "live", cwd, time.Now().Add(-time.Minute))
	seedAgents(t, n, f)                                  // the node's first pass: no panes yet
	seedAgents(t, n, f, ompPaneAgent("w1:p31", cwd, "")) // the pane appears, listed by its fallback id
	seedAgents(t, n, f, ompPaneAgent("w1:p31", cwd, file))
	onlyRow(t, listSessions(t, n), id) // its session reported: the live row
	seedAgents(t, n, f, ompPaneAgent("w1:p31", cwd, ""))
	for _, s := range listSessions(t, n) {
		if s.SessionID == id && s.Status == proto.StatusOffline { // the pane keeps the session it ran (placement.go)
			t.Fatalf("the file a live pane runs was listed as stopped once its id went missing: %+v", s)
		}
	}
	if _, ok := n.ompSession(id); ok {
		t.Fatal("the file a live pane runs was taken as stopped once its id went missing")
	}
}

// A pane that was there when the node started hides its directory's rows while nothing says what it runs. Once it
// reports one session and then another, it was first seen then: the directory's older stopped sessions are listed
// again when its id goes missing, and the session it runs stays hidden.
func TestOmpDiskPaneOnAnotherSessionShowsTheOlderOnes(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	cwd := t.TempDir()
	sessions, old := ompSessionsDir(home), time.Now().Add(-time.Minute)
	stopped := "01a10000-0000-7000-8000-000000000041"
	first := "01a10000-0000-7000-8000-000000000042"
	second := "01a10000-0000-7000-8000-000000000043"
	writeOmpSession(t, sessions, "-w-s1", stopped, "stopped", cwd, old)
	f1 := writeOmpSession(t, sessions, "-w-s2", first, "first", cwd, old)
	f2 := writeOmpSession(t, sessions, "-w-s3", second, "second", cwd, old)
	seedAgents(t, n, f, ompPaneAgent("w1:p41", cwd, f1)) // there when the node started
	seedAgents(t, n, f, ompPaneAgent("w1:p41", cwd, f2)) // resumed another session in the same pane
	seedAgents(t, n, f, ompPaneAgent("w1:p41", cwd, ""))
	list := listSessions(t, n)
	onlyRow(t, list, stopped)
	onlyRow(t, list, first)
	for _, s := range list {
		if s.SessionID == second {
			t.Fatalf("the session the pane runs was listed as stopped: %+v", s)
		}
	}
}

// A custom agent directory omp and its extension use (PI_CODING_AGENT_DIR, whatever its name) is where the node
// lists stopped sessions from too, and a send to one of them gets the stopped-session refusal.
func TestOmpDiskCustomAgentDir(t *testing.T) {
	n, _, home := herdrNode(t)
	custom := filepath.Join(t.TempDir(), "my-omp-config")
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	id := "01a10000-0000-7000-8000-000000000051"
	writeOmpSession(t, filepath.Join(custom, "sessions"), "-w-custom", id, "custom", "/w/custom", time.Now())
	writeOmpSession(t, ompSessionsDir(home), "-w-default", "01a10000-0000-7000-8000-000000000052", "default", "/w/default", time.Now())
	rows := n.ompSessions()
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("rows from a custom agent directory: %+v", rows)
	}
	onlyRow(t, listSessions(t, n), id)
	// Its file is accepted for a resume (under the custom directory's sessions); its directory is gone, so no tab opens.
	if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi"}); !isRefusal(err, "not_running") {
		t.Fatalf("a send to a stopped session in a custom directory: %v", err)
	}
}

// Only the calls a stopped session answers look it up: any other call never scans omp's session files.
func TestOmpOfflineLookupOnlyForItsCalls(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	id := "01a10000-0000-7000-8000-000000000061"
	writeOmpSession(t, ompSessionsDir(home), "-w-calls", id, "stopped", "/w/calls", time.Now())
	scanned := func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.ompDisk != nil
	}
	_, _ = request(t, n, "session.keys", map[string]any{"session_id": id, "keys": "x"})
	if scanned() {
		t.Fatal("a key press scanned omp's session files")
	}
	if _, err := request(t, n, "session.items", map[string]any{"session_id": id}); err != nil {
		t.Fatal(err)
	}
	if !scanned() {
		t.Fatal("a page of a stopped session did not look it up")
	}
}
