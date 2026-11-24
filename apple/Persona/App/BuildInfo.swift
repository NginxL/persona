import Foundation

/// What this build was built from, as the build stamped it into the Info.plist (#12926): the only reader of those
/// keys. Release builds have `PersonaCommit` (apple/project.yml "Stamp the commit": the short hash, `+` when the
/// working tree had uncommitted changes); Debug builds have the `PersonaBuildInfo` dictionary
/// (`scripts/app/stamp_debug_build_info.sh`). Every field is optional: a build that could not learn it leaves it out.
nonisolated struct BuildInfo: Codable, Equatable, Sendable {
    var commit: String?
    /// nil: not known (no commit, or git could not tell).
    var dirty: Bool?
    var branch: String?
    var builtAt: Date?
    /// The machine it was built on.
    var builder: String?
    var xcode: String?
    var swift: String?
    var configuration: String?
    var bundleID: String?

    enum CodingKeys: String, CodingKey {
        case commit, dirty, branch, builtAt = "built_at", builder, xcode, swift, configuration, bundleID = "bundle_id"
    }

    init(commit: String? = nil, dirty: Bool? = nil, branch: String? = nil, builtAt: Date? = nil, builder: String? = nil,
         xcode: String? = nil, swift: String? = nil, configuration: String? = nil, bundleID: String? = nil) {
        self.commit = commit; self.dirty = dirty; self.branch = branch; self.builtAt = builtAt; self.builder = builder
        self.xcode = xcode; self.swift = swift; self.configuration = configuration; self.bundleID = bundleID
    }

    static let debugKey = "PersonaBuildInfo"
    static let releaseCommitKey = "PersonaCommit"

    static let current = BuildInfo(info: Bundle.main.infoDictionary ?? [:])

    init(info: [String: Any]) {
        let debug = info[Self.debugKey] as? [String: Any] ?? [:]
        func text(_ value: Any?) -> String? {
            guard let s = (value as? String)?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty,
                  s.lowercased() != "unknown" else { return nil }
            return s
        }
        if let commit = text(debug["commit"]) {
            self.commit = commit
            dirty = debug["dirty"] as? Bool
        } else if var stamp = text(info[Self.releaseCommitKey]) {
            let plus = stamp.hasSuffix("+")
            if plus { stamp.removeLast() }
            commit = text(stamp)
            dirty = commit == nil ? nil : plus
        }
        branch = text(debug["branch"])
        builtAt = text(debug["builtAt"]).flatMap { try? Date($0, strategy: .iso8601) }
        builder = text(debug["builder"])
        xcode = text(debug["xcode"])
        swift = text(debug["swift"])
        configuration = text(debug["configuration"])
        bundleID = text(info["CFBundleIdentifier"])
    }

    /// `3518b00185`, `3518b00185-dirty`; nil without a commit.
    var commitLabel: String? {
        commit.map { dirty == true ? "\($0)-dirty" : $0 }
    }

    /// About's Build row: `3518b00185 · 11-22 15:00` (local time), the commit alone without a build time, and
    /// `unknown` (localized by the caller) when there is no commit either.
    func buildLabel(unknown: String, timeZone: TimeZone = .current) -> String {
        guard let commitLabel else { return builtAt.map { "\(unknown) · \(Self.shortTime($0, timeZone))" } ?? unknown }
        guard let builtAt else { return commitLabel }
        return "\(commitLabel) · \(Self.shortTime(builtAt, timeZone))"
    }

    /// `11-22 15:00`.
    static func shortTime(_ date: Date, _ timeZone: TimeZone = .current) -> String {
        date.formatted(Date.VerbatimFormatStyle(
            format: "\(month: .twoDigits)-\(day: .twoDigits) \(hour: .twoDigits(clock: .twentyFourHour, hourCycle: .zeroBased)):\(minute: .twoDigits)",
            timeZone: timeZone, calendar: Calendar(identifier: .gregorian)))
    }

    /// `2026-11-22 15:00:07 +0800`: the debug info's full times.
    static func fullTime(_ date: Date, _ timeZone: TimeZone = .current) -> String {
        date.formatted(Date.VerbatimFormatStyle(
            format: "\(year: .defaultDigits)-\(month: .twoDigits)-\(day: .twoDigits) \(hour: .twoDigits(clock: .twentyFourHour, hourCycle: .zeroBased)):\(minute: .twoDigits):\(second: .twoDigits) \(timeZone: .iso8601(.short))",
            timeZone: timeZone, calendar: Calendar(identifier: .gregorian)))
    }
}

/// When this process started (the kernel's record, so it is the launch however late this is first read); nil if the
/// kernel does not say.
nonisolated enum ProcessLaunch {
    static let date: Date? = {
        var info = kinfo_proc()
        var size = MemoryLayout<kinfo_proc>.stride
        var mib: [Int32] = [CTL_KERN, KERN_PROC, KERN_PROC_PID, getpid()]
        guard sysctl(&mib, u_int(mib.count), &info, &size, nil, 0) == 0, size > 0 else { return nil }
        let start = info.kp_proc.p_starttime
        guard start.tv_sec > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(start.tv_sec) + TimeInterval(start.tv_usec) / 1_000_000)
    }()
}

