package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// tabStartRE finds the file `personad tab-start` was given in the typed command.
var tabStartRE = regexp.MustCompile(` tab-start '?([^' ]+?)'?$`)

// ompExtensionIdle is an omp extension connected with no session open yet (nothing to resume).
func ompExtensionIdle(t *testing.T, rh *RuntimeHost, runtime string) *fakeRuntime {
	t.Helper()
	return dialRuntimeAt(t, rh.path, map[string]any{"runtime": runtime, "agent": "omp", "name": "omp",
		"caps": map[string]any{"send": true, "interrupt": true, "rename": true}})
}

// ompResumeHerdr answers the tab a resume opens: the start file's argv is recorded, and the extension lists the
// session once the command was typed (what a real omp does on its way up).
func ompResumeHerdr(t *testing.T, f *fakeHerdr, cwd, id string, rt *fakeRuntime) func() []string {
	t.Helper()
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": filepath.Base(cwd)}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	var mu sync.Mutex
	var argv []string
	var once sync.Once
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		m := tabStartRE.FindStringSubmatch(str(c.Params["text"]))
		if m == nil {
			return
		}
		b, _ := os.ReadFile(m[1])
		var file codexStartFile
		_ = json.Unmarshal(b, &file)
		mu.Lock()
		argv = file.Argv
		mu.Unlock()
		once.Do(func() {
			_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
				{"session_id": id, "status": "idle", "pane_id": "w1:p2", "cwd": cwd}}})
		})
	})
	return func() []string { mu.Lock(); defer mu.Unlock(); return argv }
}

// A stopped omp is resumed in a new tab with `omp --resume <file>`, and the phone's message goes to the extension
// once it lists the session again.
func TestOmpResumeFromDisk(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01b10000-0000-7000-8000-000000000001"
	file := writeOmpSession(t, ompSessionsDir(home), "-tmp-x", id, "resume me", cwd, time.Now())
	rt := ompExtensionIdle(t, rh, "omp-7") // connected, nothing listed yet
	rt.mu.Lock()
	rt.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return nil, errors.New("no such session") // what a runtime that does not know it answers (runtimes_calls.go)
		}
		return map[string]any{"ok": true}, nil
	}
	rt.mu.Unlock()
	argv := ompResumeHerdr(t, f, cwd, id, rt)

	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := argv(); strings.Join(got, " ") != "omp --resume "+file {
		t.Fatalf("ran %q, want omp --resume %s; typed %v", got, file, f.trace("pane.send_input"))
	}
	if len(rt.got("session.send")) != 1 {
		t.Fatalf("the message did not reach the extension: %d", len(rt.got("session.send")))
	}
}

// Two sends at once to a stopped omp start it once: the second waits for the first resume and goes to that omp.
func TestOmpResumeTwoSendsOneTab(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01b10000-0000-7000-8000-000000000009"
	writeOmpSession(t, ompSessionsDir(home), "-tmp-two", id, "twice", cwd, time.Now())
	rt := ompExtensionIdle(t, rh, "omp-9")
	rt.mu.Lock()
	rt.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return nil, errors.New("no such session")
		}
		return map[string]any{"ok": true}, nil
	}
	rt.mu.Unlock()
	ompResumeHerdr(t, f, cwd, id, rt)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-two-" + string(rune('a'+i))})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("two sends opened %d tabs: %v", len(got), f.trace())
	}
	if got := len(rt.got("session.send")); got != 2 {
		t.Fatalf("the extension got %d of the two messages", got)
	}
}

// stoppedOmpForStart is a stopped omp on disk with an idle extension connected, whose resume tab herdr then reports as
// that omp's pane (what herdr's omp integration does once omp writes its session file).
func stoppedOmpForStart(t *testing.T, id string) (*Node, *fakeHerdr, *fakeRuntime, string, *RuntimeHost) {
	t.Helper()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	file := writeOmpSession(t, ompSessionsDir(home), "-tmp-start", id, "started", cwd, time.Now())
	rt := ompExtensionIdle(t, rh, "omp-s"+id[len(id)-2:])
	rt.mu.Lock()
	rt.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return nil, errors.New("no such session")
		}
		return map[string]any{"ok": true}, nil
	}
	rt.mu.Unlock()
	ompResumeHerdr(t, f, cwd, id, rt)
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && tabStartRE.MatchString(str(c.Params["text"])) {
			f.reply("session.snapshot", snapshot(nil, nil, []any{ompPaneAgent("w1:p2", cwd, file)}, nil))
		}
	})
	return n, f, rt, cwd, rh
}

