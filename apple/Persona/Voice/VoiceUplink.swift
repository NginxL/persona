import Foundation

/// Little-endian PCM16 mono, the extension's audio both ways. Every Apple CPU is little-endian, so samples are copied as
/// they lie in memory.
nonisolated enum VoicePCM {
    static func data(_ samples: UnsafeBufferPointer<Int16>) -> Data {
        guard let base = samples.baseAddress, !samples.isEmpty else { return Data() }
        return Data(bytes: base, count: samples.count * MemoryLayout<Int16>.size)
    }

    static func data(_ samples: [Int16]) -> Data {
        samples.withUnsafeBufferPointer { data($0) }
    }

    /// Samples as Int16; a trailing odd byte is ignored.
    static func samples(_ data: Data) -> [Int16] {
        let count = data.count / 2
        guard count > 0 else { return [] }
        return data.withUnsafeBytes { raw in
            (0..<count).map { raw.loadUnaligned(fromByteOffset: $0 * 2, as: Int16.self) }
        }
    }

    /// Samples in [-1, 1).
    static func floats(_ data: Data) -> [Float] {
        samples(data).map { Float($0) / 32768 }
    }

    /// Silence of `bytes` bytes.
    static func silence(bytes: Int) -> Data {
        Data(count: max(0, bytes - bytes % 2))
    }

    /// Root mean square in [0, 1].
    static func rms(_ data: Data) -> Float {
        rms(floats(data))
    }

    /// The sum of the squared samples in [-1, 1), without making an array (the audio thread calls it per buffer).
    static func sumOfSquares(_ data: Data) -> Float {
        let count = data.count / 2
        guard count > 0 else { return 0 }
        return data.withUnsafeBytes { raw in
            var sum: Float = 0
            for i in 0..<count {
                let x = Float(raw.loadUnaligned(fromByteOffset: i * 2, as: Int16.self)) / 32768
                sum += x * x
            }
            return sum
        }
    }

    static func rms(_ samples: [Float]) -> Float {
        guard !samples.isEmpty else { return 0 }
        let sum = samples.reduce(Float(0)) { $0 + $1 * $1 }
        return min(1, (sum / Float(samples.count)).squareRoot())
    }

    /// `data` at `to` Hz instead of `from`, by linear interpolation: for the few seconds held before the extension said
    /// which rate it wants (speech, for a speech model; no filter needed for 16 ↔ 24 kHz).
    static func resample(_ data: Data, from: Int, to: Int) -> Data {
        guard from > 0, to > 0, from != to else { return data }
        let input = samples(data)
        guard input.count > 1 else { return data }
        let count = Int((Double(input.count) * Double(to) / Double(from)).rounded())
        guard count > 0 else { return Data() }
        let step = Double(from) / Double(to)
        var out = [Int16](repeating: 0, count: count)
        for i in 0..<count {
            let at = Double(i) * step
            let lo = min(Int(at), input.count - 1)
            let hi = min(lo + 1, input.count - 1)
            let frac = at - Double(lo)
            let value = Double(input[lo]) * (1 - frac) + Double(input[hi]) * frac
            out[i] = Int16(max(-32768, min(32767, value.rounded())))
        }
        return Self.data(out)
    }

    static func bytes(seconds: Double, rate: Int) -> Int {
        Int(seconds * Double(rate)) * 2
    }
}

