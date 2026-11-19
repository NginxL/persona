package link

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/osproc"
)

const standInSSHDirEnv = "PERSONA_TEST_STANDIN_SSH_DIR"

func TestMain(m *testing.M) {
	if os.Getenv(standInSSHDirEnv) != "" && len(os.Args) > 2 && os.Args[1] == "-o" && os.Args[2] == "BatchMode=yes" {
		os.Exit(standInSSHMain(os.Args[1:]))
	}
	// A developer's own proxy must not carry these tests' dials (proxy_test.go sets one where it wants one).
	for _, name := range proxyEnv {
		_ = os.Unsetenv(name)
	}
	os.Exit(m.Run())
}

type standInRun struct {
	PID     int      `json:"pid"`
	Args    []string `json:"args"`
	Forward string   `json:"forward"`
	Port    string   `json:"port"`
	Dest    string   `json:"dest"`
}

func standInSSHMain(args []string) int {
	dir := os.Getenv(standInSSHDirEnv)
	rec := standInRun{PID: os.Getpid(), Args: append([]string(nil), args...)}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o":
			i++
		case "-L":
			if i+1 < len(args) {
				i++
				rec.Forward = args[i]
			}
		case "-p":
			if i+1 < len(args) {
				i++
				rec.Port = args[i]
			}
		case "--":
			if i+1 < len(args) {
				rec.Dest = args[i+1]
			}
			i = len(args)
		default:
			if !strings.HasPrefix(args[i], "-") && rec.Dest == "" {
				rec.Dest = args[i]
			}
		}
	}
	if b, err := json.Marshal(rec); err == nil {
		tmp := filepath.Join(dir, fmt.Sprintf(".run-%d-%d.tmp", time.Now().UnixNano(), rec.PID))
		final := filepath.Join(dir, fmt.Sprintf("run-%020d-%d.json", time.Now().UnixNano(), rec.PID))
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, final)
		}
	}
	modeBytes, _ := os.ReadFile(filepath.Join(dir, "mode"))
	mode := strings.TrimSpace(string(modeBytes))
	if mode == "" {
		mode = "forward"
	}
	switch mode {
	case "auth_refused":
		fmt.Fprintln(os.Stderr, "user@example.com: Permission denied (publickey).")
		return 255
	case "host_key":
		fmt.Fprintln(os.Stderr, "Host key verification failed.")
		return 255
	case "ssh_unreachable":
		fmt.Fprintln(os.Stderr, "ssh: connect to host example.com port 22: Operation timed out")
		return 255
	}
	localAddr, targetAddr, ok := splitForwardSpec(rec.Forward)
	if !ok {
		fmt.Fprintf(os.Stderr, "Bad local forwarding specification '%s'\n", rec.Forward)
		return 255
	}
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bind [%s]: %v\n", localAddr, err)
		return 255
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			return 255
		}
		switch mode {
		case "forward_refused":
			fmt.Fprintln(os.Stderr, "channel 2: open failed: administratively prohibited: open failed")
			_ = conn.Close()
		case "target_unreachable":
			fmt.Fprintln(os.Stderr, "channel 2: open failed: connect failed: Connection refused")
			_ = conn.Close()
		default:
			go pipeForwardConn(conn, targetAddr)
		}
	}
}

func pipeForwardConn(local net.Conn, targetAddr string) {
	remote, err := net.DialTimeout("tcp", targetAddr, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "channel 2: open failed: connect failed: %v\n", err)
		_ = local.Close()
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(remote, local)
		if tc, ok := remote.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, remote)
		if tc, ok := local.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
	_ = local.Close()
	_ = remote.Close()
}

// splitForwardSpec splits "127.0.0.1:<localPort>:<targetHost>:<targetPort>" (including bracketed IPv6 targets).
func splitForwardSpec(spec string) (localAddr, targetAddr string, ok bool) {
	const prefix = "127.0.0.1:"
	rest, found := strings.CutPrefix(spec, prefix)
	if !found {
		return "", "", false
	}
	localPort, target, found := strings.Cut(rest, ":")
	if !found || !validPort(localPort) || target == "" {
		return "", "", false
	}
	return prefix + localPort, target, true
}

