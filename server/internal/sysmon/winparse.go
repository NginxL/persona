package sysmon

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// The Windows readings' parsers (windows.go reads them): plain functions over the bytes the system returns, so they are
// tested with fixtures on any OS.

// processorTimesSize is one SYSTEM_PROCESSOR_PERFORMANCE_INFORMATION: IdleTime, KernelTime, UserTime, DpcTime,
// InterruptTime (100 ns units) and InterruptCount, padded to 8 bytes.
const processorTimesSize = 48

// parseProcessorTimes reads NtQuerySystemInformation(SystemProcessorPerformanceInformation): one entry per logical CPU.
// Kernel time includes idle time (and DPCs and interrupts), so busy = kernel + user − idle of total = kernel + user.
func parseProcessorTimes(b []byte) []cpuTimes {
	var cores []cpuTimes
	for off := 0; off+processorTimesSize <= len(b); off += processorTimesSize {
		idle := binary.LittleEndian.Uint64(b[off:])
		kernel := binary.LittleEndian.Uint64(b[off+8:])
		user := binary.LittleEndian.Uint64(b[off+16:])
		total := kernel + user
		cores = append(cores, cpuTimes{busy: total - min(idle, total), total: total})
	}
	return cores
}

// systemCPU is GetSystemTimes' idle, kernel and user times as cpuTimes (kernel includes idle, as above).
func systemCPU(idle, kernel, user uint64) cpuTimes {
	total := kernel + user
	return cpuTimes{busy: total - min(idle, total), total: total}
}

// parsePagefiles reads NtQuerySystemInformation(SystemPageFileInformation) on 64-bit Windows: a chain of
// SYSTEM_PAGEFILE_INFORMATION (NextEntryOffset, TotalSize, TotalInUse, PeakUsage in pages, then the file's name), one
// per page file. Its swap is the page files' size and what is in use, in bytes.
func parsePagefiles(b []byte, page uint64) (used, total int64) {
	for off := 0; off+16 <= len(b); {
		next := binary.LittleEndian.Uint32(b[off:])
		total += int64(uint64(binary.LittleEndian.Uint32(b[off+4:])) * page)
		used += int64(uint64(binary.LittleEndian.Uint32(b[off+8:])) * page)
		if next == 0 {
			break
		}
		off += int(next)
	}
	return used, total
}

// windowsName is the edition as people say it. The registry's ProductName still says "Windows 10" on Windows 11 (same
// NT 10.0 kernel), which is told apart by its build: 22000 and up.
func windowsName(product string, build int) string {
	product = strings.TrimSpace(product)
	if product == "" {
		product = "Windows"
	}
	if build >= 22000 && strings.HasPrefix(product, "Windows 10") {
		product = "Windows 11" + strings.TrimPrefix(product, "Windows 10")
	}
	return product
}

// windowsBuild is CurrentBuild.UBR ("26200.7171"), the build alone when there is no update revision.
func windowsBuild(build string, ubr uint64) string {
	if build == "" || ubr == 0 {
		return build
	}
	return build + "." + strconv.FormatUint(ubr, 10)
}

// MIB_IF_ROW2's InterfaceAndOperStatusFlags bits.
const (
	ifHardwareInterface = 1 << 0
	ifFilterInterface   = 1 << 1
)

// physicalWindows: an interface on real hardware — Wi-Fi, Ethernet, USB tethering — and not one of the filter layers
// stacked on it (which repeat its counters). Hyper-V / WSL vEthernet, wintun (Tailscale), VPN adapters and loopback
// are software and do not count, as on Linux and macOS.
func physicalWindows(flags uint8) bool {
	return flags&ifHardwareInterface != 0 && flags&ifFilterInterface == 0
}

// diskExtentSize is one DISK_EXTENT: DiskNumber (padded to 8), StartingOffset, ExtentLength.
const diskExtentSize = 24

// parseDiskExtents reads VOLUME_DISK_EXTENTS (NumberOfDiskExtents, padded to 8, then the extents): ok when the volume
// has a non-empty extent on a disk, key naming the first one ("disk:offset") so a volume reached by two drive letters
// counts once. A virtual drive's volume answers with no extent, or one of length 0 on disk 0 at 0.
func parseDiskExtents(b []byte) (key string, ok bool) {
	if len(b) < 8 {
		return "", false
	}
	n := int(binary.LittleEndian.Uint32(b))
	for i := 0; i < n && 8+(i+1)*diskExtentSize <= len(b); i++ {
		e := b[8+i*diskExtentSize:]
		disk := binary.LittleEndian.Uint32(e)
		start := binary.LittleEndian.Uint64(e[8:])
		if length := binary.LittleEndian.Uint64(e[16:]); length > 0 {
			return strconv.FormatUint(uint64(disk), 10) + ":" + strconv.FormatUint(start, 10), true
		}
	}
	return "", false
}

// parseDriveStrings splits GetLogicalDriveStrings' "C:\\\x00D:\\\x00\x00" into the drives' roots.
func parseDriveStrings(u []uint16) []string {
	var roots []string
	start := 0
	for i, c := range u {
		if c != 0 {
			continue
		}
		if i > start {
			r := make([]rune, 0, i-start)
			for _, c := range u[start:i] {
				r = append(r, rune(c))
			}
			roots = append(roots, string(r))
		}
		start = i + 1
	}
	return roots
}

// parseSystemPowerStatus decodes Win32 SYSTEM_POWER_STATUS (ACLineStatus, BatteryFlag, BatteryLifePercent).
// Returns (nil, nil) when no system battery is present (BatteryFlag & 128 != 0), status is unknown (255),
// or BatteryLifePercent > 100.
func parseSystemPowerStatus(acLine, batteryFlag, batteryLifePct byte) (pct *float64, charging *bool) {
	if batteryFlag&128 != 0 || batteryFlag == 255 || batteryLifePct > 100 {
		return nil, nil
	}
	v := float64(batteryLifePct)
	pct = &v
	switch {
	case batteryFlag&8 != 0:
		c := true
		charging = &c
	case acLine == 0 || acLine == 1:
		c := false
		charging = &c
	}
	return pct, charging
}
