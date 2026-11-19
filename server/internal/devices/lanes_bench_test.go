package devices

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The lane experiment of #10660. One computer's node connection runs over a slow link (netshape) while it carries a
// file.chunk reply of a whole chunk; meanwhile the hub makes a small interactive call every second and runs its real
// heartbeat (measureNodes, pingWait, pingAnswered) every few seconds. Without lanes the reply holds the node's single
// writer for the whole transfer, and the interactive replies and the pongs wait behind it. One summary line per mode
// is what lane framing is judged by.

// laneBench is one run's link and load.
type laneBench struct {
	link             netshape.Config // both ways
	size             int             // bytes of the transfer
	interactiveEvery time.Duration
	measureEvery     time.Duration // the heartbeat's period here (pingInterval is 30 s; pingWait stays as it is)
	tail             int           // interactive calls made after the transfer ended
}

// laneBenchInteractiveWait bounds one interactive call, as an app's read would be.
const laneBenchInteractiveWait = 30 * time.Second

// limit bounds the transfer: three minutes, or three times what its bytes take at the link's rate (base64 in JSON).
func (b laneBench) limit() time.Duration {
	limit := 3 * time.Minute
	if b.link.Rate > 0 {
		limit = max(limit, 3*time.Duration(float64(b.size)*4/3/b.link.Rate*float64(time.Second)))
	}
	return limit
}

// fromEnv takes PERSONA_LANE_BENCH_RATE (bytes a second), _DELAY (one way, a Go duration), _QUEUE and _SIZE (bytes).
func (b *laneBench) fromEnv(t *testing.T) {
	t.Helper()
	if v := os.Getenv("PERSONA_LANE_BENCH_RATE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			t.Fatalf("PERSONA_LANE_BENCH_RATE=%q: want bytes a second", v)
		}
		b.link.Rate = f
	}
	if v := os.Getenv("PERSONA_LANE_BENCH_DELAY"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			t.Fatalf("PERSONA_LANE_BENCH_DELAY=%q: want a one-way duration such as 200ms", v)
		}
		b.link.Delay = d
	}
	for _, e := range []struct {
		name string
		to   *int
	}{{"PERSONA_LANE_BENCH_QUEUE", &b.link.Queue}, {"PERSONA_LANE_BENCH_SIZE", &b.size}} {
		if v := os.Getenv(e.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				t.Fatalf("%s=%q: want bytes", e.name, v)
			}
			*e.to = n
		}
	}
}

// laneBenchResult is what one run saw. Pings are measureNodes rounds that sent one: answered in time; timed out but
// excused (pingAnswered cleared it: activity such as a large frame still arriving explains a late pong); missed
// (counted towards pingMissLimit); or unjudged (timed out without counting either way: never written in time, or the
// writer took most of its wait). Closes are the heartbeat closing the connection.
type laneBenchResult struct {
	transfer    time.Duration
	transferErr error

	mu                  sync.Mutex
	rtts                []time.Duration // every interactive call, a timed-out one at its wait
	timeouts, failures  int
	pings, answered     int
	excused, misses     int
	unjudged, pingFails int
	pingMax             time.Duration
	closes, reconnects  int
}

// percentile is the nearest-rank p-th percentile of sorted.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max(0, (len(sorted)*p+99)/100-1)]
}

// summary is the run's one parsable line.
func (r *laneBenchResult) summary(b laneBench, mode string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rtts := slices.Clone(r.rtts)
	slices.Sort(rtts)
	ms := func(d time.Duration) int64 { return d.Milliseconds() }
	terr := "none"
	if r.transferErr != nil {
		terr = strconv.Quote(oneLineErr(r.transferErr))
	}
	queue := b.link.Queue
	if queue <= 0 {
		queue = netshape.DefaultQueue
	}
	return fmt.Sprintf("lanebench mode=%s rate=%.0f rtt=%s queue=%d size=%d transfer=%.1fs transfer_err=%s "+
		"interactive_n=%d timeouts=%d failures=%d p50=%dms p95=%dms max=%dms "+
		"pings=%d answered=%d excused=%d misses=%d unjudged=%d ping_fails=%d ping_max=%dms closes=%d reconnects=%d",
		mode, b.link.Rate, 2*b.link.Delay, queue, b.size, r.transfer.Seconds(), terr,
		len(rtts), r.timeouts, r.failures, ms(percentile(rtts, 50)), ms(percentile(rtts, 95)), ms(percentile(rtts, 100)),
		r.pings, r.answered, r.excused, r.misses, r.unjudged, r.pingFails, ms(r.pingMax), r.closes, r.reconnects)
}