type sshHarness struct {
	t   *testing.T
	dir string
	bin string
}

func newSSHHarness(t *testing.T) *sshHarness {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv(standInSSHDirEnv, dir)
	h := &sshHarness{t: t, dir: dir, bin: exe}
	h.setMode("forward")
	return h
}

func (h *sshHarness) setMode(mode string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, "mode"), []byte(mode+"\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *sshHarness) runs() []standInRun {
	h.t.Helper()
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	out := make([]standInRun, 0, len(names))
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(h.dir, name))
		if err != nil {
			continue
		}
		var r standInRun
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func waitCondition(t *testing.T, within time.Duration, desc string, ok func() bool) {
	t.Helper()
	if ok() {
		return
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(within)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if ok() {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", desc)
		}
	}
}

type sshHubConn struct {
	hello Message
	conn  *websocket.Conn
	ended chan struct{}
}

type sshTestHub struct {
	srv     *httptest.Server
	port    string
	target  string
	pin     string
	links   chan sshHubConn
	streams chan *websocket.Conn
}

func newSSHTestHub(t *testing.T, prefix string) *sshTestHub {
	t.Helper()
	h := &sshTestHub{
		links:   make(chan sshHubConn, 16),
		streams: make(chan *websocket.Conn, 16),
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prefix + "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case prefix + "/api/v1/auth/tokens":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer jp_pair" {
				http.Error(w, `{"error":"invalid pairing token"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"jd_ssh","role":"link"}`))
		case prefix + Path:
			if r.Header.Get("Authorization") != "Bearer jd_ssh" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			var hello Message
			if _, b, err := c.Read(r.Context()); err != nil || json.Unmarshal(b, &hello) != nil || hello.Type != "hello" {
				return
			}
			sendJSON(t, c, `{"type":"endpoints","hub_name":"home"}`)
			sendJSON(t, c, `{"type":"node","online":true}`)
			hc := sshHubConn{hello: hello, conn: c, ended: make(chan struct{})}
			h.links <- hc
			for {
				if _, _, err := c.Read(context.Background()); err != nil {
					close(hc.ended)
					return
				}
			}
		case prefix + StreamPath:
			if r.Header.Get("Authorization") != "Bearer jd_ssh" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: StreamCompression})
			if err != nil {
				return
			}
			h.streams <- ws
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	tcpAddr := srv.Listener.Addr().(*net.TCPAddr)
	h.srv = srv
	h.port = strconv.Itoa(tcpAddr.Port)
	h.target = net.JoinHostPort("127.0.0.1", h.port)
	h.pin = pinOf(srv)
	return h
}

func TestSSHParseAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		in, wantDest, wantPort, wantCanon, wantLogin string
	}{
		{"user@example.com", "user@example.com", "", "user@example.com", "ssh user@example.com"},
		{"user@example.com:2222", "user@example.com", "2222", "user@example.com:2222", "ssh -p 2222 user@example.com"},
		{"ssh://user@example.com:2200", "user@example.com", "2200", "user@example.com:2200", "ssh -p 2200 user@example.com"},
		{"my-hub-bastion", "my-hub-bastion", "", "my-hub-bastion", "ssh my-hub-bastion"},
		{"user@[2001:db8::1]:2222", "user@2001:db8::1", "2222", "user@[2001:db8::1]:2222", "ssh -p 2222 user@2001:db8::1"},
	} {
		dest, port, canon, err := parseSSHServer(tc.in)
		if err != nil || dest != tc.wantDest || port != tc.wantPort || canon != tc.wantCanon {
			t.Fatalf("%q -> (%q, %q, %q, %v), want (%q, %q, %q, nil)",
				tc.in, dest, port, canon, err, tc.wantDest, tc.wantPort, tc.wantCanon)
		}
		if got := SSHLoginCommand(tc.in); got != tc.wantLogin {
			t.Fatalf("SSHLoginCommand(%q) = %q, want %q", tc.in, got, tc.wantLogin)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=bad", "user@example.com extra", "ssh://user:secret@example.com", "example.com:99999"} {
		_, _, _, err := parseSSHServer(bad)
		var se *SSHError
		if !errors.As(err, &se) || se.Code != SSHInvalid {
			t.Fatalf("parseSSHServer(%q) = %v, want SSHInvalid", bad, err)
		}
	}

	loopLink := "persona://link?" + url.Values{
		"url": {"http://127.0.0.1:18660"}, "tls": {"18662"}, "fp": {"pin"}, "token": {"jp_1"}, "machine": {"laptop"},
	}.Encode()
	if target, always, ok := PairSSHDefaults(loopLink); !ok || target != "127.0.0.1:18662" || !always {
		t.Fatalf("loopback link defaults = (%q, %v, %v)", target, always, ok)
	}
	lanLink := "persona://link?" + url.Values{
		"url": {"http://192.0.2.10:18660"}, "tls": {"18662"}, "fp": {"pin"}, "token": {"jp_1"},
		"alt": {"https://hub.example.com:18662"},
	}.Encode()
	if target, always, ok := PairSSHDefaults(lanLink); !ok || target != "127.0.0.1:18662" || always {
		t.Fatalf("LAN link defaults = (%q, %v, %v)", target, always, ok)
	}
	proxyLink := "persona://link?" + url.Values{
		"url": {"https://hub.example.com/prefix"}, "token": {"jp_1"}, "machine": {"laptop"},
	}.Encode()
	if target, always, ok := PairSSHDefaults(proxyLink); !ok || target != "127.0.0.1:443" || always {
		t.Fatalf("proxy link defaults = (%q, %v, %v)", target, always, ok)
	}
}

