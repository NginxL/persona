import Foundation
import PersonaScreenKit
#if canImport(CoreGraphics)
import CoreGraphics
#endif

// Persona Screen: the capture-and-encode helper for the iOS live-screen video channel (#3954). It is an app bundle
// inside Persona Link.app so its Screen Recording grant is its own, not the node's (`design-3954.poc.md`). The node
// starts it through LaunchServices with a per-stream Unix socket and token.
//
// This is the skeleton: it carries the wire protocol end to end with a stub capture source that reports the Screen
// Recording permission as missing, so the phone stays on RFB. The real ScreenCaptureKit + VideoToolbox source is the
// PoC on a real Mac.

enum PersonaScreenMain {
    static func run(_ argv: [String]) -> Int32 {
        let args: HelperArguments
        do {
            args = try HelperArguments.parse(argv)
        } catch {
            warn("bad arguments: \(error)")
            return 2
        }
        switch args.mode {
        case .requestPermission:
            return requestPermission()
        case let .stream(socket, token):
            do {
                try runStream(socketPath: socket, token: token)
                return 0
            } catch {
                warn("stream ended: \(error)")
                return 1
            }
        }
    }

    /// `--request-permission`: ask macOS for Screen Recording. macOS shows its alert on the Mac's screen; only input
    /// at that Mac answers it (`design-3954.poc.md`, "Who clicks Allow"). Nothing here answers for the user.
    private static func requestPermission() -> Int32 {
        #if canImport(CoreGraphics)
        let granted = CGRequestScreenCaptureAccess()
        print("screen_recording: \(granted ? "granted" : "missing")")
        return granted ? 0 : 1
        #else
        warn("screen recording is a macOS capability")
        return 1
        #endif
    }

    private static func runStream(socketPath: String, token: String) throws {
        let socket = try UnixSocketClient.connect(path: socketPath)
        defer { socket.close() }
        try socket.writeAll(Data((token + "\n").utf8))

        // PoC (real Mac): a ScreenCaptureSource (ScreenCaptureKit + VideoToolbox) replaces this stub. The stub reports
        // the permission as missing on `start`, so the phone falls back to RFB.
        let source: ScreenSource = UnavailableScreenSource(reason: "permission")
        let writeLock = NSLock()
        source.onMessage = { message in
            let framed = message.framed()
            writeLock.lock()
            defer { writeLock.unlock() }
            try? socket.writeAll(framed)
        }
        defer { source.stop() }

        var parser = ScreenVideoParser()
        while true {
            let chunk = try socket.readChunk()
            if chunk.isEmpty {
                break // the node closed the stream
            }
            for raw in try parser.push(chunk) {
                let message = try ScreenVideoMessage.decode(type: raw.type, body: raw.body)
                HelperDispatch.handle(message, source: source)
            }
        }
    }

    private static func warn(_ text: String) {
        FileHandle.standardError.write(Data("persona-screen: \(text)\n".utf8))
    }
}

exit(PersonaScreenMain.run(Array(CommandLine.arguments.dropFirst())))