/// The app's debug info (#12926): one value behind Settings › About's Debug info rows, the text its Copy All puts on
/// the clipboard (`json`) and the `build` / `runtime` fields of the `phone_device_info` tool, so a person and an Agent
/// read the same thing. Missing values are left out of the JSON. No secret: the hub is its host only, never the URL's
/// token, user, path or query, and no token, cookie or key is in it.
nonisolated struct AppDebugInfo: Codable, Equatable, Sendable {
    struct App: Codable, Equatable, Sendable {
        var version: String?
        var build: String?
    }

    struct Runtime: Codable, Equatable, Sendable {
        /// `hub.example.com:8443`.
        var hubHost: String?
        var hubVersion: String?
        /// idle | connecting | connected | failed: the live connection to the hub.
        var hubConnection: String?
        var launchedAt: Date?

        enum CodingKeys: String, CodingKey {
            case hubHost = "hub_host", hubVersion = "hub_version", hubConnection = "hub_connection", launchedAt = "launched_at"
        }
    }

    struct Device: Codable, Equatable, Sendable {
        /// The hardware identifier (iPhone17,1).
        var model: String?
        /// `ios 26.0.1`.
        var system: String?
    }

    var app: App
    var build: BuildInfo
    var runtime: Runtime
    var device: Device

    /// Sorted keys, ISO 8601 times in UTC: what Copy All copies and the tool returns.
    static func encoder(pretty: Bool = true) -> JSONEncoder {
        let encoder = JSONEncoder()
        encoder.outputFormatting = pretty ? [.sortedKeys, .prettyPrinted, .withoutEscapingSlashes] : [.sortedKeys, .withoutEscapingSlashes]
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }

    /// The text Copy All puts on the clipboard.
    var json: String {
        (try? Self.encoder().encode(self)).map { String(decoding: $0, as: UTF8.self) } ?? "{}"
    }

    /// `hub.example.com:8443`: the host and port only.
    static func host(_ url: URL?) -> String? {
        guard let url, let comps = URLComponents(url: url, resolvingAgainstBaseURL: false), let host = comps.host, !host.isEmpty
        else { return nil }
        let shown = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
        return comps.port.map { "\(shown):\($0)" } ?? shown
    }

    struct Row: Equatable, Sendable, Identifiable {
        var label: String
        var value: String
        var id: String { label }
    }

    /// About's rows, in order; `unknown` where a value is missing.
    func rows(_ labels: Labels, timeZone: TimeZone = .current) -> [Row] {
        func or(_ s: String?) -> String { s.flatMap { $0.isEmpty ? nil : $0 } ?? labels.unknown }
        let version = app.version.map { v in app.build.map { "\(v) (\($0))" } ?? v }
        return [
            Row(label: labels.version, value: or(version)),
            Row(label: labels.commit, value: or(build.commitLabel)),
            Row(label: labels.branch, value: or(build.branch)),
            Row(label: labels.builtAt, value: or(build.builtAt.map { BuildInfo.fullTime($0, timeZone) })),
            Row(label: labels.builder, value: or(build.builder)),
            Row(label: labels.xcode, value: or(build.xcode)),
            Row(label: labels.swift, value: or(build.swift)),
            Row(label: labels.configuration, value: or(build.configuration)),
            Row(label: labels.bundleID, value: or(build.bundleID)),
            Row(label: labels.hub, value: or(runtime.hubHost)),
            Row(label: labels.hubVersion, value: or(runtime.hubVersion)),
            Row(label: labels.hubConnection, value: or(runtime.hubConnection)),
            Row(label: labels.launchedAt, value: or(runtime.launchedAt.map { BuildInfo.fullTime($0, timeZone) })),
            Row(label: labels.device, value: or(device.model)),
            Row(label: labels.system, value: or(device.system))
        ]
    }

    struct Labels: Sendable {
        var unknown, version, commit, branch, builtAt, builder, xcode, swift, configuration, bundleID: String
        var hub, hubVersion, hubConnection, launchedAt, device, system: String
    }
}

extension AppDebugInfo {
    /// This app's debug info now. Asks the hub for its version when the app has not heard it yet (`hubUpdate`).
    @MainActor static func current(_ model: AppModel, askHub: Bool = true) async -> AppDebugInfo {
        var hubVersion = model.hubUpdate?.version
        if hubVersion == nil, askHub, !model.isDemo, let api = model.api { hubVersion = (try? await api.hubUpdate())?.version }
        let state: String = switch model.live.state {
        case .idle: "idle"
        case .connecting: "connecting"
        case .connected: "connected"
        case .failed: "failed"
        }
        let connection = model.api == nil ? nil : state
        let info = Bundle.main.infoDictionary ?? [:]
        return AppDebugInfo(
            app: App(version: info["CFBundleShortVersionString"] as? String, build: info["CFBundleVersion"] as? String),
            build: .current,
            runtime: Runtime(hubHost: host(model.api?.config.url), hubVersion: hubVersion, hubConnection: connection,
                             launchedAt: ProcessLaunch.date),
            device: Device(model: ErrorReports.deviceModel, system: "\(ErrorReports.os) \(ErrorReports.osVersion)"))
    }
}
