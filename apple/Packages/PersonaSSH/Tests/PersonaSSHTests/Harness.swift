import Foundation
@testable import PersonaSSH

/// A real OpenSSH server for the tests: the system's sshd run as this user on a free loopback port, with its own host
/// key and authorized_keys in a temporary directory (no root, nothing of the machine's own ssh setup touched).
final class TestSSHD: @unchecked Sendable {
    let dir: URL
    let port: Int
    /// `SHA256:…` of its host key, as `ssh-keygen -lf` prints it.
    let fingerprint: String
    private var process: Process?
    let user = NSUserName()

    init(authorizedKeys: [String]) throws {
        dir = FileManager.default.temporaryDirectory.appending(path: "persona-sshd-\(UUID().uuidString.prefix(8))")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try Shell.run("/usr/bin/ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-C", "test-host", "-f", dir.appending(path: "hostkey").path])
        let printed = try Shell.run("/usr/bin/ssh-keygen", ["-lf", dir.appending(path: "hostkey.pub").path])
        fingerprint = String(printed.split(separator: " ")[1])
        try (authorizedKeys.joined(separator: "\n") + "\n").write(to: dir.appending(path: "authorized_keys"), atomically: true, encoding: .utf8)
        port = try FreePort.pick()
        let config = """
        Port \(port)
        ListenAddress 127.0.0.1
        HostKey \(dir.appending(path: "hostkey").path)
        PidFile \(dir.appending(path: "sshd.pid").path)
        AuthorizedKeysFile \(dir.appending(path: "authorized_keys").path)
        StrictModes no
        UsePAM no
        PasswordAuthentication no
        KbdInteractiveAuthentication no
        PerSourcePenalties no
        MaxStartups 100
        LogLevel VERBOSE
        """
        try config.write(to: dir.appending(path: "sshd_config"), atomically: true, encoding: .utf8)
        try start()
        trace("sshd \(port) up")
    }

    func start() throws {
        let p = Process()
        p.executableURL = URL(filePath: "/usr/sbin/sshd")
        p.arguments = ["-D", "-e", "-f", dir.appending(path: "sshd_config").path]
        let log = dir.appending(path: "sshd.log")
        if !FileManager.default.fileExists(atPath: log.path) { FileManager.default.createFile(atPath: log.path, contents: nil) }
        let handle = try FileHandle(forWritingTo: log)
        handle.seekToEndOfFile()
        p.standardError = handle
        p.standardOutput = handle
        try p.run()
        process = p
        try FreePort.waitListening(port)
    }

    /// Ends every logged-in session (the connections drop as if the network went), the server keeps listening. A
    /// session is two processes (privilege separation): the unprivileged one holds the socket, so both go.
    func dropSessions() throws {
        guard let pid = process?.processIdentifier else { return }
        for p in Self.descendants(of: pid) { kill(p, SIGKILL) }
    }

    static func descendants(of pid: pid_t) -> [pid_t] {
        let children = ((try? Shell.run("/usr/bin/pgrep", ["-P", String(pid)], allowFailure: true)) ?? "")
            .split(separator: "\n").compactMap { pid_t($0) }
        return children + children.flatMap { descendants(of: $0) }
    }

    /// Down, sessions and all.
    func stop() {
        trace("sshd \(port) stop")
        try? dropSessions()
        guard let p = process else { return }
        process = nil
        p.terminate()
        // Usually gone at once; now and then it is not (seen once in a full run): never wait for it forever.
        let deadline = Date.now + 3
        while p.isRunning, Date.now < deadline { usleep(20_000) }
        if p.isRunning { kill(p.processIdentifier, SIGKILL) }
    }

    var log: String { (try? String(contentsOf: dir.appending(path: "sshd.log"), encoding: .utf8)) ?? "" }

    /// Preserve timing and connection/authentication events, not the fixture username or public-key fingerprint.
    /// These are test-generated credentials; the snapshot still redacts them before entering the retained trace.
    /// Bound the redacted payload, not raw input: both line and UTF-8 byte caps get an explicit truncation marker.
    func traceFailure() {
        let limit = 65_536
        var bytes = 0
        var truncated = false
        for (index, line) in log.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
            if index == 1000 {
                truncated = true
                break
            }
            let redacted = line
                .replacingOccurrences(of: user, with: "<test-user>")
                .replacingOccurrences(of: "SHA256:[A-Za-z0-9+/=]+", with: "<key-id>", options: .regularExpression)
            let size = redacted.utf8.count
            if bytes + size > limit {
                truncated = true
                break
            }
            trace("sshd failure | \(redacted)")
            bytes += size
        }
        if truncated { trace("sshd failure | snapshot truncated at 1000 lines or \(limit) bytes") }
    }

    deinit {
        stop()
        try? FileManager.default.removeItem(at: dir)
    }
}