// benchLog keeps the manager's log lines: the heartbeat's verdicts are read from them.
type benchLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *benchLog) add(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *benchLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *benchLog) since(i int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines[i:])
}

func (l *benchLog) count(sub string) int {
	n := 0
	for _, line := range l.since(0) {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

// laneBenchMachine runs a machine whose every connection is a fresh shaped pipe to a fake node: node.info, an empty
// session list, pongs, a file.chunk of data, and {"ok":true} for anything else (session.info, the interactive call).
func laneBenchMachine(t *testing.T, d *Devices, b laneBench, lanes bool, data []byte) *Machine {
	t.Helper()
	m := newMachine(d, MachineConfig{ID: "slow", Name: "Slow", Kind: "ssh"})
	var mu sync.Mutex
	var conns []net.Conn
	m.dialNode = func(context.Context) (net.Conn, error) {
		managerSide, nodeSide := netshape.Pipe(b.link, b.link)
		p := rpc.New("n", nodeSide)
		caps := slices.Clone(nodeproto.Required) // what this manager cannot drive a node without
		if lanes {                               // as a node does (node.Serve)
			p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
			caps = append(caps, nodeproto.NodeLanes)
		}
		p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "node.info":
				// A Claude version, or the manager keeps asking node.info again for one (retryClaudeVersion).
				return proto.NodeInfo{Hostname: "slow", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test", ClaudeVersion: "test",
					Protocol: nodeproto.Version, ProtocolMin: nodeproto.MinManagerVersion, Capabilities: caps}, nil
			case "sessions.list":
				return map[string]any{"sessions": []proto.Session{}}, nil
			case "ping":
				return map[string]any{}, nil
			case "file.chunk":
				var req proto.FileChunkRequest
				_ = json.Unmarshal(params, &req)
				return proto.FileChunk{Path: req.Path, Name: "bench.bin", Size: int64(len(data)), Revision: "r1", Offset: req.Offset, Data: data}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
		mu.Lock()
		conns = append(conns, managerSide, nodeSide)
		mu.Unlock()
		return managerSide, nil
	}
	t.Cleanup(func() { // after the machine's run has stopped (runMachine's cleanup runs first)
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	d.addEntry(m)
	runMachine(t, m)
	// Connected and listed: the handshake's round trips are over before the load starts.
	eventually(t, time.Minute, "slow connected", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.peer != nil && m.online && m.sessionsListed
	})
	return m
}

// measure runs one heartbeat round and sorts out what it made of the ping. Only this measures, so the ping's log line
// (logCall: a failed or slow ping) and the change in the machine's ping state across the round are this ping's.
func (r *laneBenchResult) measure(ctx context.Context, d *Devices, m *Machine, logs *benchLog) string {
	m.mu.Lock()
	judged, misses, peer := m.pingJudged, m.pingMisses, m.pingPeer
	m.mu.Unlock()
	mark := logs.len()
	start := time.Now()
	d.measureNodes(ctx)
	took := time.Since(start)
	m.mu.Lock()
	judged2, misses2, peer2 := m.pingJudged, m.pingMisses, m.pingPeer
	m.mu.Unlock()
	result, closed := "", false
	for _, line := range logs.since(mark) {
		if strings.Contains(line, " rpc call method=ping ") {
			if _, rest, ok := strings.Cut(line, " result="); ok {
				result, _, _ = strings.Cut(rest, " ")
			}
		}
		closed = closed || strings.Contains(line, "pings in a row")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if result == "" && judged2.Equal(judged) {
		return "skipped (offline)"
	}
	r.pings++
	r.pingMax = max(r.pingMax, took)
	verdict := ""
	switch {
	case result == "" || result == callOK:
		r.answered++
		verdict = "answered"
	case result != callDeadline:
		r.pingFails++
		verdict = "failed (" + result + ")"
	case judged2.Equal(judged):
		r.unjudged++
		verdict = "timed out, unjudged"
	case closed || misses2 > 0 && (peer2 != peer || misses2 > misses):
		r.misses++
		verdict = "timed out, missed"
	default:
		r.excused++
		verdict = "timed out, excused"
	}
	if closed {
		verdict += ", connection closed"
	}
	return fmt.Sprintf("%s in %s", verdict, took.Round(time.Millisecond))
}

// runLaneBench runs the transfer, the interactive calls and the heartbeat once.
func runLaneBench(t *testing.T, b laneBench, lanes bool) *laneBenchResult {
	t.Helper()
	d := newTestDevices(t)
	logs := &benchLog{}
	quiet := d.logf
	d.logf = func(format string, args ...any) { // set before anything runs
		logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	data := make([]byte, b.size)
	_, _ = rand.Read(data)
	m := laneBenchMachine(t, d, b, lanes, data)
	ctx := t.Context()
	r := &laneBenchResult{}

	start := time.Now()
	transferDone := make(chan struct{})
	go func() {
		defer close(transferDone)
		cctx, cancel := context.WithTimeout(ctx, b.limit())
		defer cancel()
		var out proto.FileChunk
		err := m.call(cctx, "file.chunk", proto.FileChunkRequest{Path: "/home/me/bench.bin", Length: b.size}, &out)
		if err == nil && !bytes.Equal(out.Data, data) {
			err = errors.New("the data changed on the way")
		}
		r.mu.Lock()
		r.transfer, r.transferErr = time.Since(start), err
		r.mu.Unlock()
	}()

	stopMeasure, measured := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(measured)
		tick := time.NewTicker(b.measureEvery)
		defer tick.Stop()
		for {
			select {
			case <-stopMeasure:
				return
			case <-tick.C:
			}
			at := time.Since(start)
			verdict := r.measure(ctx, d, m, logs)
			t.Logf("ping at %4.1fs: %s", at.Seconds(), verdict)
		}
	}()

	var calls sync.WaitGroup
	tick := time.NewTicker(b.interactiveEvery)
	defer tick.Stop()
	for after := 0; after < b.tail; {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("the test ended")
		}
		select {
		case <-transferDone:
			after++
		default:
		}
		calls.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, laneBenchInteractiveWait)
			defer cancel()
			begin := time.Now()
			err := m.call(cctx, "session.info", map[string]any{"session_id": "bench"}, nil)
			took := time.Since(begin)
			r.mu.Lock()
			defer r.mu.Unlock()
			r.rtts = append(r.rtts, took)
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				r.timeouts++
			case err != nil:
				r.failures++
				t.Logf("interactive call after %s: %v", took.Round(time.Millisecond), err)
			}
		})
	}
	calls.Wait()
	close(stopMeasure)
	<-measured
	r.mu.Lock()
	r.closes = logs.count("pings in a row")
	r.reconnects = max(0, logs.count("] connected over ")-1)
	r.mu.Unlock()
	return r
}

