//go:build unix

// Command slowlinksoak reproduces #13677 end to end: a computer on Persona Link behind a slow, long link with a large
// Agent session full of screenshots, opened in a chat. It runs a throwaway hub (its own home directory, ports and
// Postgres cluster), a resident node and persona-link under a home directory of their own, a shaping proxy between
// Persona Link and the hub's TLS port, and an app connection that opens the session and reads it the way the web app
// does. It reports whether the computer stayed online, and how long the catch-up and the reads took
// (docs/operations/testing.md "Slow link soak").
//
//	cd server
//	go build -o <dir>/personad ./cmd/personad && go build -o <dir>/persona-link ./cmd/persona-link
//	go run ./internal/slowlinksoak -bin <dir> -label after
//
// Postgres comes from -pg (a directory with initdb, pg_ctl and postgres); nothing of the machine's own installation
// is read or written, and every process it starts gets an environment made here. Ports: -port-base hub HTTP, +1 hub
// TLS, +2 the shaping proxy, +3 Postgres.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/netshape"
	"github.com/nwverse/persona/server/internal/osproc"
)

type options struct {
	bin, nodeBin, pg, work, label string
	portBase                      int
	rate, queue                   int
	rtt                           time.Duration
	sessionMB                     float64
	shots                         int
	watch                         time.Duration
}

const (
	machine = "slowbox"
	session = "13677000-5eed-4000-8000-000000000001"
)

