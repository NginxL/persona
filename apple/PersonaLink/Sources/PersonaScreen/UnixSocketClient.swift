import Foundation
#if canImport(Darwin)
import Darwin
#endif

/// A blocking client for the node's per-stream Unix socket (0600, one per stream; `design-3954.protocol.md`, "Node").
/// The helper connects, sends its token as the first line, then reads and writes framed messages until the socket
/// closes.
final class UnixSocketClient {
    private let fd: Int32

    private init(fd: Int32) {
        self.fd = fd
    }

    enum SocketError: Error {
        case open(Int32)
        case connect(Int32)
        case write(Int32)
        case read(Int32)
        case pathTooLong
    }

    static func connect(path: String) throws -> UnixSocketClient {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        if fd < 0 {
            throw SocketError.open(errno)
        }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        let pathBytes = Array(path.utf8)
        if pathBytes.count >= capacity {
            Darwin.close(fd)
            throw SocketError.pathTooLong
        }
        withUnsafeMutablePointer(to: &addr.sun_path) { tuple in
            tuple.withMemoryRebound(to: UInt8.self, capacity: capacity) { dst in
                for (index, byte) in pathBytes.enumerated() {
                    dst[index] = byte
                }
                dst[pathBytes.count] = 0
            }
        }
        let size = socklen_t(MemoryLayout<sockaddr_un>.size)
        let result = withUnsafePointer(to: &addr) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                Darwin.connect(fd, sa, size)
            }
        }
        if result != 0 {
            let code = errno
            Darwin.close(fd)
            throw SocketError.connect(code)
        }
        return UnixSocketClient(fd: fd)
    }

    func writeAll(_ data: Data) throws {
        if data.isEmpty {
            return
        }
        try data.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            guard let base = raw.bindMemory(to: UInt8.self).baseAddress else {
                return
            }
            var offset = 0
            while offset < raw.count {
                let n = Darwin.write(fd, base.advanced(by: offset), raw.count - offset)
                if n <= 0 {
                    throw SocketError.write(errno)
                }
                offset += n
            }
        }
    }

    /// Reads whatever is available. An empty result means the peer closed the socket (EOF).
    func readChunk() throws -> Data {
        var buffer = [UInt8](repeating: 0, count: 64 * 1024)
        let n = buffer.withUnsafeMutableBytes { raw in
            Darwin.read(fd, raw.baseAddress, raw.count)
        }
        if n < 0 {
            throw SocketError.read(errno)
        }
        if n == 0 {
            return Data()
        }
        return Data(buffer[0 ..< n])
    }

    func close() {
        _ = Darwin.close(fd)
    }
}
