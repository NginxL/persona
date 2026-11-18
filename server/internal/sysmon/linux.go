//go:build linux

package sysmon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ReadSystem reads /etc/os-release, the kernel release, DMI and /proc/cpuinfo.
func ReadSystem(ctx context.Context) proto.System {
	osr, err := os.ReadFile("/etc/os-release")
	if err != nil {
		osr, _ = os.ReadFile("/usr/lib/os-release")
	}
	s := linuxSystem(parseOSRelease(osr))
	s.Arch, s.Cores = runtime.GOARCH, runtime.NumCPU()
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s.Kernel = strings.TrimSpace(string(b))
	}
	vendor, _ := os.ReadFile("/sys/devices/virtual/dmi/id/sys_vendor")
	product, _ := os.ReadFile("/sys/devices/virtual/dmi/id/product_name")
	s.Model = dmiModel(string(vendor), string(product))
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		s.CPU = cpuModel(b)
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.Memory = parseMeminfo(b)["MemTotal"]
	}
	if limit, _, ok := cgroupMemory(cgroupRoot); ok && limit < s.Memory {
		s.Memory = limit
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		_, _, s.BootTime = parseProcStat(b)
	}
	return s
}

// cgroupRoot is where the cgroup hierarchy is mounted (cgroupMemory).
const cgroupRoot = "/sys/fs/cgroup"

type linuxCollector struct {
	cpu   cpuTimes
	cores []cpuTimes
	net   netCounters
	// The interfaces net adds up (netSample).
	counted map[string]bool
	buf     bytes.Buffer // what read read last, reused between samples
	// Whether each interface is physical, forgotten every listEvery: resolving its /sys link takes a few syscalls,
	// and a host running containers has a veth per container.
	nics   map[string]bool
	nicsAt time.Time
	lookup func(name string) bool // physical, or a stand-in in tests; nil is physical
	mounts mountCache

	// Termux battery fallback cache (when /sys/class/power_supply is blocked by SELinux).
	termuxBattAt       time.Time
	termuxBattPct      *float64
	termuxBattCharging *bool
}

// NewCollector starts sampling (nothing runs between samples on Linux).
func NewCollector(ctx context.Context) Collector { return &linuxCollector{} }

func (c *linuxCollector) Close() {}

func (c *linuxCollector) Sample(now time.Time) proto.MetricSample {
	s := proto.MetricSample{T: now.Unix()}
	if b, err := c.read("/proc/stat"); err == nil {
		all, cores, _ := parseProcStat(b)
		s.CPU = cpuPercent(c.cpu, all)
		if len(cores) == len(c.cores) {
			for i := range cores {
				if v := cpuPercent(c.cores[i], cores[i]); v != nil {
					s.Cores = append(s.Cores, *v)
				}
			}
			if len(s.Cores) != len(cores) {
				s.Cores = nil
			}
		}
		c.cpu, c.cores = all, cores
	}
	if b, err := c.read("/proc/loadavg"); err == nil {
		s.Load = parseLoadavg(b)
	}
	if b, err := c.read("/proc/meminfo"); err == nil {
		s.MemUsed, s.MemTotal, s.SwapUsed, s.SwapTotal = linuxMemory(parseMeminfo(b))
	}
	// In a container the limit is what it can use, not the host's memory (cgroupMemory).
	if limit, used, ok := cgroupMemory(cgroupRoot); ok && limit < s.MemTotal {
		s.MemTotal, s.MemUsed = limit, min(used, limit)
	}
	if b, err := c.read("/proc/net/dev"); err == nil {
		s.NetRx, s.NetTx = c.netSample(parseNetDev(b), now)
	}
	for _, v := range volumesOf(localMounts(c.read, &c.mounts)) {
		s.DiskUsed += v.Used
		s.DiskTotal += v.Total
	}
	s.BatteryPercent, s.BatteryCharging = c.readBattery(now)
	s.TempC = readLinuxTemp()
	return s
}