func main() {
	var o options
	flag.StringVar(&o.bin, "bin", "", "directory with the personad and persona-link under test")
	flag.StringVar(&o.nodeBin, "node-bin", "", "directory with the computer's personad and persona-link (default: -bin), e.g. a release")
	flag.StringVar(&o.pg, "pg", "", "directory with initdb, pg_ctl and postgres")
	flag.StringVar(&o.work, "work", "", "working directory (default: a new temporary one)")
	flag.StringVar(&o.label, "label", "run", "name of this run in the report")
	flag.IntVar(&o.portBase, "port-base", 18871, "first of four free ports")
	flag.IntVar(&o.rate, "rate", 100_000, "bytes a second each way through the shaping proxy")
	flag.DurationVar(&o.rtt, "rtt", 300*time.Millisecond, "round trip through the shaping proxy")
	flag.IntVar(&o.queue, "queue", 256<<10, "bytes the path buffers each way before a writer waits (socket buffers)")
	flag.Float64Var(&o.sessionMB, "session-mb", 5, "size of the session's transcript, mostly screenshots")
	flag.IntVar(&o.shots, "shots", 20, "screenshots in the transcript")
	flag.DurationVar(&o.watch, "watch", 3*time.Minute, "how long to watch after the session is opened")
	flag.Parse()
	if o.bin == "" || o.pg == "" {
		fmt.Fprintln(os.Stderr, "usage: slowlinksoak -bin <dir> -pg <dir> [-label <name>]")
		os.Exit(2)
	}
	if o.nodeBin == "" {
		o.nodeBin = o.bin
	}
	for _, p := range []*string{&o.bin, &o.nodeBin, &o.pg} { // the processes run in their own directories
		if abs, err := filepath.Abs(*p); err == nil {
			*p = abs
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := run(ctx, &o)
	if rep != nil {
		b, _ := json.MarshalIndent(rep, "", "  ")
		_ = os.WriteFile(filepath.Join(o.work, "report.json"), b, 0o600)
		fmt.Println(string(b))
	}
	if err != nil {
		fmt.Printf("RESULT %s: error: %v\n", o.label, err)
		os.Exit(1)
	}
	verdict := "stayed online"
	if rep.OfflineTransitions > 0 || rep.StreamDrops > 0 {
		verdict = fmt.Sprintf("went offline %d time(s), its connection dropped %d time(s)", rep.OfflineTransitions, rep.StreamDrops)
	}
	fmt.Printf("RESULT %s: %s; catch-up %s; items reads %d ok / %d failed, slowest %s\n", o.label, verdict,
		rep.CatchUp, rep.ReadsOK, rep.ReadsFailed, rep.SlowestRead)
}

// report is what one run found.
type report struct {
	Label              string   `json:"label"`
	Link               string   `json:"link"`
	Computer           string   `json:"computer_binaries"`
	SessionBytes       int64    `json:"session_bytes"`
	OfflineTransitions int      `json:"offline_transitions"`
	OfflineReasons     []string `json:"offline_reasons,omitempty"`
	PongTimeouts       int      `json:"pong_timeouts"`
	WatchTimeouts      int      `json:"watch_timeouts"`
	ConnectionsEnded   int      `json:"connections_ended"`
	StreamDrops        int      `json:"stream_drops"` // the node connection's stream ended under it (resumed or not)
	CatchUp            string   `json:"catch_up"`     // until the app had every item of the session
	CatchUpItems       int      `json:"catch_up_items"`
	ImageBytesPushed   int64    `json:"image_bytes_pushed"` // image data that reached the app inline
	ReadsOK            int      `json:"reads_ok"`
	ReadsFailed        int      `json:"reads_failed"`
	SlowestRead        string   `json:"slowest_read"`
	ProxyConnections   int64    `json:"proxy_connections"` // TCP connections Persona Link opened through the proxy
	UploadBytes        int64    `json:"upload_bytes"`      // computer to hub through the proxy, after the session was opened
	HubLog             []string `json:"hub_log_lines,omitempty"`
}

func say(format string, args ...any) {
	// In UTC: the machine's time zone is not read (internal/tzguard).
	fmt.Printf("%sZ slowlinksoak: %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func run(ctx context.Context, o *options) (*report, error) {
	if o.work == "" {
		dir, err := os.MkdirTemp("", "slowlinksoak-")
		if err != nil {
			return nil, err
		}
		o.work = dir
	}
	defer func() { say("logs and report in %s", o.work) }()
	rep := &report{Label: o.label, Computer: o.nodeBin, Link: fmt.Sprintf("%d B/s each way, RTT %s, %d KiB buffered", o.rate, o.rtt, o.queue>>10)}
	hubHome, nodeHome := filepath.Join(o.work, "hub-home"), filepath.Join(o.work, "node-home")
	for _, d := range []string{hubHome, nodeHome, filepath.Join(o.work, "tmp"), filepath.Join(o.work, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	var procs []*exec.Cmd
	defer func() {
		for i := len(procs) - 1; i >= 0; i-- {
			stopProcess(procs[i])
		}
	}()
	start := func(name string, home string, args ...string) (*exec.Cmd, error) {
		log, err := os.Create(filepath.Join(o.work, "logs", name+".log"))
		if err != nil {
			return nil, err
		}
		c := exec.Command(args[0], args[1:]...)
		c.Dir, c.Stdout, c.Stderr = home, log, log
		c.Env = env(o, home)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			return nil, err
		}
		procs = append(procs, c)
		return c, nil
	}

	// Postgres: a cluster of its own, on its own port, trust on loopback only.
	pgPort, hubPort, tlsPort, proxyPort := o.portBase+3, o.portBase, o.portBase+1, o.portBase+2
	pgData := filepath.Join(o.work, "pgdata")
	say("starting Postgres on %d", pgPort)
	if out, err := exec.CommandContext(ctx, filepath.Join(o.pg, "initdb"), "-D", pgData, "-U", "persona", "--auth=trust", "-E", "UTF8", "--no-locale").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("initdb: %v: %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, filepath.Join(o.pg, "pg_ctl"), "-D", pgData, "-l", filepath.Join(o.work, "logs", "pg.log"), "-w", "start",
		"-o", fmt.Sprintf("-p %d -c listen_addresses=127.0.0.1 -k %s", pgPort, filepath.Join(o.work, "tmp"))).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pg_ctl start: %v: %s", err, out)
	}
	defer func() { _ = exec.Command(filepath.Join(o.pg, "pg_ctl"), "-D", pgData, "-m", "fast", "stop").Run() }()

	// The hub: its TLS port is advertised only through the shaping proxy, so Persona Link reaches it that way.
	token := randomHex(24)
	hub := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	cfg := map[string]any{
		"listen": fmt.Sprintf("127.0.0.1:%d", hubPort), "token": token, "public_url": hub,
		"database_url": fmt.Sprintf("postgres://persona@127.0.0.1:%d/postgres?sslmode=disable", pgPort),
		"mesh":         map[string]any{"discovery": false},
		"public": map[string]any{"tls_listen": fmt.Sprintf("127.0.0.1:%d", tlsPort), "advertise": []string{fmt.Sprintf("https://127.0.0.1:%d", proxyPort)},
			"advertise_ipv6": false, "advertise_lan": false},
	}
	if err := writeJSON(filepath.Join(hubHome, ".persona", "hub.json"), cfg); err != nil {
		return nil, err
	}
	say("starting the hub on %s (TLS %d)", hub, tlsPort)
	if _, err := start("hub", hubHome, filepath.Join(o.bin, "personad"), "hub"); err != nil {
		return nil, err
	}
	api := &client{hub: hub, token: token}
	if err := waitFor(ctx, 90*time.Second, "the hub's health", func() bool { return api.get(ctx, "/api/v1/health", nil) == nil }); err != nil {
		return nil, err
	}

	// The shaping proxy: each connection Persona Link opens goes through a link of its own, shaped each way.
	shape := netshape.Config{Rate: float64(o.rate), Delay: o.rtt / 2, Queue: o.queue}
	stopProxy, err := proxy(ctx, fmt.Sprintf("127.0.0.1:%d", proxyPort), fmt.Sprintf("127.0.0.1:%d", tlsPort), shape)
	if err != nil {
		return nil, err
	}
	defer stopProxy()

	// The computer: a session's transcript, small for now, a resident node and Persona Link paired through the proxy.
	transcript := filepath.Join(nodeHome, ".claude", "projects", "-work", session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		return nil, err
	}
	if err := appendLines(transcript, userLine(0, "Take screenshots of the page as you go"), assistantLine(0, "Starting.")); err != nil {
		return nil, err
	}
	pair := exec.CommandContext(ctx, filepath.Join(o.bin, "personad"), "hub", "pair", "--machine", machine, "--add", "--name", "Slow box")
	pair.Dir, pair.Env = hubHome, env(o, hubHome)
	link, err := pair.Output()
	if err != nil {
		return nil, fmt.Errorf("hub pair: %v", err)
	}
	say("starting the resident node")
	if _, err := start("node", nodeHome, filepath.Join(o.nodeBin, "personad"), "node", "--attach"); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, 30*time.Second, "node-attach.sock", func() bool {
		_, err := os.Stat(filepath.Join(nodeHome, ".persona", "node-attach.sock"))
		return err == nil
	}); err != nil {
		return nil, err
	}
	pl := exec.CommandContext(ctx, filepath.Join(o.nodeBin, "persona-link"), "pair", "-")
	pl.Dir, pl.Env, pl.Stdin = nodeHome, env(o, nodeHome), strings.NewReader(strings.TrimSpace(string(link)))
	if out, err := pl.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("persona-link pair: %v: %s", err, out)
	}
	if err := onlyThrough(filepath.Join(nodeHome, ".persona", "link.json"), fmt.Sprintf("https://127.0.0.1:%d", proxyPort)); err != nil {
		return nil, err
	}
	say("starting Persona Link")
	if _, err := start("link", nodeHome, filepath.Join(o.nodeBin, "persona-link"), "run", "--log", filepath.Join(o.work, "logs", "link-run.log")); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, 3*time.Minute, "the computer online", func() bool { return api.online(ctx) == "" }); err != nil {
		return nil, err
	}
	say("online through the shaping proxy (%s)", rep.Link)

	// An app opens the session while it is small: the hub learns how far it has read, as it had before #13677.
	app, err := dialApp(ctx, hub, token)
	if err != nil {
		return nil, err
	}
	defer app.close()
	app.subscribe(ctx, 0)
	time.Sleep(3 * time.Second) // the watch is on
	// A line while it is open: its push tells the hub how far it has read (since_seq of the next watch).
	if err := appendLines(transcript, assistantLine(99, "Looking at the page now.")); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, 2*time.Minute, "the pushed line", func() bool { return app.count() >= 1 }); err != nil {
		return nil, err
	}
	app.unsubscribe(ctx)
	time.Sleep(2 * time.Second)

	// The agent works on with screenshots while nobody looks: the transcript grows to session-mb.
	per := int(o.sessionMB * (1 << 20) / float64(o.shots) * 3 / 4)
	var lines []string
	for i := 1; i <= o.shots; i++ {
		lines = append(lines, screenshotLine(i, per), assistantLine(i, fmt.Sprintf("Step %d done.", i)))
	}
	if err := appendLines(transcript, lines...); err != nil {
		return nil, err
	}
	if st, err := os.Stat(transcript); err == nil {
		rep.SessionBytes = st.Size()
	}
	want := 2 * o.shots // what came after the line the hub has
	say("the transcript is now %.1f MB; opening the session again", float64(rep.SessionBytes)/(1<<20))
	hubLogAt := fileSize(filepath.Join(o.work, "logs", "hub.log"))

	// Open it again, as the web app does: subscribe, then read what came after over HTTP every few seconds.
	opened := time.Now()
	connsAt, upAt := proxyConns.Load(), proxyUp.Load()
	app.reset()
	app.subscribe(ctx, 0)
	reads := &readStats{}
	readCtx, stopReads := context.WithCancel(ctx)
	defer stopReads()
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for readCtx.Err() == nil {
			began := time.Now()
			err := api.do(readCtx, http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/%s/items?after_seq=2&lazy_images=1", machine, session), nil, 45*time.Second)
			reads.add(time.Since(began), err)
			select {
			case <-readCtx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}()
	// Watch the computer's state.
	deadline := time.Now().Add(o.watch)
	wasOnline := true
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		case <-time.After(time.Second):
		}
		reason := api.online(ctx)
		if reason != "" && wasOnline {
			rep.OfflineTransitions++
			rep.OfflineReasons = append(rep.OfflineReasons, fmt.Sprintf("+%s %s", time.Since(opened).Round(time.Second), reason))
			say("offline: %s", reason)
		}
		wasOnline = reason == ""
		if rep.CatchUp == "" && app.count() >= want {
			rep.CatchUp = time.Since(opened).Round(100 * time.Millisecond).String()
			say("the app has every item after %s", rep.CatchUp)
		}
	}
	stopReads()
	readers.Wait()
	if rep.CatchUp == "" {
		rep.CatchUp = fmt.Sprintf("incomplete (%d of %d items)", app.count(), want)
	}
	rep.CatchUpItems = app.count()
	rep.ProxyConnections, rep.UploadBytes = proxyConns.Load()-connsAt, proxyUp.Load()-upAt
	rep.ImageBytesPushed = app.imageBytes.Load()
	rep.ReadsOK, rep.ReadsFailed, rep.SlowestRead = reads.ok, reads.failed, reads.slowest.Round(100*time.Millisecond).String()
	for _, l := range linesFrom(filepath.Join(o.work, "logs", "hub.log"), hubLogAt) {
		switch {
		case strings.Contains(l, "no pong within"):
			rep.PongTimeouts++
		case strings.Contains(l, "session.watch") && strings.Contains(l, "deadline exceeded"):
			rep.WatchTimeouts++
		case strings.Contains(l, "connection ended"):
			rep.ConnectionsEnded++
		case strings.Contains(l, "connection over link lost") || strings.Contains(l, "node exited"):
			rep.StreamDrops++
		default:
			continue
		}
		if len(rep.HubLog) < 20 {
			rep.HubLog = append(rep.HubLog, l)
		}
	}
	return rep, nil
}