// TestLaneBench measures the slow link of #10660 (30 KB/s, 400 ms round trip, a 1 MiB chunk) with lanes off and on.
// It takes minutes: PERSONA_LANE_BENCH=1 runs it, PERSONA_LANE_BENCH_OUT=<file> appends the summary lines there,
// and PERSONA_LANE_BENCH_RATE / _DELAY / _QUEUE / _SIZE change the link and the transfer.
func TestLaneBench(t *testing.T) {
	if os.Getenv("PERSONA_LANE_BENCH") != "1" {
		t.Skip("the lane experiment takes minutes: PERSONA_LANE_BENCH=1 runs it")
	}
	b := laneBench{link: netshape.Config{Rate: 30_000, Delay: 200 * time.Millisecond, Queue: 64 << 10}, size: 1 << 20,
		interactiveEvery: time.Second, measureEvery: 5 * time.Second, tail: 3}
	b.fromEnv(t)
	for _, lanes := range []bool{false, true} {
		t.Run(onOff(lanes), func(t *testing.T) {
			line := runLaneBench(t, b, lanes).summary(b, onOff(lanes))
			t.Log(line)
			if out := os.Getenv("PERSONA_LANE_BENCH_OUT"); out != "" {
				f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := fmt.Fprintln(f, line); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestLaneBenchShort is the experiment scaled down to seconds, lanes on: a chunk in flight neither gets the connection
// closed nor holds an interactive call for long. Without lanes the transfer takes about 7 s and the interactive calls
// made meanwhile wait for its end (p95 near 7 s); with them they take about a round trip.
func TestLaneBenchShort(t *testing.T) {
	b := laneBench{link: netshape.Config{Rate: 100_000, Delay: 50 * time.Millisecond, Queue: 64 << 10}, size: 512 << 10,
		interactiveEvery: 200 * time.Millisecond, measureEvery: time.Second, tail: 3}
	r := runLaneBench(t, b, true)
	t.Log(r.summary(b, "on"))
	r.mu.Lock()
	defer r.mu.Unlock()
	rtts := slices.Clone(r.rtts)
	slices.Sort(rtts)
	if r.transferErr != nil {
		t.Errorf("transfer: %v", r.transferErr)
	}
	if r.closes > 0 || r.reconnects > 0 {
		t.Errorf("the heartbeat closed the connection %d time(s), %d reconnect(s)", r.closes, r.reconnects)
	}
	if p95 := percentile(rtts, 95); p95 > 2*time.Second {
		t.Errorf("interactive p95 %s with a chunk in flight, want under 2s", p95)
	}
}

// TestLaneBenchBothWays is the experiment with the other way busy too: the node keeps sending 96 KiB events to the
// manager as fast as the link takes them, while the manager sends a forwarded port's bytes to the node (24 KiB
// stream.data through its notes queue, three outstanding). The acknowledgements of the manager's frames then wait behind
// the node's events on the way back: judged by the round trip, the window shrank to a fraction of the link (fix-1 of
// #10660's review). Then the node's events stop, as if a burst of them ended, and for 25 s the manager's interactive
// calls and pings show how soon they wait as on a quiet link again (its second review). One line per queue and mode:
// what reached the node after warmUp and over the whole run, the longest a note took from its queue to the node; then,
// p50/p95/max ms, interactive calls while the node's queue drains, in the first second after, the 3 s after that and
// the last 10 s, the port's notes after it drained and in the last 10 s, the heartbeat rounds after it drained; and
// the connections the notes' stall check closed.
func TestLaneBenchBothWays(t *testing.T) {
	if os.Getenv("PERSONA_LANE_BENCH") != "1" {
		t.Skip("the lane experiment takes minutes: PERSONA_LANE_BENCH=1 runs it")
	}
	for _, queue := range []int{64 << 10, 256 << 10} {
		for _, lanes := range []bool{false, true} {
			t.Run(fmt.Sprintf("queue=%dKiB/%s", queue>>10, onOff(lanes)), func(t *testing.T) {
				line := runBothWays(t, netshape.Config{Rate: 30_000, Delay: 200 * time.Millisecond, Queue: queue}, lanes, 60*time.Second, 20*time.Second, 25*time.Second)
				t.Log(line)
				if out := os.Getenv("PERSONA_LANE_BENCH_OUT"); out != "" {
					f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					if _, err := fmt.Fprintln(f, line); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func runBothWays(t *testing.T, link netshape.Config, lanes bool, run, warmUp, quietFor time.Duration) string {
	t.Helper()
	d := newTestDevices(t)
	logs := &benchLog{}
	quiet := d.logf
	d.logf = func(format string, args ...any) {
		logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	type arrival struct {
		i, bytes int
		at       time.Time
	}
	arrivals := make(chan arrival, 1024)
	stop, flood := make(chan struct{}), make(chan struct{})
	var lastLoad atomic.Int64 // when the node's latest event went to the link
	var mu sync.Mutex
	var conns []net.Conn
	m := newMachine(d, MachineConfig{ID: "busy", Name: "Busy", Kind: "ssh"})
	m.dialNode = func(context.Context) (net.Conn, error) {
		managerSide, nodeSide := netshape.Pipe(link, link)
		p := rpc.New("n", nodeSide)
		caps := slices.Clone(nodeproto.Required)
		if lanes {
			p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
			caps = append(caps, nodeproto.NodeLanes)
		}
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "node.info":
				return proto.NodeInfo{Hostname: "busy", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test", ClaudeVersion: "test",
					Protocol: nodeproto.Version, ProtocolMin: nodeproto.MinManagerVersion, Capabilities: caps}, nil
			case "sessions.list":
				return map[string]any{"sessions": []proto.Session{}}, nil
			}
			return map[string]any{}, nil
		}
		p.OnEvent = func(event string, params json.RawMessage) {
			if event == "stream.data" {
				var note struct {
					I int `json:"i"`
				}
				_ = json.Unmarshal(params, &note)
				arrivals <- arrival{i: note.I, bytes: len(params), at: time.Now()}
			}
		}
		go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
		go func() { // the other way: the node's events, as fast as the link takes them
			load := json.RawMessage(`{"pad":"` + strings.Repeat("e", 96<<10) + `"}`)
			for {
				select {
				case <-stop:
					return
				case <-flood:
					return
				default:
				}
				if p.Notify("bench.load", load) != nil {
					return
				}
				lastLoad.Store(time.Now().UnixNano())
			}
		}()
		mu.Lock()
		conns = append(conns, managerSide, nodeSide)
		mu.Unlock()
		return managerSide, nil
	}
	t.Cleanup(func() {
		close(stop)
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	d.addEntry(m)
	runMachine(t, m)
	eventually(t, time.Minute, "busy connected", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.peer != nil && m.online && m.sessionsListed
	})

	data := strings.Repeat("A", 24<<10)
	queued := map[int]time.Time{}
	var worst time.Duration
	all, delivered, sent, arrived := 0, 0, 0, 0
	start := time.Now()
	measureFrom := start.Add(warmUp)
	for time.Since(start) < run {
		for sent-arrived < 3 { // three outstanding, as a port's window keeps a few chunks out
			raw, _ := json.Marshal(map[string]any{"id": "p1", "i": sent, "data": data})
			queued[sent] = time.Now()
			m.queueNote("stream.data", raw)
			sent++
		}
		select {
		case a := <-arrivals:
			arrived++
			worst = max(worst, a.at.Sub(queued[a.i]))
			all += a.bytes
			if a.at.After(measureFrom) {
				delivered += a.bytes
			}
		case <-time.After(time.Until(start.Add(run))):
		}
	}
	line := fmt.Sprintf("lanebench both mode=%s rate=%.0f rtt=%s queue=%d notes=%d delivered=%.0fB/s whole_run=%.0fB/s worst_note=%dms",
		onOff(lanes), link.Rate, 2*link.Delay, link.Queue, arrived, float64(delivered)/(run-warmUp).Seconds(), float64(all)/run.Seconds(),
		worst.Milliseconds())

	// The other way stops: the node's events end and its queue on the way back drains, at the link's rate, while the
	// port's bytes go on. Interactive calls (four a second) and the heartbeat (every 2.5 s) show how soon what the
	// manager sends waits as on a quiet link again: a window still sized for acknowledgements that waited behind those
	// events once put that much ahead of them (#10660's second review).
	close(flood)
	quietStart := time.Now()
	type call struct{ at, took time.Duration }
	var callMu sync.Mutex
	var calls []call
	var callers sync.WaitGroup
	var notes []call // the port's notes: when queued, and how long to the node
	r := &laneBenchResult{}
	var pings []call // each heartbeat round: when, and how long it took
	measured := make(chan struct{})
	go func() {
		defer close(measured)
		for time.Since(quietStart) < quietFor {
			select {
			case <-time.After(2500 * time.Millisecond):
			case <-t.Context().Done():
				return
			}
			at, begun := time.Since(quietStart), time.Now()
			r.measure(context.Background(), d, m, logs)
			callMu.Lock()
			pings = append(pings, call{at, time.Since(begun)})
			callMu.Unlock()
		}
	}()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for time.Since(quietStart) < quietFor {
		for sent-arrived < 3 {
			raw, _ := json.Marshal(map[string]any{"id": "p1", "i": sent, "data": data})
			queued[sent] = time.Now()
			m.queueNote("stream.data", raw)
			sent++
		}
		select {
		case a := <-arrivals:
			arrived++
			notes = append(notes, call{queued[a.i].Sub(quietStart), a.at.Sub(queued[a.i])})
		case <-tick.C:
			callers.Add(1)
			go func(at time.Duration) {
				defer callers.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				start := time.Now()
				_ = m.call(ctx, "session.info", map[string]any{"session_id": "bench"}, nil)
				callMu.Lock()
				calls = append(calls, call{at, time.Since(start)})
				callMu.Unlock()
			}(time.Since(quietStart))
		case <-time.After(time.Until(quietStart.Add(quietFor))):
		}
	}
	callers.Wait()
	<-measured
	// The node's last event went to the link when it ended; what was queued then on the way back drains at its rate.
	drained := time.Unix(0, lastLoad.Load()).Sub(quietStart) + time.Duration(float64(link.Queue)/link.Rate*float64(time.Second))
	window := func(calls []call, from, to time.Duration) string {
		var took []time.Duration
		for _, c := range calls {
			if c.at >= from && c.at < to {
				took = append(took, c.took)
			}
		}
		slices.Sort(took)
		if len(took) == 0 {
			return "0/0/0"
		}
		return fmt.Sprintf("%d/%d/%d", percentile(took, 50).Milliseconds(), percentile(took, 95).Milliseconds(), took[len(took)-1].Milliseconds())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return line + fmt.Sprintf(" drained=%.1fs calls_draining=%s calls_first_s=%s calls_next_3s=%s calls_late=%s notes_after=%s notes_late=%s pings=%d answered=%d excused=%d misses=%d pings_after=%s stall_closes=%d reconnects=%d",
		drained.Seconds(), window(calls, 0, drained), window(calls, drained, drained+time.Second), window(calls, drained+time.Second, drained+4*time.Second),
		window(calls, quietFor-10*time.Second, quietFor), window(notes, drained, drained+4*time.Second), window(notes, quietFor-10*time.Second, quietFor),
		r.pings, r.answered, r.excused, r.misses, window(pings, drained, quietFor), logs.count("took nothing for"),
		max(logs.count("] connected over ")-1, 0))
}