// localMounts is one mount point per local block device (diskMountinfo, or diskMounts without mountinfo), "/" when
// neither can be read.
func localMounts(read func(string) ([]byte, error), cache *mountCache) []string {
	if b, err := read("/proc/self/mountinfo"); err == nil {
		if m := cache.of(b); len(m) > 0 {
			return m
		}
	} else if b, err := read("/proc/self/mounts"); err == nil {
		if m := diskMounts(b); len(m) > 0 {
			return m
		}
	}
	return []string{"/"}
}

// volumesOf is each mount's use, like df; a mount statfs cannot read is left out.
func volumesOf(mounts []string) []proto.DiskVolume {
	var out []proto.DiskVolume
	for _, m := range mounts {
		var st syscall.Statfs_t
		if syscall.Statfs(m, &st) == nil {
			used, total := diskUsage(st.Blocks, st.Bfree, st.Bavail, uint64(st.Bsize))
			out = append(out, proto.DiskVolume{Mount: m, Used: used, Total: total})
		}
	}
	return out
}

// Volumes are the local filesystems a sample adds up, one by one (a computer's diagnosis, docs/server/machines.md
// "Diagnosis and cleanup").
func Volumes() []proto.DiskVolume {
	var cache mountCache
	return volumesOf(localMounts(os.ReadFile, &cache))
}

// netSample is the network rates since the last call, from each interface's counters (parseNetDev): the physical
// interfaces added up. In a container's own network namespace /sys calls every interface virtual (eth0 is one end of
// a veth pair), so when none is physical the name decides, as without /sys: eth0 counts, lo does not. When the set
// added up changes, a newcomer's bytes since boot would draw a spike, so that sample has no rate, as on Windows.
func (c *linuxCollector) netSample(dev map[string][2]uint64, now time.Time) (rx, tx *float64) {
	byName := true
	for name := range dev {
		if c.isPhysical(name, now) {
			byName = false
			break
		}
	}
	counts := func(name string) bool {
		if byName {
			return !virtualName(name)
		}
		return c.isPhysical(name, now)
	}
	cur, same, n := netCounters{at: now}, true, 0
	for name, v := range dev {
		if counts(name) {
			cur.rx += v[0]
			cur.tx += v[1]
			same, n = same && c.counted[name], n+1
		}
	}
	if !same || n != len(c.counted) {
		c.counted, c.net = make(map[string]bool, n), netCounters{}
		for name := range dev {
			if counts(name) {
				c.counted[name] = true
			}
		}
	}
	rx, tx = netRates(c.net, cur)
	c.net = cur
	return rx, tx
}

// read reads a /proc file into c.buf: the bytes are good until the next read.
func (c *linuxCollector) read(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c.buf.Reset()
	if _, err := c.buf.ReadFrom(f); err != nil {
		return nil, err
	}
	return c.buf.Bytes(), nil
}

// isPhysical is physical(name) remembered for up to listEvery, so an interface renamed or moved shows within a minute,
// as on Windows; a name not seen before is looked up at once.
func (c *linuxCollector) isPhysical(name string, now time.Time) bool {
	if c.nics == nil || now.Sub(c.nicsAt) >= listEvery || now.Before(c.nicsAt) {
		c.nics, c.nicsAt = map[string]bool{}, now
	}
	p, ok := c.nics[name]
	if !ok {
		if c.lookup != nil {
			p = c.lookup(name)
		} else {
			p = physical(name)
		}
		c.nics[name] = p
	}
	return p
}

// mountCache keeps the last mountinfo and what diskMountinfo picked from it: the file is the same on almost every
// sample, and has thousands of lines on a host running containers.
type mountCache struct {
	raw    []byte
	points []string
	parsed bool
}

