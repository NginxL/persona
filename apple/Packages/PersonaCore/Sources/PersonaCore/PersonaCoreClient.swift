import Foundation
import PersonaCoreC
import Synchronization
@_exported import PersonaCoreWire

/// Host callbacks arrive on the Rust worker thread, not the main actor. They must return promptly and must not free
/// or synchronously flush the client that is invoking them. Network and other blocking work belongs to the host.
public struct PersonaCoreCallbacks: Sendable {
    public var changed: @Sendable (UInt64) -> Void
    public var run: @Sendable (Effect) throws -> Void

    public init(changed: @escaping @Sendable (UInt64) -> Void = { _ in },
                run: @escaping @Sendable (Effect) throws -> Void) {
        self.changed = changed
        self.run = run
    }
}

public enum PersonaCoreError: Error, Equatable {
    case creationFailed
    case closed
    case dispatchFailed
    case flushTimedOut
    case invalidPatch
    case callbackFailed(String)
}

private final class CallbackBox: @unchecked Sendable {
    let callbacks: PersonaCoreCallbacks
    let failures = Mutex<[String]>([])
    init(_ callbacks: PersonaCoreCallbacks) { self.callbacks = callbacks }
}

/// An owned C core handle. Value codecs are Sendable; this class serializes only handle lifetime and calls, never
/// the worker's state. Call `close` on a thread other than a host callback when an orderly stop is required.
private struct NativeHandle: @unchecked Sendable {
    let raw: OpaquePointer
}

public final class PersonaCoreClient: @unchecked Sendable {
    private let lock = NSLock()
    private var handle: OpaquePointer?
    private let callbackBox: CallbackBox

    public init(config: Config, callbacks: PersonaCoreCallbacks) throws {
        let box = CallbackBox(callbacks)
        callbackBox = box
        let context = Unmanaged.passRetained(box).toOpaque()
        var host = PersonaCoreHost(context: context, changed: { context, version in
            guard let context else { return }
            Unmanaged<CallbackBox>.fromOpaque(context).takeUnretainedValue().callbacks.changed(version)
        }, run: { context, bytes, length in
            guard let context else { return }
            let box = Unmanaged<CallbackBox>.fromOpaque(context).takeUnretainedValue()
            let data = bytes.map { Data(bytes: $0, count: length) } ?? Data()
            guard let effect = try? Effect.bincodeDeserialize(input: [UInt8](data)) else {
                box.failures.withLock { $0.append("invalid effect bytes") }
                return
            }
            do { try box.callbacks.run(effect) } catch {
                box.failures.withLock { $0.append(String(describing: error)) }
            }
        }, release: { context in
            guard let context else { return }
            _ = Unmanaged<CallbackBox>.fromOpaque(context).takeRetainedValue()
        })
        let bytes = try config.bincodeSerialize()
        handle = bytes.withUnsafeBufferPointer { buffer in
            persona_core_new(host, buffer.baseAddress, buffer.count)
        }
        guard handle != nil else {
            // `persona_core_new` consumes the host even when config decoding or worker creation fails.
            host.context = nil
            throw PersonaCoreError.creationFailed
        }
    }

    deinit {
        close()
    }

    public func dispatch(_ action: Action) throws {
        let bytes = try action.bincodeSerialize()
        try withHandle { handle in
            try bytes.withUnsafeBufferPointer { buffer in
                guard persona_core_dispatch(handle, buffer.baseAddress, buffer.count) else {
                    throw PersonaCoreError.dispatchFailed
                }
            }
        }
    }

    public func drain(chat: String) throws -> ChatPatch {
        let chat = Array(chat.utf8)
        return try withHandle { handle in
            let bytes = chat.withUnsafeBufferPointer { buffer in
                persona_core_drain(handle, buffer.baseAddress, buffer.count)
            }
            defer { persona_core_bytes_free(bytes) }
            guard let data = bytes.data else { throw PersonaCoreError.invalidPatch }
            return try ChatPatch.bincodeDeserialize(input: Array(UnsafeBufferPointer(start: data, count: bytes.length)))
        }
    }

    /// Tests only: never wait for the worker on the UI thread or inside a host callback.
    public func flush(timeoutMilliseconds: UInt32 = 5_000) throws {
        try withHandle { handle in
            guard persona_core_flush(handle, timeoutMilliseconds) else { throw PersonaCoreError.flushTimedOut }
            let failures = callbackBox.failures.withLock { $0 }
            guard failures.isEmpty else { throw PersonaCoreError.callbackFailed(failures.joined(separator: "; ")) }
        }
    }

    public func close() {
        lock.lock()
        let handle = handle
        self.handle = nil
        lock.unlock()
        guard let handle else { return }
        if Thread.isMainThread {
            let handle = NativeHandle(raw: handle)
            DispatchQueue.global(qos: .utility).async {
                persona_core_free(handle.raw)
            }
        } else {
            persona_core_free(handle)
        }
    }

    private func withHandle<T>(_ work: (OpaquePointer) throws -> T) throws -> T {
        lock.lock()
        defer { lock.unlock() }
        guard let handle else { throw PersonaCoreError.closed }
        return try work(handle)
    }
}
