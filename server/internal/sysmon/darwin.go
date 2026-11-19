//go:build darwin

package sysmon

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/nwverse/persona/server/internal/proto"
)

// ReadSystem: sw_vers and sysctl, plus the model name, which is looked up once per process (macModel).
func ReadSystem(ctx context.Context) proto.System {
	return readSystem(ctx, macModel)
}

func readSystem(ctx context.Context, models *modelLookup) proto.System {
	s := proto.System{OS: "darwin", Distro: "macos", Name: "macOS", Arch: runtime.GOARCH, Cores: runtime.NumCPU()}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "sw_vers").Output(); err == nil {
		name, version, build := parseSwVers(out)
		if name != "" {
			s.Name = name
		}
		s.Version, s.Build = version, build
		s.Name = strings.TrimSpace(s.Name + " " + version)
	}
	if rel, err := unix.Sysctl("kern.osrelease"); err == nil {
		s.Kernel = "Darwin " + rel
	}
	s.Model = models.get(ctx)
	if s.Model == "" {
		s.Model, _ = unix.Sysctl("hw.model") // "Mac13,2"
	}
	s.CPU, _ = unix.Sysctl("machdep.cpu.brand_string")
	if n, err := unix.SysctlUint64("hw.memsize"); err == nil {
		s.Memory = int64(n)
	}
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		s.BootTime = tv.Sec
	}
	return s
}

// macModel is this process's model name lookup: a resident node makes a new Node, and so reads the system again, for
// every hub connection (#6028), and the hub's handshake waits for that read (node.info).
var macModel = newModelLookup(runCommand)

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

const (
	// modelWait is how long after a lookup starts ReadSystem waits for the model name; after it, the identifier
	// stands in.
	modelWait = time.Second
	// modelRetry is how long after a lookup ended without a name the next read may start another: a system_profiler
	// that failed or was cut off once (a machine still busy after login) is asked again on a later connection, but
	// a Mac where it always fails runs it at most once per modelRetry, not on every connection.
	modelRetry = 5 * time.Minute
	// ioregTimeout and profilerTimeout bound the two commands. system_profiler can sit for over 10 s on its
	// Activation Lock check (#6028), past the 10 s that once cut it off just before it answered.
	ioregTimeout    = 5 * time.Second
	profilerTimeout = time.Minute
)

// modelLookup finds the Mac's model name ("MacBook Pro", "Mac Studio") in the background, so no caller waits on
// system_profiler past modelWait from the start of a lookup. A name, once found, is kept for the process; a lookup
// that ends without one is tried again by a read modelRetry after it ended. Concurrent reads share the lookup in
// flight.
type modelLookup struct {
	run func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu   sync.Mutex
	name string        // the name found; set once, never cleared
	cur  *modelAttempt // the lookup in flight, or the last one, which ended without a name
}

// modelAttempt is one run of find.
type modelAttempt struct {
	until time.Time     // its start + modelWait
	done  chan struct{} // closed when find returned
	ended time.Time     // set under modelLookup.mu before done closes; zero while it runs
}

func newModelLookup(run func(ctx context.Context, name string, args ...string) ([]byte, error)) *modelLookup {
	return &modelLookup{run: run}
}

