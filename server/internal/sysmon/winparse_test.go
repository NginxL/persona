package sysmon

import (
	"encoding/binary"
	"slices"
	"testing"
	"unicode/utf16"
)

func TestProcessorTimes(t *testing.T) {
	// Two logical CPUs, as SystemProcessorPerformanceInformation lays them out: idle, kernel (with idle), user, DPC,
	// interrupt (100 ns), interrupt count, padding.
	b := make([]byte, 2*processorTimesSize)
	put := func(i int, idle, kernel, user uint64) {
		e := b[i*processorTimesSize:]
		binary.LittleEndian.PutUint64(e, idle)
		binary.LittleEndian.PutUint64(e[8:], kernel)
		binary.LittleEndian.PutUint64(e[16:], user)
		binary.LittleEndian.PutUint64(e[24:], 7) // DPC and interrupt time are inside kernel time: not added again
		binary.LittleEndian.PutUint32(e[40:], 99)
	}
	put(0, 600, 800, 200) // 1000 in all, 400 busy
	put(1, 900, 950, 50)
	got := parseProcessorTimes(append(b, 1, 2, 3)) // a cut-off entry at the end is ignored
	want := []cpuTimes{{busy: 400, total: 1000}, {busy: 100, total: 1000}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
	// The next reading: core 0 was busy for half of the 1000 between.
	put(0, 1100, 1500, 500)
	if v := cpuPercent(want[0], parseProcessorTimes(b)[0]); v == nil || *v != 50 {
		t.Fatalf("core 0 = %v", v)
	}
	if c := systemCPU(600, 800, 200); c != (cpuTimes{busy: 400, total: 1000}) {
		t.Fatalf("system %+v", c)
	}
	if c := systemCPU(5, 3, 0); c.busy != 0 { // idle never makes busy go below zero
		t.Fatalf("clamped %+v", c)
	}
}

func TestPagefiles(t *testing.T) {
	entry := func(next, size, inUse uint32, name string) []byte {
		e := make([]byte, 32, 32+2*len(name))
		binary.LittleEndian.PutUint32(e, next)
		binary.LittleEndian.PutUint32(e[4:], size)
		binary.LittleEndian.PutUint32(e[8:], inUse)
		binary.LittleEndian.PutUint32(e[12:], inUse+10) // peak
		for _, c := range utf16.Encode([]rune(name)) {
			e = binary.LittleEndian.AppendUint16(e, c)
		}
		return e
	}
	first := entry(0, 0, 0, `\??\C:\pagefile.sys`)
	binary.LittleEndian.PutUint32(first, uint32(len(first))) // NextEntryOffset: the second follows the name
	b := append(first, entry(0, 0, 0, `\??\D:\pagefile.sys`)...)
	binary.LittleEndian.PutUint32(b[4:], 1000)           // C: 1000 pages, 250 in use
	binary.LittleEndian.PutUint32(b[8:], 250)            //
	binary.LittleEndian.PutUint32(b[len(first)+4:], 500) // D: 500 pages, 50 in use
	binary.LittleEndian.PutUint32(b[len(first)+8:], 50)  //
	if used, total := parsePagefiles(b, 4096); used != 300*4096 || total != 1500*4096 {
		t.Fatalf("used %d total %d", used, total)
	}
	if used, total := parsePagefiles(nil, 4096); used != 0 || total != 0 { // no page file
		t.Fatalf("none: %d %d", used, total)
	}
}

func TestWindowsNames(t *testing.T) {
	for _, c := range []struct {
		product string
		build   int
		want    string
	}{
		{"Windows 10 Home", 26200, "Windows 11 Home"}, // the registry's name on Windows 11
		{"Windows 10 Pro", 19045, "Windows 10 Pro"},
		{"Windows Server 2022 Datacenter", 20348, "Windows Server 2022 Datacenter"},
		{" ", 0, "Windows"},
	} {
		if got := windowsName(c.product, c.build); got != c.want {
			t.Errorf("windowsName(%q, %d) = %q, want %q", c.product, c.build, got, c.want)
		}
	}
	if b := windowsBuild("26200", 7171); b != "26200.7171" {
		t.Fatal(b)
	}
	if b := windowsBuild("19045", 0); b != "19045" {
		t.Fatal(b)
	}
}

func TestPhysicalWindows(t *testing.T) {
	// The flags of this Windows 11 machine's interfaces (GetIfEntry2Ex).
	for name, c := range map[string]struct {
		flags uint8
		want  bool
	}{
		"WLAN (hardware, connector)":           {0b00000101, true},
		"Ethernet, no cable":                   {0b00010101, true},
		"Tailscale (wintun)":                   {0b00000000, false},
		"Wi-Fi Direct virtual adapter":         {0b00010000, false},
		"Bluetooth PAN":                        {0b00010000, false},
		"loopback":                             {0b00000000, false},
		"a filter layer on the WLAN (if seen)": {0b00000111, false},
	} {
		if got := physicalWindows(c.flags); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestDiskExtents(t *testing.T) {
	extents := func(disk uint32, start, length uint64) []byte {
		b := make([]byte, 8+diskExtentSize)
		binary.LittleEndian.PutUint32(b, 1)
		binary.LittleEndian.PutUint32(b[8:], disk)
		binary.LittleEndian.PutUint64(b[16:], start)
		binary.LittleEndian.PutUint64(b[24:], length)
		return b
	}
	// C: on disk 1 (seen on the machine), and Google Drive's G:, which answers disk 0 at 0 with length 0.
	if key, ok := parseDiskExtents(extents(1, 292552704, 490350837760)); !ok || key != "1:292552704" {
		t.Fatalf("C: %q %v", key, ok)
	}
	if _, ok := parseDiskExtents(extents(0, 0, 0)); ok {
		t.Fatal("a virtual drive counted")
	}
	if _, ok := parseDiskExtents([]byte{0, 0, 0, 0, 0, 0, 0, 0}); ok { // no extents at all
		t.Fatal("no extents counted")
	}
	if _, ok := parseDiskExtents(extents(1, 0, 10)[:20]); ok { // cut off
		t.Fatal("cut-off answer counted")
	}
}

func TestDriveStrings(t *testing.T) {
	u := utf16.Encode([]rune("C:\\\x00D:\\\x00G:\\\x00\x00"))
	if got := parseDriveStrings(u); !slices.Equal(got, []string{`C:\`, `D:\`, `G:\`}) {
		t.Fatalf("%q", got)
	}
	if got := parseDriveStrings(nil); got != nil {
		t.Fatalf("%q", got)
	}
}

func TestParseSystemPowerStatus(t *testing.T) {
	// Desktop (no system battery: BatteryFlag & 128 != 0).
	if pct, chg := parseSystemPowerStatus(1, 128, 255); pct != nil || chg != nil {
		t.Fatalf("desktop = (%v, %v)", pct, chg)
	}
	// Unknown battery status or > 100%.
	if pct, chg := parseSystemPowerStatus(255, 255, 255); pct != nil || chg != nil {
		t.Fatalf("unknown = (%v, %v)", pct, chg)
	}
	if pct, chg := parseSystemPowerStatus(1, 1, 101); pct != nil || chg != nil {
		t.Fatalf("invalid pct = (%v, %v)", pct, chg)
	}
	// Laptop discharging on battery.
	if pct, chg := parseSystemPowerStatus(0, 1, 68); pct == nil || *pct != 68 || chg == nil || *chg {
		t.Fatalf("discharging = (%v, %v)", pct, chg)
	}
	// Laptop / handheld charging on AC.
	if pct, chg := parseSystemPowerStatus(1, 9, 45); pct == nil || *pct != 45 || chg == nil || !*chg {
		t.Fatalf("charging = (%v, %v)", pct, chg)
	}
	// Laptop on AC, full (not charging).
	if pct, chg := parseSystemPowerStatus(1, 1, 100); pct == nil || *pct != 100 || chg == nil || *chg {
		t.Fatalf("ac full = (%v, %v)", pct, chg)
	}
}
