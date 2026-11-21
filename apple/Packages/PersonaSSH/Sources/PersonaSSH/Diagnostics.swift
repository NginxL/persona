import Foundation

/// Opt-in lifecycle diagnostics for the native integration tests. Production leaves this unset and writes nothing.
/// No traffic, credentials or keys are intentionally recorded; existing error descriptions may name an endpoint.
private let traceLock = NSLock()

/// An opaque, in-process label, only present when tracing is enabled. Never derive it from an address, account,
/// host, port or key.
func traceID(_ prefix: String) -> String? {
    guard ProcessInfo.processInfo.environment["PERSONA_SSH_TRACE"] != nil else { return nil }
    return prefix + "-" + UUID().uuidString.prefix(8).lowercased()
}

func trace(_ message: @autoclosure () -> String) {
    guard let path = ProcessInfo.processInfo.environment["PERSONA_SSH_TRACE"] else { return }
    traceLock.withLock {
        let line = "\(Date.now.formatted(Date.ISO8601FormatStyle(includingFractionalSeconds: true))) \(message())\n"
        if !FileManager.default.fileExists(atPath: path) { FileManager.default.createFile(atPath: path, contents: nil) }
        guard let handle = FileHandle(forWritingAtPath: path) else { return }
        handle.seekToEndOfFile()
        handle.write(Data(line.utf8))
        try? handle.close()
    }
}