// get returns the name once it is known. Otherwise it starts a lookup when none ran yet, or when the last one ended
// without a name modelRetry ago or more, and waits for the current lookup until modelWait has passed since that
// lookup started (or ctx ends); it then returns "", as it does when there is no name.
func (l *modelLookup) get(ctx context.Context) string {
	l.mu.Lock()
	if l.name != "" {
		defer l.mu.Unlock()
		return l.name
	}
	a := l.cur
	if a == nil || (!a.ended.IsZero() && time.Since(a.ended) >= modelRetry) {
		a = &modelAttempt{until: time.Now().Add(modelWait), done: make(chan struct{})}
		l.cur = a
		go l.lookup(a)
	}
	l.mu.Unlock()

	t := time.NewTimer(time.Until(a.until))
	defer t.Stop()
	select {
	case <-a.done:
	case <-t.C:
	case <-ctx.Done():
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.name
}

func (l *modelLookup) lookup(a *modelAttempt) {
	name := l.find()
	l.mu.Lock()
	if name != "" {
		l.name = name
	}
	a.ended = time.Now()
	l.mu.Unlock()
	close(a.done)
}

// find asks the IORegistry first: on Apple silicon its product entry has the name (about 20 ms). Intel Macs have no
// such entry; system_profiler, which can take seconds, is left for them.
func (l *modelLookup) find() string {
	ctx, cancel := context.WithTimeout(context.Background(), ioregTimeout)
	out, err := l.run(ctx, "ioreg", "-p", "IODeviceTree", "-rd1", "-n", "product")
	cancel()
	if err == nil {
		if m := ioregModel(out); m != "" {
			return m
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), profilerTimeout)
	defer cancel()
	if out, err := l.run(ctx, "system_profiler", "SPHardwareDataType"); err == nil {
		return profilerModel(out)
	}
	return ""
}

// darwinCollector keeps `iostat -n 0 -w 10` (CPU) and `vm_stat 10` (memory) running and takes their latest rows:
// macOS has no /proc, and without cgo the Mach calls Activity Monitor uses are out of reach. Two idle processes
// that print a line every 10 s cost less than starting them for every sample. Load, swap, network and disk are
// sysctl / route socket / statfs reads.
type darwinCollector struct {
	cancel context.CancelFunc

	mu           sync.Mutex
	busy         *float64
	vm           *vmStat
	page         uint64
	net          netCounters
	battAt       time.Time
	battPct      *float64
	battCharging *bool
}

func NewCollector(ctx context.Context) Collector {
	ctx, cancel := context.WithCancel(ctx)
	c := &darwinCollector{cancel: cancel}
	every := fmt.Sprint(int(proto.MetricEvery / time.Second))
	go follow(ctx, time.Minute, isIostatRow, func(line string) {
		if busy, _, ok := parseIostatRow(line); ok {
			c.mu.Lock()
			c.busy = &busy
			c.mu.Unlock()
		}
	}, func() {
		c.mu.Lock()
		c.busy = nil
		c.mu.Unlock()
	}, "iostat", "-n", "0", "-w", every)
	go follow(ctx, time.Minute, nil, func(line string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if page, ok := parseVMStatHeader(line); ok {
			c.page = page
		} else if v, ok := parseVMStatRow(line); ok {
			c.vm = &v
		}
	}, func() {
		// The page size stays: it cannot change while the machine runs.
		c.mu.Lock()
		c.vm = nil
		c.mu.Unlock()
	}, "vm_stat", every)
	return c
}

func (c *darwinCollector) Close() { c.cancel() }

// isIostatRow tells follow which line is iostat's first row, dropped because it is the average since boot, not the
// last 10 s.
func isIostatRow(line string) bool {
	_, _, ok := parseIostatRow(line)
	return ok
}

func (c *darwinCollector) Sample(now time.Time) proto.MetricSample {
	s := proto.MetricSample{T: now.Unix()}
	c.mu.Lock()
	if c.busy != nil {
		s.CPU = ptr(*c.busy)
	}
	vm, page := c.vm, c.page
	c.mu.Unlock()
	if b, err := unix.SysctlRaw("vm.loadavg"); err == nil {
		s.Load = parseLoadavgRaw(b)
	}
	if n, err := unix.SysctlUint64("hw.memsize"); err == nil {
		s.MemTotal = int64(n)
	}
	if vm != nil && page > 0 {
		s.MemUsed = min(darwinMemoryUsed(*vm, page), s.MemTotal)
	}
	if b, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(b) >= 24 {
		s.SwapTotal = int64(binary.LittleEndian.Uint64(b[0:]))
		s.SwapUsed = int64(binary.LittleEndian.Uint64(b[16:]))
	}
	if rib, err := syscall.RouteRIB(syscall.NET_RT_IFLIST2, 0); err == nil {
		cur := netCounters{at: now}
		names := map[int]string{}
		if list, err := net.Interfaces(); err == nil {
			for _, ifc := range list {
				names[ifc.Index] = ifc.Name
			}
		}
		for _, i := range parseIfList2(rib) {
			if physicalDarwin(i, names[i.index]) {
				cur.rx += i.rx
				cur.tx += i.tx
			}
		}
		s.NetRx, s.NetTx = netRates(c.net, cur)
		c.net = cur
	}
	if v := Volumes(); len(v) > 0 {
		s.DiskUsed, s.DiskTotal = v[0].Used, v[0].Total
	}
	s.BatteryPercent, s.BatteryCharging = c.readBattery(now)
	return s
}

func (c *darwinCollector) readBattery(now time.Time) (*float64, *bool) {
	c.mu.Lock()
	due := c.battAt.IsZero() || now.Sub(c.battAt) >= listEvery || now.Before(c.battAt)
	pct, chg := c.battPct, c.battCharging
	c.mu.Unlock()
	if !due {
		return pct, chg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
	cancel()
	if err == nil {
		pct, chg = parsePmsetBatt(out)
	} else {
		pct, chg = nil, nil
	}
	c.mu.Lock()
	c.battAt, c.battPct, c.battCharging = now, pct, chg
	c.mu.Unlock()
	return pct, chg
}

// Volumes is the data volume ("/" is the sealed system snapshot, a few GB; what fills up is the data volume), or "/"
// when there is none.
func Volumes() []proto.DiskVolume {
	for _, m := range []string{"/System/Volumes/Data", "/"} {
		var st syscall.Statfs_t
		if syscall.Statfs(m, &st) == nil {
			used, total := diskUsage(st.Blocks, st.Bfree, st.Bavail, uint64(st.Bsize))
			return []proto.DiskVolume{{Mount: m, Used: used, Total: total}}
		}
	}
	return nil
}

// ifStats is one RTM_IFINFO2 message of the route socket's interface list.
type ifStats struct {
	index  int
	flags  uint32
	typ    byte
	rx, tx uint64
}

// parseIfList2 walks NET_RT_IFLIST2: if_msghdr2 (pack 4): msglen u16 @0, type u8 @3, flags i32 @8, index u16 @12,
// if_data64 @32 (type u8 @+0, ibytes u64 @+64, obytes u64 @+72); address messages in between are skipped.
func parseIfList2(b []byte) []ifStats {
	const rtmIfinfo2, size = 0x12, 160
	var out []ifStats
	for len(b) >= 4 {
		n := int(binary.LittleEndian.Uint16(b))
		if n < 4 || n > len(b) {
			break
		}
		if b[3] == rtmIfinfo2 && n >= size {
			out = append(out, ifStats{
				index: int(binary.LittleEndian.Uint16(b[12:])),
				flags: binary.LittleEndian.Uint32(b[8:]),
				typ:   b[32],
				rx:    binary.LittleEndian.Uint64(b[96:]),
				tx:    binary.LittleEndian.Uint64(b[104:]),
			})
		}
		b = b[n:]
	}
	return out
}

// physicalDarwin: Ethernet-type interfaces named en* (Wi-Fi, Ethernet, Thunderbolt ports), not loopback.
// Tunnels (utun* — Tailscale, VPNs, TUN proxies), bridges, awdl/llw (AirDrop), anpi and VM interfaces are not.
func physicalDarwin(i ifStats, name string) bool {
	const iftEther = 0x6
	return i.typ == iftEther && i.flags&syscall.IFF_LOOPBACK == 0 && strings.HasPrefix(name, "en")
}

// parseLoadavgRaw reads struct loadavg { fixpt_t ldavg[3]; long fscale } (fscale @16 on 64-bit).
func parseLoadavgRaw(b []byte) *float64 {
	if len(b) < 24 {
		return nil
	}
	scale := binary.LittleEndian.Uint64(b[16:])
	if scale == 0 {
		return nil
	}
	v := float64(int64(float64(binary.LittleEndian.Uint32(b))/float64(scale)*100+0.5)) / 100
	return &v
}

// loadAverageOS reads vm.loadavg as a C struct (no process, no locale).
func loadAverageOS() *float64 {
	if b, err := unix.SysctlRaw("vm.loadavg"); err == nil {
		return parseLoadavgRaw(b)
	}
	return nil
}