/// The repository's mock hub (scripts/mock/mock_hub.py) on a free port.
final class TestHub: @unchecked Sendable {
    let port: Int
    private let process: Process

    init() throws {
        let script = URL(filePath: #filePath).deletingLastPathComponent().appending(path: "../../../../../scripts/mock/mock_hub.py").standardized
        let p = Process()
        p.executableURL = URL(filePath: "/usr/bin/env")
        p.arguments = ["python3", "-u", script.path]
        var env = ProcessInfo.processInfo.environment
        env["PORT"] = "0"
        p.environment = env
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        trace("mock spawn start")
        try p.run()
        process = p
        trace("mock waiting for banner")
        // "mock hub on 127.0.0.1:<port>"
        var seen = ""
        let deadline = Date.now + 20
        while Date.now < deadline {
            let chunk = out.fileHandleForReading.availableData
            if chunk.isEmpty { break }
            trace("mock stdout received")
            seen += String(decoding: chunk, as: UTF8.self)
            if let line = seen.split(separator: "\n").first(where: { $0.contains("mock hub on") }),
               let p = Int(line.split(separator: ":").last ?? "") {
                port = p
                trace("mock banner received")
                trace("hub \(p) up")
                return
            }
        }
        p.terminate()
        throw TestError("mock hub did not start: \(seen)")
    }

    deinit {
        trace("hub \(port) down")
        process.terminate()
        let deadline = Date.now + 3
        while process.isRunning, Date.now < deadline { usleep(20_000) }
        if process.isRunning { kill(process.processIdentifier, SIGKILL) }
    }
}

struct TestError: Error, CustomStringConvertible {
    var description: String
    init(_ s: String) { description = s }
}

enum Shell {
    @discardableResult
    static func run(_ path: String, _ args: [String], allowFailure: Bool = false) throws -> String {
        let command = URL(filePath: path).lastPathComponent
        trace("command \(command) start")
        defer { trace("command \(command) end") }
        let p = Process()
        p.executableURL = URL(filePath: path)
        p.arguments = args
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        try p.run()
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        if p.terminationStatus != 0 && !allowFailure { throw TestError("\(path) \(args) exited \(p.terminationStatus)") }
        return String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

enum FreePort {
    /// A port nothing listens on right now.
    static func pick() throws -> Int {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let bound = withUnsafeMutablePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, len) == 0 && getsockname(fd, $0, &len) == 0 }
        }
        guard bound else { throw TestError("no free port") }
        return Int(UInt16(bigEndian: addr.sin_port))
    }

    static func waitListening(_ port: Int, timeout: TimeInterval = 5) throws {
        let deadline = Date.now + timeout
        while Date.now < deadline {
            if isListening(port) { return }
            usleep(50_000)
        }
        throw TestError("nothing listens on \(port)")
    }

    static func isListening(_ port: Int) -> Bool {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = UInt16(port).bigEndian
        return withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) == 0 }
        }
    }
}