// env is the whole environment of a process this starts: nothing of the machine's own installation reaches it.
func env(o *options, home string) []string {
	return []string{"HOME=" + home, "PERSONA_HOME=" + filepath.Join(home, ".persona"), "TMPDIR=" + filepath.Join(o.work, "tmp"),
		"PATH=" + o.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8", "USER=" + os.Getenv("USER"),
		"PERSONA_HUB_EDGE=0", "PERSONA_SPLIT_PROGRAMS=0", "PERSONA_LOCAL_NODE=0"}
}

func stopProcess(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

// proxy forwards every connection to listen on to target through a link shaped by cfg each way.
var proxyConns, proxyUp atomic.Int64

func proxy(ctx context.Context, listen, target string, cfg netshape.Config) (func(), error) {
	// Through osproc: this tool starts hub and node processes while the proxy accepts, and a connection accepted with
	// plain net would be inherited by them on macOS (#7765).
	ln, err := osproc.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			proxyConns.Add(1)
			go func() {
				defer c.Close()
				up, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
				if err != nil {
					return
				}
				defer up.Close()
				near, far := netshape.Pipe(cfg, cfg)
				defer near.Close()
				defer far.Close()
				done := make(chan struct{}, 4)
				pump := func(dst io.Writer, src io.Reader, closeDst func()) {
					_, _ = io.Copy(dst, src)
					closeDst()
					done <- struct{}{}
				}
				go pump(near, countingReader{c}, func() { _ = near.Close() })
				go pump(c, near, func() { _ = c.Close() })
				go pump(up, far, func() { _ = up.Close() })
				go pump(far, up, func() { _ = far.Close() })
				<-done
			}()
		}
	}()
	return func() { _ = ln.Close() }, nil
}