// The regression check of #10941's resume rule for omp: a session omp ran without Persona has no node record, but its
// session file says where it ran, and that is where `omp --resume` opens it. An extension's resume of one outside the
// health space (resume_outside_health, for one its computer does not list) goes on, as the owner's does; one whose
// file says the space is refused resume_unknown before herdr is asked anything.
func TestOmpAnExtensionsResumeReadsTheSessionFile(t *testing.T) {
	const id, inSpace = "01b10000-0000-7000-8000-000000000031", "01b10000-0000-7000-8000-000000000032"
	n, f, _, cwd, _ := stoppedOmpForStart(t, id)
	charts := mkdir(t, filepath.Join(identity.HealthDir(n.home), "charts"))
	writeOmpSession(t, ompSessionsDir(n.home), "-health-charts", inSpace, "charts", charts, time.Now())
	d := n.omps()
	d.mu.Lock()
	d.at = time.Time{} // scan again: the file above is newer than the last scan
	d.mu.Unlock()
	if s, ok := n.ompResumeSession(inSpace); !ok || s.Cwd != charts {
		t.Fatalf("the health space's omp session on disk: %+v, %v", s, ok)
	}
	if _, ok := n.state.get(id); ok {
		t.Fatal("the omp session has a record: not one Persona did not start")
	}
	start := func(id string) (map[string]any, error) {
		return request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp",
			"resume_session_id": id, "resume_outside_health": true})
	}
	if _, err := start(inSpace); rpcCode(err) != proto.CodeResumeUnknown {
		t.Fatalf("the extension's resume of an omp session in the health space: %v (code %q)", err, rpcCode(err))
	}
	if tabs := f.trace("tab.create"); len(tabs) != 0 {
		t.Fatalf("a refused resume opened a tab: %v", tabs)
	}
	res, err := start(id)
	if err != nil {
		t.Fatalf("the extension's resume of an omp session outside the space: %v", err)
	}
	if s, _ := res["session"].(map[string]any); s["session_id"] != id {
		t.Fatalf("resumed %v", res)
	}
}

// Two session.start calls resuming the same stopped omp start it once: the second waits on the session's recovery lock
// and finds it running.
func TestOmpStartResumeTwiceOneTab(t *testing.T) {
	id := "01b10000-0000-7000-8000-000000000021"
	n, f, _, cwd, _ := stoppedOmpForStart(t, id)
	var wg sync.WaitGroup
	res := make([]map[string]any, 2)
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp", "resume_session_id": id})
		}()
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if s, _ := res[i]["session"].(map[string]any); s["session_id"] != id {
			t.Fatalf("start %d answered %v", i, res[i])
		}
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("two starts opened %d tabs", len(got))
	}
}

// A session.start and a send racing for the same stopped omp start it once, and the message reaches that omp.
func TestOmpStartResumeRacingASendOneTab(t *testing.T) {
	id := "01b10000-0000-7000-8000-000000000022"
	n, f, rt, cwd, _ := stoppedOmpForStart(t, id)
	var wg sync.WaitGroup
	var startErr, sendErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, startErr = request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp", "resume_session_id": id})
	}()
	go func() {
		defer wg.Done()
		_, sendErr = request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-race"})
	}()
	wg.Wait()
	if startErr != nil || sendErr != nil {
		t.Fatalf("start %v, send %v", startErr, sendErr)
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("a start and a send opened %d tabs", len(got))
	}
	if got := len(rt.got("session.send")); got != 1 {
		t.Fatalf("the extension got %d messages, want the send's one", got)
	}
}

// leftExtension is an omp whose extension listed id while it ran and then left with it (omp exited): the node still
// knows which runtime listed the session, but that runtime is gone.
func leftExtension(t *testing.T, n *Node, rh *RuntimeHost, id, cwd string) {
	t.Helper()
	old := ompExtension(t, rh, "omp-gone", map[string]any{"session_id": id, "status": "idle", "pane_id": "w1:p9", "cwd": cwd})
	_ = old.conn.Close()
	waitFor(t, "the omp extension to leave", func() bool { return rh.reg.ownerOf(id) == nil })
	if h, _, known := n.runtimes.belongs(id); h != nil || !known {
		t.Fatalf("the left extension: connection %v, known %v", h, known)
	}
}

// A message to a stopped omp whose extension listed it and then left (the usual case: omp exited) resumes it from its
// file, rather than being refused as a runtime that is away.
func TestOmpResumeAfterItsExtensionLeft(t *testing.T) {
	id := "01b10000-0000-7000-8000-000000000031"
	n, f, rt, cwd, rh := stoppedOmpForStart(t, id)
	leftExtension(t, n, rh, id, cwd)
	if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-left"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("opened %d tabs", len(got))
	}
	if got := len(rt.got("session.send")); got != 1 {
		t.Fatalf("the resumed omp got %d messages", got)
	}
}

// session.start resuming a stopped omp whose extension left: resumed from its file, not refused as another tool's.
func TestOmpStartResumeAfterItsExtensionLeft(t *testing.T) {
	id := "01b10000-0000-7000-8000-000000000032"
	n, f, _, cwd, rh := stoppedOmpForStart(t, id)
	leftExtension(t, n, rh, id, cwd)
	res, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp", "resume_session_id": id})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if s, _ := res["session"].(map[string]any); s["session_id"] != id {
		t.Fatalf("start answered %v", res)
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("opened %d tabs", len(got))
	}
}

