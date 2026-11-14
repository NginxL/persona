import Foundation

/// Bandwidth adaptation from the phone's receiver reports (#3954, docs/extensions/computer-use.md "Adaptation"): it
/// turns each report into a queueing delay and cuts the bitrate 30% over 150 ms, skips frames over 500 ms, and raises
/// it 10% after 2 s under 50 ms, never past the phone's cap.
///
/// The phone stamps arrivals on its clock and the helper stamps captures on its own, so a report's one-way delay
/// carries an unknown offset. The queueing delay is that delay above the lowest one seen over the last 10 s: the offset
/// cancels, and a slow drift between the clocks ages out of the window.
///
/// Times are microseconds on the helper's monotonic clock (`now`). The struct holds no clock of its own, so a test
/// drives it with any timeline.
public struct RateController {
    /// What one report changed.
    public struct Decision: Equatable {
        /// The new target bitrate in kbit/s, or nil when it stays.
        public var kbps: Int?
        /// Frames are being skipped until the queue drains (`admit` says which ones still go).
        public var skipping: Bool
        /// The phone could not decode a frame: the next one should be a keyframe.
        public var keyframe: Bool

        public init(kbps: Int? = nil, skipping: Bool = false, keyframe: Bool = false) {
            self.kbps = kbps
            self.skipping = skipping
            self.keyframe = keyframe
        }
    }

    public static let cutAboveMicros: Int64 = 150_000
    public static let skipAboveMicros: Int64 = 500_000
    public static let raiseBelowMicros: Int64 = 50_000
    /// How long the queue must stay under `raiseBelowMicros` before each raise.
    public static let calmMicros: UInt64 = 2_000_000
    /// After a cut, the next waits this long: one congested moment cuts once, not once for every report it delays.
    public static let cutHoldMicros: UInt64 = 500_000
    /// While skipping, one frame still goes this often, so reports keep coming and say when the queue has drained.
    public static let probeMicros: UInt64 = 250_000
    /// The window the one-way delay's floor is taken over.
    public static let baselineMicros: UInt64 = 10_000_000

    /// The current target bitrate in kbit/s.
    public private(set) var kbps: Int
    /// The phone's cap in kbit/s.
    public private(set) var cap: Int
    public private(set) var skipping = false

    /// The window's one-way delays, increasing, each with when it came: the first is the window's lowest.
    private var floor: [(at: UInt64, delay: Int64)] = []
    private var lastCut: UInt64?
    private var calmSince: UInt64?
    private var lastProbe: UInt64?

    /// - Parameter cap: the phone's `max_kbps`; the stream starts at `CapturePlan.initialKbps(cap:)`.
    public init(cap: Int) {
        self.cap = CapturePlan.capKbps(cap)
        kbps = CapturePlan.initialKbps(cap: cap)
    }

    /// A new cap from the phone (`update.max_kbps`). A cap below the current bitrate takes effect at once and is
    /// returned; a higher one is reached by raising as usual.
    public mutating func setCap(_ requested: Int) -> Int? {
        cap = CapturePlan.capKbps(requested)
        guard kbps > cap else { return nil }
        kbps = cap
        return kbps
    }

    /// The queueing delay of a report with this one-way delay, in µs, after adding it to the window.
    private mutating func queueing(delay: Int64, now: UInt64) -> Int64 {
        while let last = floor.last, last.delay >= delay { floor.removeLast() }
        floor.append((now, delay))
        while floor.count > 1, now &- floor[0].at > Self.baselineMicros { floor.removeFirst() }
        return delay - floor[0].delay
    }

    /// One receiver report, received at `now`.
    public mutating func receive(_ report: ReceiverReport, now: UInt64) -> Decision {
        let delay = Int64(truncatingIfNeeded: report.arrivalMicros) - Int64(truncatingIfNeeded: report.captureMicros)
        let queue = queueing(delay: delay, now: now)
        var decision = Decision(keyframe: report.flags.contains(.decodingError))

        if queue > Self.skipAboveMicros {
            skipping = true
        } else if skipping, queue <= Self.cutAboveMicros {
            skipping = false
            lastProbe = nil
        }

        if queue > Self.cutAboveMicros {
            calmSince = nil
            if lastCut.map({ now &- $0 >= Self.cutHoldMicros }) ?? true {
                let cut = max(CapturePlan.minKbps, kbps * 7 / 10)
                lastCut = now
                if cut != kbps {
                    kbps = cut
                    decision.kbps = cut
                }
            }
        } else if queue < Self.raiseBelowMicros {
            let since = calmSince ?? now
            calmSince = since
            if now &- since >= Self.calmMicros {
                calmSince = now // the next raise needs another calm stretch
                let raised = min(cap, (kbps * 11 + 9) / 10)
                if raised != kbps {
                    kbps = raised
                    decision.kbps = raised
                }
            }
        } else {
            calmSince = nil
        }
        decision.skipping = skipping
        return decision
    }

    /// Whether a frame captured at `now` should be encoded: always, but while skipping only one per `probeMicros`.
    public mutating func admit(now: UInt64) -> Bool {
        guard skipping else { return true }
        if let last = lastProbe, now &- last < Self.probeMicros { return false }
        lastProbe = now
        return true
    }
}