type countingReader struct{ r io.Reader }

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	proxyUp.Add(int64(n))
	return n, err
}

// onlyThrough leaves the address through the proxy as the link's only candidate (link.Config.Endpoints).
func onlyThrough(path, address string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	cfg["endpoints"] = []map[string]any{{"url": address, "kind": "ipv4", "manual": true}}
	return writeJSON(path, cfg)
}

type client struct{ hub, token string }

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, out, 20*time.Second)
}

func (c *client) do(ctx context.Context, method, path string, out any, within time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.hub+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %.200s", method, path, res.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// online is "" while the computer is online, else why not.
func (c *client) online(ctx context.Context) string {
	var res struct {
		Machines []struct {
			ID     string `json:"id"`
			Online bool   `json:"online"`
			Error  string `json:"error"`
		} `json:"machines"`
	}
	if err := c.get(ctx, "/api/v1/machines", &res); err != nil {
		return "machines: " + err.Error()
	}
	for _, m := range res.Machines {
		if m.ID == machine {
			if m.Online {
				return ""
			}
			if m.Error == "" {
				return "offline"
			}
			return m.Error
		}
	}
	return "not listed"
}

// app is an app connection to the hub's WebSocket that has the session open.
type app struct {
	c          *websocket.Conn
	mu         sync.Mutex
	seen       map[string]bool
	imageBytes atomic.Int64
}

func dialApp(ctx context.Context, hub, token string) (*app, error) {
	url := "ws" + strings.TrimPrefix(hub, "http") + "/api/v1/ws"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(-1)
	a := &app{c: c, seen: map[string]bool{}}
	go func() {
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var m struct {
				Type      string `json:"type"`
				SessionID string `json:"session_id"`
				Items     []struct {
					ID    string `json:"id"`
					Image *struct {
						Data string `json:"data"`
					} `json:"image"`
				} `json:"items"`
			}
			if json.Unmarshal(data, &m) != nil || m.Type != "items" || m.SessionID != session {
				continue
			}
			a.mu.Lock()
			for _, it := range m.Items {
				a.seen[it.ID] = true
				if it.Image != nil {
					a.imageBytes.Add(int64(len(it.Image.Data)))
				}
			}
			a.mu.Unlock()
		}
	}()
	return a, nil
}

