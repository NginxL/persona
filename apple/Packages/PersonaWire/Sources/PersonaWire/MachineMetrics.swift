import Foundation

/// What a machine is (docs/server/machines.md): OS, hardware, since when it runs. From the node's last connection.
public nonisolated struct MachineSystem: Codable, Hashable, Sendable {
    public var os: String
    public var distro: String
    public var like: String?
    public var name: String
    public var version: String?
    public var build: String?
    public var kernel: String?
    public var arch: String?
    public var model: String?
    public var cpu: String?
    public var cores: Int
    public var memory: Int64
    public var bootTime: Int64?

    public enum CodingKeys: String, CodingKey {
        case os, distro, like, name, version, build, kernel, arch, model, cpu, cores, memory
        case bootTime = "boot_time"
    }

    /// The mark to draw for this OS (OSBadge).
    public var family: OSFamily { OSFamily(distro: distro, like: like, os: os) }

    public var bootDate: Date? { bootTime.map { Date(timeIntervalSince1970: TimeInterval($0)) } }

    public init(os: String, distro: String, like: String? = nil, name: String, version: String? = nil, build: String? = nil, kernel: String? = nil, arch: String? = nil, model: String? = nil, cpu: String? = nil, cores: Int, memory: Int64, bootTime: Int64? = nil) {
        self.os = os
        self.distro = distro
        self.like = like
        self.name = name
        self.version = version
        self.build = build
        self.kernel = kernel
        self.arch = arch
        self.model = model
        self.cpu = cpu
        self.cores = cores
        self.memory = memory
        self.bootTime = bootTime
    }
}

public nonisolated enum OSFamily: String, Sendable {
    case macos, ubuntu, debian, fedora, redhat, centos, rocky, alma, arch, alpine, opensuse, nixos, linux, other

    public init(distro: String, like: String?, os: String) {
        let ids = [distro.lowercased()] + (like ?? "").lowercased().split(separator: " ").map(String.init)
        for id in ids {
            switch id {
            case "macos": self = .macos; return
            case "ubuntu", "pop", "linuxmint", "elementary", "zorin": self = .ubuntu; return
            case "debian", "raspbian", "kali": self = .debian; return
            case "fedora": self = .fedora; return
            case "rhel", "redhat": self = .redhat; return
            case "centos": self = .centos; return
            case "rocky": self = .rocky; return
            case "almalinux": self = .alma; return
            case "arch", "manjaro", "endeavouros": self = .arch; return
            case "alpine": self = .alpine; return
            case "opensuse", "opensuse-leap", "opensuse-tumbleweed", "suse", "sles": self = .opensuse; return
            case "nixos": self = .nixos; return
            default: continue
            }
        }
        self = os == "darwin" ? .macos : os == "linux" ? .linux : .other
    }
}

/// One reading (every 10 s). Nil: not measured.
public nonisolated struct MetricSample: Codable, Hashable, Sendable {
    public var t: Int64
    public var cpu: Double?
    public var cores: [Double]?
    public var load: Double?
    public var memUsed: Int64
    public var memTotal: Int64
    public var swapUsed: Int64
    public var swapTotal: Int64
    public var netRx: Double?
    public var netTx: Double?
    public var diskUsed: Int64
    public var diskTotal: Int64
    public var batteryPercent: Double?
    public var batteryCharging: Bool?
    public var tempC: Double?

    public enum CodingKeys: String, CodingKey {
        case t, cpu, cores, load
        case memUsed = "mem_used", memTotal = "mem_total", swapUsed = "swap_used", swapTotal = "swap_total"
        case netRx = "net_rx", netTx = "net_tx", diskUsed = "disk_used", diskTotal = "disk_total"
        case batteryPercent = "battery_percent", batteryCharging = "battery_charging", tempC = "temp_c"
    }

    public var date: Date { Date(timeIntervalSince1970: TimeInterval(t)) }
    public var memPercent: Double? { memTotal > 0 ? 100 * Double(memUsed) / Double(memTotal) : nil }
    public var diskPercent: Double? { diskTotal > 0 ? 100 * Double(diskUsed) / Double(diskTotal) : nil }
    public var swapPercent: Double? { swapTotal > 0 ? 100 * Double(swapUsed) / Double(swapTotal) : nil }

    public init(t: Int64, cpu: Double? = nil, cores: [Double]? = nil, load: Double? = nil, memUsed: Int64, memTotal: Int64, swapUsed: Int64, swapTotal: Int64, netRx: Double? = nil, netTx: Double? = nil, diskUsed: Int64, diskTotal: Int64, batteryPercent: Double? = nil, batteryCharging: Bool? = nil, tempC: Double? = nil) {
        self.t = t
        self.cpu = cpu
        self.cores = cores
        self.load = load
        self.memUsed = memUsed
        self.memTotal = memTotal
        self.swapUsed = swapUsed
        self.swapTotal = swapTotal
        self.netRx = netRx
        self.netTx = netTx
        self.diskUsed = diskUsed
        self.diskTotal = diskTotal
        self.batteryPercent = batteryPercent
        self.batteryCharging = batteryCharging
        self.tempC = tempC
    }
}

