package devices

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
)

// TestResumeBench is #10663's experiment: a machine downloads 8 MiB in 256 KiB file.chunk calls while it makes an
// interactive call every 200 ms, over a shaped link (1 MB/s, 100 ms round trip), and 3 s in the connection breaks:
// the TCP connection cut (both ends see it end), or the computer's address changed (the old one goes silent: unanswered
// pings show it, and with resuming a transport on which nothing came back while frames waited to be acknowledged). Without resuming (a node from before it, main's behavior) the calls in flight are dropped
// and the machine connects anew; with it the connection is taken back. One line per run: how long until a call
// succeeded again, the calls that failed, the transfer, the reconnects and resumes. PERSONA_RESUME_BENCH=1 runs it;
// PERSONA_RESUME_BENCH_OUT=<file> appends the lines there.
func TestResumeBench(t *testing.T) {
	if os.Getenv("PERSONA_RESUME_BENCH") != "1" {
		t.Skip("the resume experiment takes a minute: PERSONA_RESUME_BENCH=1 runs it")
	}
	link := netshape.Config{Rate: 1_000_000, Delay: 50 * time.Millisecond, Queue: 256 << 10}
	for _, scenario := range []string{"cut", "address"} {
		for _, resume := range []bool{false, true} {
			t.Run(scenario+"/"+onOff(resume), func(t *testing.T) {
				line := runResumeBench(t, link, scenario, resume)
				t.Log(line)
				if out := os.Getenv("PERSONA_RESUME_BENCH_OUT"); out != "" {
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

// TestResumeBenchShort is the experiment's cut, scaled down and with resuming only: no call fails and nothing
// connects anew.
func TestResumeBenchShort(t *testing.T) {
	r := resumeBenchRun(t, netshape.Config{Rate: 4_000_000, Delay: 10 * time.Millisecond, Queue: 256 << 10}, "cut", true,
		resumeBench{chunks: 16, chunk: 128 << 10, cutAfter: 500 * time.Millisecond, after: 3 * time.Second})
	if r.failed > 0 || r.transferFailed > 0 || r.reconnects > 0 || r.resumed != 1 {
		t.Fatalf("resumed %d, %d calls and %d chunks failed, %d reconnects", r.resumed, r.failed, r.transferFailed, r.reconnects)
	}
}

type resumeBench struct {
	chunks, chunk   int
	cutAfter, after time.Duration // when the connection breaks; how long calls go on after it
}

type resumeBenchResult struct {
	recovered                time.Duration // from the break until a call succeeded again
	calls, failed            int
	transfer                 time.Duration
	transferFailed           int
	reconnects, resumed      int
	worstCall                time.Duration
	pingsUnanswered, pingCut int
}

func runResumeBench(t *testing.T, link netshape.Config, scenario string, resume bool) string {
	after := 20 * time.Second
	if scenario == "address" { // nothing says the old transport is gone: the heartbeat, or resuming's own check, finds out
		after = 50 * time.Second
	}
	r := resumeBenchRun(t, link, scenario, resume, resumeBench{chunks: 32, chunk: 256 << 10, cutAfter: 3 * time.Second, after: after})
	return fmt.Sprintf("resumebench scenario=%s resume=%s recovered=%dms calls=%d failed=%d worst_call=%dms transfer=%.1fs transfer_failed=%d reconnects=%d resumed=%d",
		scenario, onOff(resume), r.recovered.Milliseconds(), r.calls, r.failed, r.worstCall.Milliseconds(), r.transfer.Seconds(),
		r.transferFailed, r.reconnects, r.resumed)
}

func resumeBenchRun(t *testing.T, link netshape.Config, scenario string, resume bool, b resumeBench) resumeBenchResult {
	t.Helper()
	oldWait, oldLimit := pingWait, pingMissLimit
	pingWait, pingMissLimit = time.Second, 2 // pings each second below: an address change shows in about 3 s
	t.Cleanup(func() { pingWait, pingMissLimit = oldWait, oldLimit })
	d := newTestDevices(t)
	logs := &benchLog{}
	quiet := d.logf
	d.logf = func(format string, args ...any) {
		logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	n := &residentNode{link: link, noResume: !resume, release: make(chan struct{}), slowSeen: make(chan struct{})}
	close(n.release)
	m := newMachine(d, MachineConfig{ID: "bench", Name: "bench", Kind: "ssh"})
	m.dialNode = n.dial
	d.addEntry(m)
	runMachine(t, m)
	eventually(t, 10*time.Second, "bench connected", func() bool { return logs.count("] connected over ") > 0 })

	stop := make(chan struct{})
	var bg sync.WaitGroup
	bg.Add(1)
	go func() { // the heartbeat, every second
		defer bg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			d.measureNodes(context.Background())
		}
	}()
	type call struct {
		start, end time.Time
		err        error
	}
	var mu sync.Mutex
	var calls []call
	var r resumeBenchResult
	transfer := make(chan struct{})
	bg.Add(1)
	go func() {
		defer bg.Done()
		defer close(transfer)
		start := time.Now()
		for i := range b.chunks {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err := m.call(ctx, "file.chunk", map[string]any{"path": "/home/me/big.bin", "offset": i * b.chunk, "length": b.chunk}, nil)
			cancel()
			if err != nil {
				mu.Lock()
				r.transferFailed++
				mu.Unlock()
			}
		}
		mu.Lock()
		r.transfer = time.Since(start)
		mu.Unlock()
	}()
	var callers sync.WaitGroup
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	start := time.Now()
	cutAt := start.Add(b.cutAfter)
	cut := false
	for time.Since(start) < b.cutAfter+b.after {
		<-tick.C
		if !cut && time.Now().After(cutAt) {
			cut, cutAt = true, time.Now()
			switch scenario {
			case "cut":
				n.mu.Lock()
				mgr := n.manager
				n.mu.Unlock()
				n.cut()
				mgr.Close()
			case "address":
				n.die()
			}
		}
		callers.Add(1)
		go func() {
			defer callers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := call{start: time.Now()}
			c.err = m.call(ctx, "session.info", map[string]any{"session_id": "bench"}, nil)
			c.end = time.Now()
			mu.Lock()
			calls = append(calls, c)
			mu.Unlock()
		}()
	}
	callers.Wait()
	<-transfer
	close(stop)
	bg.Wait()
	mu.Lock()
	defer mu.Unlock()
	slices.SortFunc(calls, func(a, b call) int { return a.end.Compare(b.end) })
	r.calls = len(calls)
	for _, c := range calls {
		if c.err != nil {
			r.failed++
		}
		r.worstCall = max(r.worstCall, c.end.Sub(c.start))
	}
	for _, c := range calls {
		if c.err == nil && c.end.After(cutAt) && c.start.After(cutAt) {
			r.recovered = c.end.Sub(cutAt)
			break
		}
	}
	r.reconnects = max(logs.count("] connected over ")-1, 0)
	r.resumed = logs.count("resumed after")
	return r
}