/// Where the microphone's PCM goes (docs/assistant/voice.md 1.2 A「ready 之前的声音最多缓存 3 秒」): held, at most `seconds` of it
/// (the oldest dropped), until the extension is ready, then sent as it comes; held again while the call reconnects.
/// The audio thread pushes; the main actor opens, holds and closes it. Each piece keeps the rate it was captured at:
/// what was held before `ready` named the extension's rate is resampled to it on the way out.
///
/// Muting sends silence of the same length (docs/assistant/voice.md 1.2 K): the extension keeps hearing time pass and the upstream's
/// turn-taking does not stall. The input level (about 20 a second, for the orb) is measured before muting and is 0
/// while muted.
///
/// A meeting call's uplink is gated (#3862 phase 4, docs/assistant/meeting-minutes.md「Hearing only Talk」): nothing is
/// held or sent outside a Talk span (`talk(_:frame:)`), so the meeting's audio never reaches the voice service between
/// spans, before `ready` included.
nonisolated final class VoiceUplink: @unchecked Sendable {
    let seconds: Double
    /// Sends only inside a Talk span, and never holds (a meeting call).
    let gated: Bool
    private let lock = NSLock()
    private var talking = false
    private var held: [(pcm: Data, rate: Int)] = []
    private var sink: (@Sendable (Data) -> Void)?
    private var sinkRate = 0
    private var mutedFlag = false
    private var closed = false
    private var levelSink: (@Sendable (Float) -> Void)?
    private var levelSum: Float = 0
    private var levelCount = 0
    private var levelsOff = false

    /// What is sent at a time when held audio goes out: a tenth of a second.
    static let chunkSeconds = 0.1

    init(seconds: Double = 3, gated: Bool = false) {
        self.seconds = seconds
        self.gated = gated
    }

    /// A Talk span is open (gated uplinks only).
    var isTalking: Bool { lock.withLock { talking } }

    /// Opens or closes a Talk span of a gated uplink. `frame` sends the `talk` frame, under the same lock as every push:
    /// the socket keeps the order of its sends, so no buffer of the span goes before its `talk on` or after its
    /// `talk off` (the extension would replace it by silence and log a phone bug).
    func talk(_ on: Bool, frame: () -> Void) {
        lock.withLock {
            guard gated, !closed else { return }
            if on { frame(); talking = true } else { talking = false; frame() }
        }
    }

    var isMuted: Bool {
        get { lock.withLock { mutedFlag } }
        set { lock.withLock { mutedFlag = newValue } }
    }

    /// Whether pushes go out right now (false: held, or closed).
    var isOpen: Bool { lock.withLock { sink != nil } }

    /// Seconds of audio held, for the tests.
    var heldSeconds: Double {
        lock.withLock { held.reduce(0) { $0 + Double($1.pcm.count) / Double(max(1, $1.rate * 2)) } }
    }

    /// Called with the input level (0…1) about 20 times a second, on the audio thread.
    func onLevel(_ sink: @escaping @Sendable (Float) -> Void) {
        lock.withLock { levelSink = sink }
    }

    /// No level is measured or handed on (the app in the background, #9887: nobody sees the orb there).
    var levelsPaused: Bool {
        get { lock.withLock { levelsOff } }
        set {
            lock.withLock {
                levelsOff = newValue
                levelSum = 0
                levelCount = 0
            }
        }
    }

    /// One buffer from the microphone, PCM16 at `rate`. Any thread.
    func push(_ pcm: Data, rate: Int) {
        guard !pcm.isEmpty, rate > 0 else { return }
        let level: Float? = lock.withLock {
            guard !closed else { return nil }
            if !levelsOff {
                levelSum += VoicePCM.sumOfSquares(pcm)
                levelCount += pcm.count / 2
            }
            let out = mutedFlag ? VoicePCM.silence(bytes: pcm.count) : pcm
            if gated {
                // Outside a span, or while the socket is away, the audio goes nowhere: never held for later.
                if talking, let sink { sink(sinkRate == rate ? out : VoicePCM.resample(out, from: rate, to: sinkRate)) }
            } else if let sink {
                sink(sinkRate == rate ? out : VoicePCM.resample(out, from: rate, to: sinkRate))
            } else {
                hold(out, rate: rate)
            }
            guard !levelsOff, levelCount >= max(1, rate / 20) else { return nil }
            let value = mutedFlag ? 0 : min(1, (levelSum / Float(levelCount)).squareRoot())
            levelSum = 0
            levelCount = 0
            return value
        }
        if let level { lock.withLock { levelSink }?(level) }
    }

    /// Sends what is held (at `rate`), then everything pushed from now on, through `send`.
    func open(rate: Int, send: @escaping @Sendable (Data) -> Void) {
        lock.withLock {
            guard !closed else { return }
            sinkRate = rate
            var pending = Data()
            for piece in held {
                pending.append(piece.rate == rate ? piece.pcm : VoicePCM.resample(piece.pcm, from: piece.rate, to: rate))
            }
            held = []
            let chunk = max(2, VoicePCM.bytes(seconds: Self.chunkSeconds, rate: rate))
            var at = 0
            while at < pending.count {
                let end = min(pending.count, at + chunk)
                send(pending.subdata(in: at..<end))
                at = end
            }
            sink = send
        }
    }

    /// Drops what is held for the socket, keeping it closed or open as it is: a system call put on hold, so nothing said
    /// before the hold goes up when `ready` opens the uplink while it is on (#9290).
    func discardHeld() {
        lock.withLock { held = [] }
    }

    /// Stops sending and holds again (the socket went away; a reconnect opens it again).
    func hold() {
        lock.withLock {
            sink = nil
            sinkRate = 0
            // A span is the socket's: one that went away took it along.
            talking = false
        }
    }

    /// Drops everything; pushes after this are ignored.
    func close() {
        lock.withLock {
            closed = true
            sink = nil
            held = []
            levelSink = nil
            talking = false
        }
    }

    // Under the lock.
    private func hold(_ pcm: Data, rate: Int) {
        held.append((pcm, rate))
        // Drop the oldest pieces while more than `seconds` is held.
        var total = held.reduce(0.0) { $0 + Double($1.pcm.count) / Double($1.rate * 2) }
        while total > seconds, let first = held.first {
            let over = total - seconds
            let firstSeconds = Double(first.pcm.count) / Double(first.rate * 2)
            if firstSeconds <= over {
                held.removeFirst()
                total -= firstSeconds
            } else {
                let drop = min(first.pcm.count, VoicePCM.bytes(seconds: over, rate: first.rate) + 2)
                held[0].pcm = Data(first.pcm.dropFirst(drop))
                total -= Double(drop) / Double(first.rate * 2)
            }
        }
    }
}