/// One step of history: averages, or a break (gap) where the machine sent nothing. Memory / swap / disk in bytes.
public nonisolated struct MetricPoint: Codable, Hashable, Sendable {
    public var t: Int64
    public var gap: Bool?
    public var cpu: Double?
    public var load: Double?
    public var mem: Double?
    public var swap: Double?
    public var rx: Double?
    public var tx: Double?
    public var disk: Double?
    public var batteryPercent: Double?
    public var tempC: Double?

    public enum CodingKeys: String, CodingKey {
        case t, gap, cpu, load, mem, swap, rx, tx, disk
        case batteryPercent = "battery_percent", tempC = "temp_c"
    }

    public var date: Date { Date(timeIntervalSince1970: TimeInterval(t)) }

    public init(t: Int64, gap: Bool? = nil, cpu: Double? = nil, load: Double? = nil, mem: Double? = nil, swap: Double? = nil, rx: Double? = nil, tx: Double? = nil, disk: Double? = nil, batteryPercent: Double? = nil, tempC: Double? = nil) {
        self.t = t
        self.gap = gap
        self.cpu = cpu
        self.load = load
        self.mem = mem
        self.swap = swap
        self.rx = rx
        self.tx = tx
        self.disk = disk
        self.batteryPercent = batteryPercent
        self.tempC = tempC
    }
    public var isGap: Bool { gap == true }
}

/// A machine's dashboard card or detail page.
public nonisolated struct MachineMetrics: Codable, Hashable, Sendable, Identifiable {
    public var machineID: String
    public var online: Bool
    public var system: MachineSystem?
    public var latest: MetricSample?
    public var points: [MetricPoint]
    /// Its tokens, on the dashboard only; nil from an older hub or a node that never reported usage.
    public var usage: UsageCard?

    public var id: String { machineID }

    public init(machineID: String, online: Bool, system: MachineSystem? = nil, latest: MetricSample? = nil, points: [MetricPoint], usage: UsageCard? = nil) {
        self.machineID = machineID
        self.online = online
        self.system = system
        self.latest = latest
        self.points = points
        self.usage = usage
    }

    public enum CodingKeys: String, CodingKey {
        case machineID = "machine_id", online, system, latest, points, usage
    }
}

public nonisolated enum MetricRange: String, CaseIterable, Identifiable, Sendable {
    case hour = "1h", sixHours = "6h", day = "24h"
    public var id: String { rawValue }
    public var title: String {
        switch self {
        case .hour: String(localized: "1 Hour")
        case .sixHours: String(localized: "6 Hours")
        case .day: String(localized: "24 Hours")
        }
    }
    public var seconds: TimeInterval {
        switch self {
        case .hour: 3600
        case .sixHours: 6 * 3600
        case .day: 24 * 3600
        }
    }
}