func TestSSHPairLinkAndStreamsShareOneSSHProcess(t *testing.T) {
	ssh := newSSHHarness(t)
	hub := newSSHTestHub(t, "")
	node := newFakeNode(t)
	path := filepath.Join(t.TempDir(), "link.json")

	// Loopback pairing link whose HTTP port is unreachable directly on another computer; target 127.0.0.1:<TLS port>
	// comes from the pairing link's tls parameter.
	rawLink := "persona://link?" + url.Values{
		"url":     {"http://127.0.0.1:1"},
		"tls":     {hub.port},
		"fp":      {hub.pin},
		"hub":     {"h1"},
		"token":   {"jp_pair"},
		"machine": {"laptop"},
	}.Encode()

	// Pre-pair "Test connection" infers 127.0.0.1:<TLS port> and verifies the hub's pinned key over the tunnel.
	norm, err := TestSSH(context.Background(), SSH{Server: "user@example.com:2222", Always: true}, rawLink, nil, ssh.bin)
	if err != nil || norm.Target != hub.target {
		t.Fatalf("TestSSH = (%+v, %v), want target %s", norm, err, hub.target)
	}
	runsAfterTest := ssh.runs()
	if len(runsAfterTest) != 1 {
		t.Fatalf("TestSSH started %d ssh processes, want 1", len(runsAfterTest))
	}
	waitCondition(t, 3*time.Second, "TestSSH ssh cleanup", func() bool { return !osproc.AliveMine(runsAfterTest[0].PID) })

	// Pair through the SSH tunnel without passing Target explicitly.
	cfg, err := PairWithSSH(context.Background(), rawLink, "", "", path, &SSH{Server: "user@example.com:2222", Always: true}, ssh.bin)
	if err != nil {
		t.Fatalf("PairWithSSH: %v", err)
	}
	if cfg.Token != "jd_ssh" || cfg.SSH == nil || cfg.SSH.Server != "user@example.com:2222" || cfg.SSH.Target != hub.target || !cfg.SSH.Always {
		t.Fatalf("paired config = %+v, ssh = %+v", cfg, cfg.SSH)
	}
	runsAfterPair := ssh.runs()
	if len(runsAfterPair) != 2 {
		t.Fatalf("runs after pair = %d, want 2", len(runsAfterPair))
	}
	pairRun := runsAfterPair[1]
	if pairRun.Port != "2222" || pairRun.Dest != "user@example.com" || !strings.HasSuffix(pairRun.Forward, ":"+hub.target) ||
		!slices.Contains(pairRun.Args, "BatchMode=yes") || !slices.Contains(pairRun.Args, "-N") {
		t.Fatalf("unexpected ssh invocation: %+v", pairRun)
	}
	waitCondition(t, 3*time.Second, "PairWithSSH ssh cleanup", func() bool { return !osproc.AliveMine(pairRun.PID) })

	// Start Run: the link and all streams opened over it must reuse one single ssh process.
	ch := make(chan Status, 128)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, Options{
			Path:   path,
			Target: node.path,
			SSHBin: ssh.bin,
			Status: func(s Status) {
				select {
				case ch <- s:
				default:
				}
			},
		})
	}()
	t.Cleanup(func() { cancel(); <-done })

	st := waitFor(t, ch, func(s Status) bool {
		return s.State == "connected" && s.Kind == KindSSH && s.HubName == "home" && s.Node == "online"
	})
	if st.Address != "user@example.com:2222" || st.SSH == nil || !st.SSH.Always {
		t.Fatalf("connected status = %+v", st)
	}

	var hc sshHubConn
	select {
	case hc = <-hub.links:
	case <-time.After(5 * time.Second):
		t.Fatal("hub did not receive link")
	}
	if hc.hello.Address != "" {
		t.Fatalf("hello.Address over SSH = %q, want empty", hc.hello.Address)
	}

	// Open 3 Agent streams and round-trip distinct payloads through fakeNode.
	for i, payload := range []string{"alpha-payload\n", "bravo-payload\n", "charlie-payload\n"} {
		id := fmt.Sprintf("stream-%d", i+1)
		sendJSON(t, hc.conn, fmt.Sprintf(`{"type":"open","id":%q}`, id))
		var ws *websocket.Conn
		select {
		case ws = <-hub.streams:
		case <-time.After(5 * time.Second):
			t.Fatalf("stream %s did not arrive", id)
		}
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		nc := websocket.NetConn(sctx, ws, websocket.MessageBinary)
		greet := make([]byte, len(nodeHello))
		if _, err := io.ReadFull(nc, greet); err != nil || string(greet) != nodeHello {
			scancel()
			_ = nc.Close()
			t.Fatalf("stream %s greeting = %q (%v)", id, greet, err)
		}
		if _, err := nc.Write([]byte(payload)); err != nil {
			scancel()
			_ = nc.Close()
			t.Fatalf("stream %s write: %v", id, err)
		}
		echo := make([]byte, len(payload))
		if _, err := io.ReadFull(nc, echo); err != nil || string(echo) != payload {
			scancel()
			_ = nc.Close()
			t.Fatalf("stream %s echo = %q (%v)", id, echo, err)
		}
		_ = nc.Close()
		scancel()
	}
	if got := node.attachs.Load(); got != 3 {
		t.Fatalf("fakeNode attachs = %d, want 3", got)
	}

	runsDuringLink := ssh.runs()
	if len(runsDuringLink) != 3 {
		t.Fatalf("total ssh starts = %d (want 3: 1 test + 1 pair + 1 active link carrying all 3 streams)", len(runsDuringLink))
	}
	activePID := runsDuringLink[2].PID
	if !osproc.AliveMine(activePID) {
		t.Fatalf("active ssh process %d is not alive while link is up", activePID)
	}

	// Quit leaves no ssh process behind.
	cancel()
	<-done
	waitCondition(t, 3*time.Second, "quit ssh cleanup", func() bool { return !osproc.AliveMine(activePID) })
}

