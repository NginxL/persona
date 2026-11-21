import Foundation
import Network
import Synchronization

/// Accepts TCP and reads the client's greeting but never answers it. Tests can stop a real SSH handshake at a
/// known point without a firewall, a remote server, or a race with a connection-refused error.
final class QuietSSHServer: @unchecked Sendable {
    private struct State {
        var connections: [NWConnection] = []
        var closed: Set<ObjectIdentifier> = []
    }
    private let state = Mutex(State())
    private let listener: NWListener
    private let queue = DispatchQueue(label: "persona.tests.quiet-ssh")

    init() async throws {
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
        listener.newConnectionHandler = { [weak self] connection in
            guard let self else { connection.cancel(); return }
            self.state.withLock { $0.connections.append(connection) }
            connection.start(queue: self.queue)
            self.receive(connection)
        }
        let waiting = Mutex<(continuation: CheckedContinuation<Void, Error>?, cancelled: Bool)>((nil, false))
        try await withTaskCancellationHandler {
            try Task.checkCancellation()
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
                let canStart = waiting.withLock { value in
                    guard !value.cancelled else { return false }
                    value.continuation = continuation
                    return true
                }
                guard canStart else { continuation.resume(throwing: CancellationError()); return }
                listener.stateUpdateHandler = { [weak self] status in
                    let result: Result<Void, Error>
                    switch status {
                    case .ready: result = .success(())
                    case .failed(let error), .waiting(let error): result = .failure(error)
                    case .cancelled: result = .failure(CancellationError())
                    default: return
                    }
                    let ready = waiting.withLock { value in defer { value.continuation = nil }; return value.continuation }
                    ready?.resume(with: result)
                    if case .failure = result { self?.listener.cancel() }
                }
                listener.start(queue: queue)
            }
            try Task.checkCancellation()
        } onCancel: {
            let ready = waiting.withLock { value in
                value.cancelled = true
                defer { value.continuation = nil }
                return value.continuation
            }
            self.listener.cancel()
            // Do not depend on Network.framework delivering .cancelled: setup may not have started yet.
            ready?.resume(throwing: CancellationError())
        }
    }

    var port: Int { Int(listener.port!.rawValue) }
    var accepted: Int { state.withLock { $0.connections.count } }
    var closed: Int { state.withLock { $0.closed.count } }

    private func receive(_ connection: NWConnection) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 65_536) { [weak self] _, _, complete, error in
            guard let self else { return }
            if complete || error != nil {
                self.state.withLock { _ = $0.closed.insert(ObjectIdentifier(connection)) }
                connection.cancel()
            } else {
                self.receive(connection)
            }
        }
    }

    func stop() {
        listener.cancel()
        for connection in state.withLock({ $0.connections }) { connection.cancel() }
    }

    deinit { stop() }
}