/// How worrying a value is: the dashboard highlights a card (with an icon and words, never color alone).
public nonisolated enum MetricLevel: Int, Comparable, Sendable {
    case normal, warning, critical
    public static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }

    /// CPU / memory / disk in percent; warning from `warn`, critical from `crit`.
    public static func of(_ v: Double?, warn: Double, crit: Double) -> MetricLevel {
        guard let v else { return .normal }
        return v >= crit ? .critical : v >= warn ? .warning : .normal
    }
}

/// What the dashboard calls out on a machine, worst first: CPU ≥ 75 / 90 %, memory ≥ 80 / 90 %, disk ≥ 80 / 90 %,
/// load ≥ 1 / 2 per core (docs/server/machines.md「Alerts」).
public nonisolated struct MetricAlert: Hashable, Sendable {
    public var label: String
    public var level: MetricLevel

    public init(label: String, level: MetricLevel) {
        self.label = label
        self.level = level
    }

    public static func of(_ s: MetricSample, cores: Int?) -> [MetricAlert] {
        var out: [MetricAlert] = []
        let cpu = MetricLevel.of(s.cpu, warn: 75, crit: 90)
        if cpu > .normal { out.append(.init(label: String(localized: "High CPU"), level: cpu)) }
        let mem = MetricLevel.of(s.memPercent, warn: 80, crit: 90)
        if mem > .normal { out.append(.init(label: String(localized: "Low memory"), level: mem)) }
        let disk = MetricLevel.of(s.diskPercent, warn: 80, crit: 90)
        if disk > .normal { out.append(.init(label: String(localized: "Disk almost full"), level: disk)) }
        if let load = s.load, let cores, cores > 0 {
            let l = MetricLevel.of(load / Double(cores), warn: 1, crit: 2)
            if l > .normal { out.append(.init(label: String(localized: "High load"), level: l)) }
        }
        return out.sorted { $0.level > $1.level }
    }
}

public nonisolated enum MetricFormat {
    /// "41.2 GB", "512 MB" (binary units, like Activity Monitor and free -h).
    public static func bytes(_ v: Double) -> String {
        let units = ["B", "KB", "MB", "GB", "TB"]
        var x = v, i = 0
        // Pick the unit and the decimals from the number as it will be shown, after rounding: 1023.6 KB is
        // "1.00 MB", not "1024 KB"; 99.97 GB is "100 GB", not "100.0 GB".
        while i < units.count - 1 && x >= (i == 0 ? 1024 : 1023.5) { x /= 1024; i += 1 }
        if i == 0 { return "\(Int(x)) B" }
        return x >= 99.95 ? String(format: "%.0f %@", x, units[i]) : x >= 9.995 ? String(format: "%.1f %@", x, units[i]) : String(format: "%.2f %@", x, units[i])
    }

    public static func bytes(_ v: Int64) -> String { bytes(Double(v)) }

    /// A rate: "1.2 MB/s".
    public static func rate(_ v: Double?) -> String { v.map { bytes($0) + "/s" } ?? "—" }

    /// A percent: "37%", "4.5%" under 10.
    public static func percent(_ v: Double?) -> String {
        guard let v else { return "—" }
        return v < 10 && v > 0 ? String(format: "%.1f%%", v) : String(format: "%.0f%%", v)
    }

    public static func load(_ v: Double?) -> String { v.map { String(format: $0 >= 10 ? "%.1f" : "%.2f", $0) } ?? "—" }

    /// "3 d 4 hr", "5 hr 12 min", "8 min".
    public static func uptime(since boot: Date, now: Date = .now) -> String {
        let s = max(0, Int(now.timeIntervalSince(boot)))
        let d = s / 86400, h = s % 86400 / 3600, m = s % 3600 / 60
        if d > 0 { return h > 0 ? String(localized: "\(d)d \(h)h") : String(localized: "\(d)d") }
        if h > 0 { return String(localized: "\(h)h \(m)m [Machine Metrics 204e89]") }
        return String(localized: "\(m) min [Task Views 135d12]")
    }
}