func TestSSHReconnectsWhenKilledAndLeavesNoProcessOnModeSwitchOrUnpair(t *testing.T) {
	ssh := newSSHHarness(t)
	hub := newSSHTestHub(t, "")
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		*c = Config{
			Hub:       "h1",
			Machine:   "laptop",
			FP:        hub.pin,
			Token:     "jd_ssh",
			Endpoints: []Endpoint{{URL: "https://127.0.0.1:" + hub.port, Kind: "lan"}},
			SSH:       &SSH{Server: "user@example.com", Target: hub.target, Always: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ch := make(chan Status, 128)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, Options{
			Path:      path,
			SSHBin:    ssh.bin,
			PairCheck: 30 * time.Millisecond,
			Status: func(s Status) {
				select {
				case ch <- s:
				default:
				}
			},
		})
	}()
	t.Cleanup(func() { cancel(); <-done })

	st1 := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind == KindSSH })
	runs1 := ssh.runs()
	if len(runs1) != 1 {
		t.Fatalf("expected 1 ssh process, got %d", len(runs1))
	}
	pid1 := runs1[0].PID

	// Kill the ssh child directly: Run notices, reports waiting, and reconnects with a fresh ssh process.
	if err := osproc.Kill(pid1); err != nil {
		t.Fatal(err)
	}
	st2 := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind == KindSSH && s.Conn > st1.Conn })
	runs2 := ssh.runs()
	if len(runs2) < 2 {
		t.Fatalf("expected a new ssh process after kill, got %d", len(runs2))
	}
	pid2 := runs2[len(runs2)-1].PID
	if pid2 == pid1 {
		t.Fatalf("reconnected with same pid %d", pid2)
	}

	// Switching to passive mode closes the SSH tunnel cleanly.
	if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ch, func(s Status) bool { return s.Mode == ModePassive })
	waitCondition(t, 3*time.Second, "passive mode ssh cleanup", func() bool { return !osproc.AliveMine(pid2) })

	// Switch back to active mode, then unpair by removing link.json: no ssh process stays behind.
	if _, err := SetMode(path, ModeActive, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ch, func(s Status) bool {
		return s.Mode == ModeActive && s.State == "connected" && s.Kind == KindSSH && s.Conn > st2.Conn
	})
	runs3 := ssh.runs()
	pid3 := runs3[len(runs3)-1].PID
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ch, func(s Status) bool { return s.State == "unpaired" })
	waitCondition(t, 3*time.Second, "unpair ssh cleanup", func() bool { return !osproc.AliveMine(pid3) })
}

// gatedDirectListener sits in front of targetAddr so tests can flip direct reachability on/off and count direct TCP
// dials while the hub itself remains reachable through the SSH tunnel.
type gatedDirectListener struct {
	url      string
	allowed  atomic.Bool
	attempts atomic.Int32
}

func newGatedDirectListener(t *testing.T, targetAddr string, startAllowed bool) *gatedDirectListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	g := &gatedDirectListener{url: "https://" + ln.Addr().String()}
	g.allowed.Store(startAllowed)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go g.serve(c, targetAddr)
		}
	}()
	return g
}

// serve counts c as an attempt only when it opens with a TLS record: other programs on this machine probe local
// ports too (a node's port scan, user agent persona-ports; docs/lessons/testing.md).
func (g *gatedDirectListener) serve(c net.Conn, targetAddr string) {
	first := make([]byte, 1)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, first); err != nil || first[0] != 0x16 {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	g.attempts.Add(1)
	if !g.allowed.Load() {
		_ = c.Close()
		return
	}
	pipeForwardConn(&replayConn{Conn: c, r: io.MultiReader(bytes.NewReader(first), c)}, targetAddr)
}

// replayConn is a connection whose first bytes were already read, put back in front of the rest. It embeds the
// net.Conn interface, not *net.TCPConn, whose WriteTo io.Copy would use around Read.
type replayConn struct {
	net.Conn
	r io.Reader
}

