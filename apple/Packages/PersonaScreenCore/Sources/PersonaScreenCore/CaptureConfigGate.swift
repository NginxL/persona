import Foundation

/// When Persona Screen may hand ScreenCaptureKit a new configuration (#7856). A change made before `startCapture` has
/// completed can be lost: the stream keeps the configuration it was made with, so capture went on at the start's frame
/// rate (and size) after a client's first tier asked for less. Such a change waits and is applied once capture runs; one
/// waiting change carries them all, since the configuration it applies is the latest.
public struct CaptureConfigGate: Equatable, Sendable {
    public private(set) var started = false
    public private(set) var pending = false

    public init() {}

    /// The configuration changed: true to apply it now; false when it waits for capture to start.
    public mutating func changed() -> Bool {
        if started { return true }
        pending = true
        return false
    }

    /// `startCapture` completed without an error: true when a change waits and is to be applied now.
    public mutating func captureStarted() -> Bool {
        started = true
        defer { pending = false }
        return pending
    }

    /// A new capture (or none): it starts again from not started, nothing waiting.
    public mutating func reset() { self = CaptureConfigGate() }
}