func (a *app) subscribe(ctx context.Context, since int) {
	msg := fmt.Sprintf(`{"type":"subscribe","machine_id":%q,"session_id":%q,"lazy_images":true,"fold_work":false}`, machine, session)
	_ = a.c.Write(ctx, websocket.MessageText, []byte(msg))
}

func (a *app) unsubscribe(ctx context.Context) {
	_ = a.c.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"type":"unsubscribe","machine_id":%q,"session_id":%q}`, machine, session)))
}

func (a *app) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.seen)
}

func (a *app) reset() {
	a.mu.Lock()
	a.seen = map[string]bool{}
	a.mu.Unlock()
	a.imageBytes.Store(0)
}

func (a *app) close() { _ = a.c.CloseNow() }

type readStats struct {
	mu          sync.Mutex
	ok, failed  int
	slowest     time.Duration
	lastFailure string
}

func (r *readStats) add(d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		r.failed++
		r.lastFailure = err.Error()
	} else {
		r.ok++
	}
	r.slowest = max(r.slowest, d)
}

func userLine(i int, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u%d","timestamp":"2026-11-23T09:%02d:00Z","cwd":"/work","sessionId":%q,"message":{"role":"user","content":%q}}`, i, i%60, session, text)
}

func assistantLine(i int, text string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a%d","timestamp":"2026-11-23T09:%02d:30Z","cwd":"/work","sessionId":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, i, i%60, session, text)
}

// screenshotLine is a user line with one image of size bytes, as a computer-use tool's result writes.
func screenshotLine(i, size int) string {
	raw := make([]byte, size)
	_, _ = rand.Read(raw) // screenshots do not compress much either
	data := base64.StdEncoding.EncodeToString(raw)
	return fmt.Sprintf(`{"type":"user","uuid":"s%d","timestamp":"2026-11-23T09:%02d:10Z","cwd":"/work","sessionId":%q,"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}}`, i, i%60, session, data)
}

func appendLines(path string, lines ...string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, l := range lines {
		if _, err := w.WriteString(l + "\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func waitFor(ctx context.Context, within time.Duration, what string, ok func() bool) error {
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", within, what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// linesFrom is the lines of path from offset on.
func linesFrom(path string, offset int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}