func (c *replayConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (c *replayConn) CloseWrite() error {
	if tc, ok := c.Conn.(*net.TCPConn); ok {
		return tc.CloseWrite()
	}
	return nil
}

func TestSSHRoutingOrderFallbackStickyReconnectAndDirectRecovery(t *testing.T) {
	ssh := newSSHHarness(t)
	hub := newSSHTestHub(t, "")
	direct := newGatedDirectListener(t, hub.target, true)
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		*c = Config{
			Hub:       "h1",
			Machine:   "laptop",
			FP:        hub.pin,
			Token:     "jd_ssh",
			Endpoints: []Endpoint{{URL: direct.url, Kind: "lan"}},
			SSH:       &SSH{Server: "user@example.com", Target: hub.target, Always: false},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: direct hub answers and Always is false -> direct wins without starting ssh; live SetSSH toggles routes.
	ch1 := make(chan Status, 128)
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_ = Run(ctx1, Options{
			Path:      path,
			SSHBin:    ssh.bin,
			PairCheck: 30 * time.Millisecond,
			Status: func(s Status) {
				select {
				case ch1 <- s:
				default:
				}
			},
		})
	}()
	st := waitFor(t, ch1, func(s Status) bool { return s.State == "connected" })
	if st.Kind != "lan" || st.Address != direct.url || len(ssh.runs()) != 0 {
		t.Fatalf("direct-first status = %+v, ssh runs = %d", st, len(ssh.runs()))
	}
	<-hub.links // drain first link

	// Turn Always on while connected: switches to SSH without re-pairing.
	if _, err := SetSSH(path, &SSH{Server: "user@example.com", Target: hub.target, Always: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ch1, func(s Status) bool { return s.State == "connected" && s.Kind == KindSSH })
	<-hub.links
	alwaysPID := ssh.runs()[len(ssh.runs())-1].PID

	// Turn Always off again while direct is reachable: switches back to direct and cleans up ssh.
	if _, err := SetSSH(path, &SSH{Server: "user@example.com", Target: hub.target, Always: false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ch1, func(s Status) bool { return s.State == "connected" && s.Kind == "lan" })
	<-hub.links
	waitCondition(t, 3*time.Second, "switch off Always ssh cleanup", func() bool { return !osproc.AliveMine(alwaysPID) })
	cancel1()
	<-done1

	// Phase 2: direct is down, Always is false -> tries direct first, falls back to SSH; reconnect goes straight to SSH
	// without trying direct first; once direct comes back, periodic DirectRetryEvery switches back to direct and closes ssh.
	direct.allowed.Store(false)
	direct.attempts.Store(0)
	ch2 := make(chan Status, 128)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		_ = Run(ctx2, Options{
			Path:             path,
			SSHBin:           ssh.bin,
			PairCheck:        30 * time.Millisecond,
			DirectRetryEvery: 300 * time.Millisecond,
			Status: func(s Status) {
				select {
				case ch2 <- s:
				default:
				}
			},
		})
	}()
	t.Cleanup(func() { cancel2(); <-done2 })

	stFallback := waitFor(t, ch2, func(s Status) bool { return s.State == "connected" && s.Kind == KindSSH })
	firstSSHLink := <-hub.links
	afterFallbackAttempts := direct.attempts.Load()
	if afterFallbackAttempts < 1 {
		t.Fatalf("direct was not tried before falling back to SSH (attempts = %d)", afterFallbackAttempts)
	}

	// Drop the hub's WebSocket immediately (before DirectRetryEvery fires) while direct is still down: reconnect must go
	// straight to SSH without dialing direct first.
	_ = firstSSHLink.conn.Close(websocket.StatusGoingAway, "restart")
	waitFor(t, ch2, func(s Status) bool {
		return s.State == "connected" && s.Kind == KindSSH && s.Conn > stFallback.Conn
	})
	_ = <-hub.links
	if got := direct.attempts.Load(); got != afterFallbackAttempts {
		t.Fatalf("reconnect after working SSH tried direct first: attempts went from %d to %d", afterFallbackAttempts, got)
	}
	stickyPID := ssh.runs()[len(ssh.runs())-1].PID

	// Bring direct back: DirectRetryEvery probes direct, reconnects over LAN, and closes the SSH tunnel.
	direct.allowed.Store(true)
	stRecovered := waitFor(t, ch2, func(s Status) bool { return s.State == "connected" && s.Kind == "lan" })
	if stRecovered.Address != direct.url {
		t.Fatalf("recovered direct address = %q, want %q", stRecovered.Address, direct.url)
	}
	waitCondition(t, 3*time.Second, "direct recovery ssh cleanup", func() bool { return !osproc.AliveMine(stickyPID) })
}

func TestSSHPreservesEndToEndTLSPinAndSystemTrust(t *testing.T) {
	ssh := newSSHHarness(t)
	hub := newSSHTestHub(t, "")

	wrongPinLink := "persona://link?" + url.Values{
		"url":     {"http://127.0.0.1:1"},
		"tls":     {hub.port},
		"fp":      {"not-the-hubs-pin"},
		"hub":     {"h1"},
		"token":   {"jp_pair"},
		"machine": {"laptop"},
	}.Encode()

	if _, err := TestSSH(context.Background(), SSH{Server: "user@example.com", Always: true}, wrongPinLink, nil, ssh.bin); !errors.Is(err, errPin) {
		t.Fatalf("TestSSH with wrong pin = %v, want errPin", err)
	}
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := PairWithSSH(context.Background(), wrongPinLink, "", "", path, &SSH{Server: "user@example.com", Always: true}, ssh.bin); !errors.Is(err, errPin) {
		t.Fatalf("PairWithSSH with wrong pin = %v, want errPin", err)
	}

	if err := Update(path, func(c *Config) error {
		*c = Config{
			Hub:       "h1",
			Machine:   "laptop",
			FP:        "not-the-hubs-pin",
			Token:     "jd_ssh",
			Endpoints: []Endpoint{{URL: "https://127.0.0.1:" + hub.port, Kind: "lan"}},
			SSH:       &SSH{Server: "user@example.com", Target: hub.target, Always: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ch := make(chan Status, 32)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, Options{
			Path:   path,
			SSHBin: ssh.bin,
			Status: func(s Status) {
				select {
				case ch <- s:
				default:
				}
			},
		})
	}()
	st := waitFor(t, ch, func(s Status) bool { return s.State == "waiting" })
	cancel()
	<-done
	if st.SSHError != nil || !strings.Contains(st.Error, errPin.Error()) {
		t.Fatalf("wrong pin status = %+v, want errPin without SSHError", st)
	}

	// Reverse-proxy hub (TrustSystem) over SSH tunnel: checks CA pool and certificate ServerName end to end.
	proxyHub := newSSHTestHub(t, "/prefix")
	restoreEmpty := UseRootsForTesting(x509.NewCertPool())
	proxyCfg := &Config{
		Hub:       "h1",
		Machine:   "laptop",
		Trust:     TrustSystem,
		Token:     "jd_ssh",
		Endpoints: []Endpoint{{URL: "https://example.com:" + proxyHub.port + "/prefix", Kind: "name"}},
		SSH:       &SSH{Server: "user@example.com", Target: proxyHub.target, Always: true},
	}
	if _, err := TestSSH(context.Background(), *proxyCfg.SSH, "", proxyCfg, ssh.bin); err == nil || !strings.Contains(err.Error(), "证书不受系统信任") {
		restoreEmpty()
		t.Fatalf("untrusted CA over SSH = %v", err)
	}
	restoreEmpty()

	testCA(t, proxyHub.srv)
	if _, err := TestSSH(context.Background(), *proxyCfg.SSH, "", proxyCfg, ssh.bin); err != nil {
		t.Fatalf("trusted CA + matching ServerName over SSH: %v", err)
	}
	wrongHostCfg := *proxyCfg
	wrongHostCfg.Endpoints = []Endpoint{{URL: "https://localhost:" + proxyHub.port + "/prefix", Kind: "name"}}
	if _, err := TestSSH(context.Background(), *wrongHostCfg.SSH, "", &wrongHostCfg, ssh.bin); err == nil || !strings.Contains(err.Error(), "证书上的域名对不上") {
		t.Fatalf("mismatched hostname over SSH = %v", err)
	}

	// A config with neither a pin nor Trust (Load refuses it, but nothing here should depend on that) is checked with
	// the system's CAs like a direct connection, never left unverified: the token must not reach an unknown server.
	restoreEmpty = UseRootsForTesting(x509.NewCertPool())
	defer restoreEmpty()
	bare := *proxyCfg
	bare.Trust = ""
	if l, err := dialSSH(context.Background(), &bare, ssh.bin); err == nil {
		l.closeNow()
		t.Fatal("dialSSH without pin or trust connected to a hub with an untrusted certificate")
	} else if !isCertError(err) {
		t.Fatalf("dialSSH without pin or trust = %v, want a certificate error", err)
	}
}

func TestSSHErrorSentences(t *testing.T) {
	ssh := newSSHHarness(t)
	cases := []struct {
		name      string
		mode      string
		bin       string
		server    string
		target    string
		wantCode  string
		wantSubEn string
	}{
		{
			name:      "no_ssh",
			mode:      "forward",
			bin:       filepath.Join(t.TempDir(), "does-not-exist-ssh"),
			server:    "user@example.com",
			target:    "127.0.0.1:18662",
			wantCode:  SSHNoBinary,
			wantSubEn: "no ssh command",
		},
		{
			name:      "auth_refused",
			mode:      "auth_refused",
			bin:       ssh.bin,
			server:    "user@example.com",
			target:    "127.0.0.1:18662",
			wantCode:  SSHAuthRefused,
			wantSubEn: "refused authentication",
		},
		{
			name:      "host_key",
			mode:      "host_key",
			bin:       ssh.bin,
			server:    "user@example.com:2222",
			target:    "127.0.0.1:18662",
			wantCode:  SSHHostKey,
			wantSubEn: "ssh -p 2222 user@example.com",
		},
		{
			name:      "forward_refused",
			mode:      "forward_refused",
			bin:       ssh.bin,
			server:    "user@example.com",
			target:    "127.0.0.1:18662",
			wantCode:  SSHForwardRefused,
			wantSubEn: "AllowTcpForwarding",
		},
		{
			name:      "target_unreachable",
			mode:      "target_unreachable",
			bin:       ssh.bin,
			server:    "user@example.com",
			target:    "127.0.0.1:18662",
			wantCode:  SSHTargetUnreachable,
			wantSubEn: "127.0.0.1:18662",
		},
	}

	seenEn := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ssh.setMode(tc.mode)
			_, err := TestSSH(context.Background(), SSH{Server: tc.server, Target: tc.target, Always: true}, "", nil, tc.bin)
			var se *SSHError
			if !errors.As(err, &se) || se.Code != tc.wantCode {
				t.Fatalf("TestSSH error = %v (type %T), want code %s", err, err, tc.wantCode)
			}
			enText := se.Text(i18n.En)
			zhText := se.Text(i18n.ZhHans)
			jaText := se.Text(i18n.Ja)
			if !strings.Contains(enText, tc.wantSubEn) {
				t.Fatalf("en sentence %q missing %q", enText, tc.wantSubEn)
			}
			for lang, txt := range map[string]string{"en": enText, "zh-Hans": zhText, "ja": jaText} {
				if txt == "" || strings.Contains(txt, "\n") {
					t.Fatalf("%s text %q must be a single non-empty line", lang, txt)
				}
			}
			if prev, dup := seenEn[enText]; dup {
				t.Fatalf("code %s produced same sentence as %s: %q", tc.wantCode, prev, enText)
			}
			seenEn[enText] = tc.wantCode

			// Run also attaches SSHError to Status while waiting.
			path := filepath.Join(t.TempDir(), "link.json")
			_ = Update(path, func(c *Config) error {
				*c = Config{
					Hub:       "h1",
					Machine:   "laptop",
					FP:        "pin",
					Token:     "jd_ssh",
					Endpoints: []Endpoint{{URL: "https://127.0.0.1:18662", Kind: "lan"}},
					SSH:       &SSH{Server: tc.server, Target: tc.target, Always: true},
				}
				return nil
			})
			ch := make(chan Status, 16)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = Run(ctx, Options{
					Path:   path,
					SSHBin: tc.bin,
					Status: func(s Status) {
						select {
						case ch <- s:
						default:
						}
					},
				})
			}()
			st := waitFor(t, ch, func(s Status) bool { return s.State == "waiting" })
			cancel()
			<-done
			if st.SSHError == nil || st.SSHError.Code != tc.wantCode {
				t.Fatalf("waiting status = %+v, want SSHError.Code %s", st, tc.wantCode)
			}
		})
	}
}