// A session.start resuming an omp with a prompt sends what came with it: its images and client_msg_id, so the same
// message sent again under that id is not delivered twice.
func TestOmpStartResumeSendsItsImagesAndClientMsgID(t *testing.T) {
	id := "01b10000-0000-7000-8000-000000000033"
	n, _, rt, cwd, _ := stoppedOmpForStart(t, id)
	images := []proto.Image{{Mime: "image/png", Data: "aW1hZ2U="}}
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp", "resume_session_id": id,
		"prompt": "look at this", "images": images, "client_msg_id": "m-start"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	sent := rt.got("session.send")
	if len(sent) != 1 {
		t.Fatalf("the resumed omp got %d messages", len(sent))
	}
	var got struct {
		Text        string        `json:"text"`
		Images      []proto.Image `json:"images"`
		ClientMsgID string        `json:"client_msg_id"`
	}
	_ = json.Unmarshal(sent[0], &got)
	if got.Text != "look at this" || len(got.Images) != 1 || got.Images[0].Data != images[0].Data || got.ClientMsgID != "m-start" {
		t.Fatalf("sent %s", sent[0])
	}
	if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "look at this", "client_msg_id": "m-start"}); err != nil {
		t.Fatalf("the same message again: %v", err)
	}
	if got := len(rt.got("session.send")); got != 1 {
		t.Fatalf("the same client_msg_id was delivered %d times", got)
	}
}

// Nothing to resume with: no file, a deleted one, or one outside the agent's sessions directory. Nothing is started.
func TestOmpResumeRefusals(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, _ := piHerdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	// A session listed from disk whose file is then deleted.
	id := "01b10000-0000-7000-8000-000000000002"
	file := writeOmpSession(t, ompSessionsDir(home), "-tmp-y", id, "gone", t.TempDir(), time.Now())
	_ = listSessions(t, n) // the scan caches the file
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi", "client_msg_id": "m-gone"})
	if code, _ := refusalOf(t, err); code != "omp_resume_unknown" {
		t.Fatalf("code %q (%v)", code, err)
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Fatalf("a tab was opened: %v", got)
	}

	// A file outside the agent's sessions directory, or with an apostrophe, is never run.
	root := filepath.Join(ompAgentDir(home), "sessions")
	for _, p := range []string{
		filepath.Join(root, "..", "x.jsonl"),
		filepath.Join(root, "a'b.jsonl"),
		filepath.Join(root, "a.txt"),
		"/etc/passwd",
	} {
		if ompResumePath(home, p) {
			t.Errorf("%s was taken as a resume", p)
		}
	}
	if !ompResumePath(home, filepath.Join(root, "-w", "t_uuid.jsonl")) {
		t.Error("a session file under the agent's sessions directory was refused")
	}
}

// The tab opened and no extension came: not_running, with the screen's last lines, and the wait is the pi resume's.
func TestOmpResumeWaitsThenRefuses(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	prev := piResumeWait
	piResumeWait = 300 * time.Millisecond
	t.Cleanup(func() { piResumeWait = prev })
	n, f, home, _ := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01b10000-0000-7000-8000-000000000004"
	writeOmpSession(t, ompSessionsDir(home), "-tmp-z", id, "quiet", cwd, time.Now())
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": filepath.Base(cwd)}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.setScreen("w1:p2", "omp: starting\n")
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi", "client_msg_id": "m-wait"})
	if code, _ := refusalOf(t, err); code != "not_running" {
		t.Fatalf("code %q (%v)", code, err)
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("tab.create %v", got)
	}
}

// herdr's own restore of the pane: an omp gets `omp --resume <file>` once its file is known, and no command at all
// (never `omp resume <pane>`) when it is not.
func TestOmpHerdrResumeReport(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home, _ := piHerdrNode(t)
	id := "01b10000-0000-7000-8000-000000000005"
	file := filepath.Join(ompAgentDir(home), "sessions", "-w", "t_"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"type":"session","id":"`+id+`","cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := agentLaunch{Agent: ompAgent, Launcher: ompAgent, ID: id}
	unknown := agentLaunch{Agent: ompAgent, Launcher: ompAgent, ID: "01b10000-0000-7000-8000-000000000006"}
	if argv, ok := n.agentResumeArgv(unknown, unknown.ID); ok {
		t.Fatalf("without a known file herdr was given %q", argv)
	}
	n.ompFiles = map[string]string{id: file}
	argv, ok := n.agentResumeArgv(spec, id)
	if !ok || argv[0] != "eval" || !strings.Contains(argv[1], "omp --resume "+file) {
		t.Fatalf("resume_argv %q %v", argv, ok)
	}
	if strings.Contains(argv[1], "omp resume") {
		t.Fatalf("the pane id was used as a session: %q", argv)
	}
}