// of is diskMountinfo(b), parsed again only when b differs from the last call's. The result must not be changed.
func (m *mountCache) of(b []byte) []string {
	if !m.parsed || !bytes.Equal(b, m.raw) {
		m.raw, m.points, m.parsed = append(m.raw[:0], b...), diskMountinfo(b), true
	}
	return m.points
}

// physical: the kernel puts every software interface (lo, docker0, veth*, br-*, tun*, tailscale0, wg0, bonds and
// VLANs on top of real ports) under /sys/devices/virtual/net; real NICs, virtio ones in a VM included, sit under
// their bus. Without /sys the name decides.
func physical(name string) bool {
	if target, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", name)); err == nil {
		return !strings.Contains(target, "/devices/virtual/")
	}
	return !virtualName(name)
}

func loadAverageOS() *float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	return parseLoadavg(b)
}

const termuxBatteryEvery = time.Minute

func readTrimmedFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c *linuxCollector) readBattery(now time.Time) (*float64, *bool) {
	if pct, chg := readSysPowerSupply("/sys/class/power_supply"); pct != nil {
		return pct, chg
	}
	if os.Getenv("TERMUX_VERSION") == "" {
		return nil, nil
	}
	if c.termuxBattAt.IsZero() || now.Sub(c.termuxBattAt) >= termuxBatteryEvery || now.Before(c.termuxBattAt) {
		c.termuxBattAt = now
		c.termuxBattPct, c.termuxBattCharging = readTermuxBattery()
	}
	return c.termuxBattPct, c.termuxBattCharging
}

func readSysPowerSupply(root string) (*float64, *bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	var supplies []powerSupply
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		supplies = append(supplies, powerSupply{
			Type:       readTrimmedFile(filepath.Join(dir, "type")),
			Scope:      readTrimmedFile(filepath.Join(dir, "scope")),
			Present:    readTrimmedFile(filepath.Join(dir, "present")),
			Status:     readTrimmedFile(filepath.Join(dir, "status")),
			Capacity:   readTrimmedFile(filepath.Join(dir, "capacity")),
			EnergyNow:  readTrimmedFile(filepath.Join(dir, "energy_now")),
			EnergyFull: readTrimmedFile(filepath.Join(dir, "energy_full")),
			ChargeNow:  readTrimmedFile(filepath.Join(dir, "charge_now")),
			ChargeFull: readTrimmedFile(filepath.Join(dir, "charge_full")),
		})
	}
	return parsePowerSupplies(supplies)
}

func readTermuxBattery() (*float64, *bool) {
	path, err := exec.LookPath("termux-battery-status")
	if err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path).Output()
	if err != nil {
		return nil, nil
	}
	return parseTermuxBatteryStatus(out)
}

func readLinuxTemp() *float64 {
	var readings []thermalReading
	if entries, err := os.ReadDir("/sys/class/hwmon"); err == nil {
		for _, e := range entries {
			dir := filepath.Join("/sys/class/hwmon", e.Name())
			name := readTrimmedFile(filepath.Join(dir, "name"))
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				fn := f.Name()
				if !strings.HasPrefix(fn, "temp") || !strings.HasSuffix(fn, "_input") {
					continue
				}
				raw := readTrimmedFile(filepath.Join(dir, fn))
				milli, err := strconv.ParseInt(raw, 10, 64)
				if err != nil {
					continue
				}
				labelFile := strings.TrimSuffix(fn, "_input") + "_label"
				label := readTrimmedFile(filepath.Join(dir, labelFile))
				readings = append(readings, thermalReading{Name: name, Label: label, MilliC: milli})
			}
		}
	}
	if entries, err := os.ReadDir("/sys/class/thermal"); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "thermal_zone") {
				continue
			}
			dir := filepath.Join("/sys/class/thermal", e.Name())
			typ := readTrimmedFile(filepath.Join(dir, "type"))
			raw := readTrimmedFile(filepath.Join(dir, "temp"))
			milli, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			readings = append(readings, thermalReading{Name: typ, MilliC: milli})
		}
	}
	return selectTempC(readings)
}
