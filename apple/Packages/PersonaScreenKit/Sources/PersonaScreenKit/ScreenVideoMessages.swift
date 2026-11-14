import Foundation

// The #3954 live-screen video messages: the control structs (JSON bodies) and the binary bodies (`format`, `frame`,
// `report`), plus one `ScreenVideoMessage` enum that frames and parses either side's traffic. See
// `design-3954.protocol.md` for the field-by-field wire format.

/// `hello` (helper → phone): the first message after the phone's `start`. `rect` is the display's rectangle in the
/// RFB framebuffer's pixels when the node knows it, so the app lays the video onto the same place as RFB.
public struct HelloMessage: Codable, Equatable, Sendable {
    public var version: Int
    public var codec: String // "hevc" | "h264"
    public var display: Int
    public var width: Int
    public var height: Int
    public var rect: [Int]?

    public init(version: Int, codec: String, display: Int, width: Int, height: Int, rect: [Int]? = nil) {
        self.version = version
        self.codec = codec
        self.display = display
        self.width = width
        self.height = height
        self.rect = rect
    }
}

/// `start` (phone → helper): opens capture. `display` nil means the main display.
public struct StartMessage: Codable, Equatable, Sendable {
    public var display: Int?
    public var maxSize: Int
    public var fps: Int
    public var maxKbps: Int
    public var codecs: [String]

    enum CodingKeys: String, CodingKey {
        case display
        case maxSize = "max_size"
        case fps
        case maxKbps = "max_kbps"
        case codecs
    }

    public init(display: Int?, maxSize: Int, fps: Int, maxKbps: Int, codecs: [String]) {
        self.display = display
        self.maxSize = maxSize
        self.fps = fps
        self.maxKbps = maxKbps
        self.codecs = codecs
    }
}

/// `update` (phone → helper): any of `start`'s fields again, plus `paused` for the app leaving the foreground
/// without Picture in Picture, and `path` after the stream moved to another route (#6398).
public struct UpdateMessage: Codable, Equatable, Sendable {
    /// The route the stream now takes (`LinkStats`), so the helper starts its delay baseline again: the one-way delay
    /// changed with the route, and a slower route must not read as a queue. Strings, not `LinkStats`' enums, so a
    /// helper never fails an `update` over a route kind it doesn't know; helpers before it skip the field.
    public struct Path: Codable, Equatable, Sendable {
        public var kind: String
        public var via: String
        public var minRttMs: Double?

        enum CodingKeys: String, CodingKey {
            case kind
            case via
            case minRttMs = "min_rtt_ms"
        }

        public init(kind: String, via: String, minRttMs: Double? = nil) {
            self.kind = kind
            self.via = via
            self.minRttMs = minRttMs
        }

        /// The route `link` measures, as `update` carries it.
        public init(_ link: LinkStats) {
            self.init(kind: link.kind.rawValue, via: link.via.rawValue, minRttMs: link.minRttMs)
        }

        /// The kind as `LinkStats` names it; nil for one this side doesn't know.
        public var linkKind: LinkStats.Kind? { LinkStats.Kind(rawValue: kind) }
        public var linkVia: LinkStats.Via? { LinkStats.Via(rawValue: via) }
    }

    public var display: Int?
    public var maxSize: Int?
    public var fps: Int?
    public var maxKbps: Int?
    public var codecs: [String]?
    public var paused: Bool?
    public var path: Path?

    enum CodingKeys: String, CodingKey {
        case display
        case maxSize = "max_size"
        case fps
        case maxKbps = "max_kbps"
        case codecs
        case paused
        case path
    }

    public init(display: Int? = nil, maxSize: Int? = nil, fps: Int? = nil, maxKbps: Int? = nil,
                codecs: [String]? = nil, paused: Bool? = nil, path: Path? = nil) {
        self.display = display
        self.maxSize = maxSize
        self.fps = fps
        self.maxKbps = maxKbps
        self.codecs = codecs
        self.paused = paused
        self.path = path
    }
}

/// `paused` (helper → phone): capture stopped; the phone goes back to RFB. `reason` is one of
/// locked | login_window | permission | display_gone | busy.
public struct PausedMessage: Codable, Equatable, Sendable {
    public var reason: String

    public init(reason: String) {
        self.reason = reason
    }
}

/// `heartbeat` (helper → phone): sent once a second while no frame went out, so the phone can tell a quiet screen
/// from a dead stream.
public struct HeartbeatMessage: Codable, Equatable, Sendable {
    public var last: Int

    public init(last: Int) {
        self.last = last
    }
}

/// `timing` (helper → phone, #3954): once a second while frames go out, the helper's side of the latency the phone's
/// connection-stats overlay breaks down, as medians over that second in milliseconds, and what went out. `captureMs`
/// is ScreenCaptureKit's display time to its callback (about 0: frames come before their display time), `encodeMs`
/// VideoToolbox in to out, `networkMs` half the round trip from a frame going out to the phone's report on it (no
/// clocks to agree). A segment with nothing to measure in that second is nil. Apps before it skip the type by length.
public struct TimingMessage: Codable, Equatable, Sendable {
    public var captureMs: Double?
    public var encodeMs: Double?
    public var networkMs: Double?
    public var kbps: Int
    public var fps: Int

    enum CodingKeys: String, CodingKey {
        case captureMs = "capture_ms"
        case encodeMs = "encode_ms"
        case networkMs = "network_ms"
        case kbps
        case fps
    }

    public init(captureMs: Double?, encodeMs: Double?, networkMs: Double?, kbps: Int, fps: Int) {
        self.captureMs = captureMs
        self.encodeMs = encodeMs
        self.networkMs = networkMs
        self.kbps = kbps
        self.fps = fps
    }
}

/// `format` (helper → phone): the codec's parameter sets (HEVC VPS/SPS/PPS, or H.264 SPS/PPS), sent before the
/// first keyframe and whenever they change.
public struct FormatMessage: Equatable, Sendable {
    public var parameterSets: [Data]

    public init(parameterSets: [Data]) {
        self.parameterSets = parameterSets
    }
}

/// `frame` (helper → phone): one encoded sample as length-prefixed NAL units (AVCC).
public struct VideoFrame: Equatable, Sendable {
    public struct Flags: OptionSet, Equatable, Sendable {
        public let rawValue: UInt8
        public init(rawValue: UInt8) { self.rawValue = rawValue }
        public static let keyframe = Flags(rawValue: 1)
        public static let refinement = Flags(rawValue: 2)
    }

    public var number: UInt32
    public var captureMicros: UInt64
    public var flags: Flags
    public var sample: Data

    public init(number: UInt32, captureMicros: UInt64, flags: Flags, sample: Data) {
        self.number = number
        self.captureMicros = captureMicros
        self.flags = flags
        self.sample = sample
    }
}

/// `report` (phone → helper): how the latest frame arrived, driving the helper's bandwidth adaptation.
public struct ReceiverReport: Equatable, Sendable {
    public struct Flags: OptionSet, Equatable, Sendable {
        public let rawValue: UInt8
        public init(rawValue: UInt8) { self.rawValue = rawValue }
        public static let decodingError = Flags(rawValue: 1)
    }

    public var lastFrame: UInt32
    public var captureMicros: UInt64
    public var arrivalMicros: UInt64
    public var bytesSinceLast: UInt32
    public var flags: Flags

    public init(lastFrame: UInt32, captureMicros: UInt64, arrivalMicros: UInt64, bytesSinceLast: UInt32, flags: Flags) {
        self.lastFrame = lastFrame
        self.captureMicros = captureMicros
        self.arrivalMicros = arrivalMicros
        self.bytesSinceLast = bytesSinceLast
        self.flags = flags
    }
}

/// One decoded message in either direction. `unknown` keeps a type neither side understands, so a future field is
/// skipped rather than fatal.
public enum ScreenVideoMessage: Equatable, Sendable {
    case hello(HelloMessage)
    case format(FormatMessage)
    case frame(VideoFrame)
    case heartbeat(HeartbeatMessage)
    case paused(PausedMessage)
    case resumed
    case timing(TimingMessage)
    case start(StartMessage)
    case report(ReceiverReport)
    case keyframe
    case update(UpdateMessage)
    case unknown(type: UInt8, body: Data)

    /// The whole on-the-wire message, length prefix and all.
    public func framed() -> Data {
        switch self {
        case let .hello(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.hello.rawValue, body: Self.encodeJSON(m))
        case let .format(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.format.rawValue, body: Self.encodeFormat(m))
        case let .frame(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.frame.rawValue, body: Self.encodeFrame(m))
        case let .heartbeat(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.heartbeat.rawValue, body: Self.encodeJSON(m))
        case let .paused(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.paused.rawValue, body: Self.encodeJSON(m))
        case .resumed: return ScreenVideoWire.frame(ScreenVideoWire.Kind.resumed.rawValue, body: Data("{}".utf8))
        case let .timing(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.timing.rawValue, body: Self.encodeJSON(m))
        case let .start(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.start.rawValue, body: Self.encodeJSON(m))
        case let .report(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.report.rawValue, body: Self.encodeReport(m))
        case .keyframe: return ScreenVideoWire.frame(ScreenVideoWire.Kind.keyframe.rawValue)
        case let .update(m): return ScreenVideoWire.frame(ScreenVideoWire.Kind.update.rawValue, body: Self.encodeJSON(m))
        case let .unknown(type, body): return ScreenVideoWire.frame(type, body: body)
        }
    }

    /// Decodes a framed message's type and body. An unrecognised type becomes `.unknown`.
    public static func decode(type: UInt8, body: Data) throws -> ScreenVideoMessage {
        guard let kind = ScreenVideoWire.Kind(rawValue: type) else {
            return .unknown(type: type, body: body)
        }
        switch kind {
        case .hello: return .hello(try decodeJSON(body))
        case .format: return .format(try decodeFormat(body))
        case .frame: return .frame(try decodeFrame(body))
        case .heartbeat: return .heartbeat(try decodeJSON(body))
        case .paused: return .paused(try decodeJSON(body))
        case .resumed: return .resumed
        case .timing: return .timing(try decodeJSON(body))
        case .start: return .start(try decodeJSON(body))
        case .report: return .report(try decodeReport(body))
        case .keyframe: return .keyframe
        case .update: return .update(try decodeJSON(body))
        }
    }

    // MARK: - Bodies

    private static func encodeJSON<T: Encodable>(_ value: T) -> Data {
        // The wire has no JSON a plain struct can fail to encode; a failure here would be a programming error.
        (try? JSONEncoder().encode(value)) ?? Data("{}".utf8)
    }

    private static func decodeJSON<T: Decodable>(_ body: Data) throws -> T {
        do {
            return try JSONDecoder().decode(T.self, from: body)
        } catch {
            throw ScreenVideoError.badJSON
        }
    }

    private static func encodeFormat(_ m: FormatMessage) -> Data {
        var out = Data()
        out.append(UInt8(truncatingIfNeeded: m.parameterSets.count))
        for set in m.parameterSets {
            out.appendBigEndian(UInt16(truncatingIfNeeded: set.count))
            out.append(set)
        }
        return out
    }

    private static func decodeFormat(_ body: Data) throws -> FormatMessage {
        var r = ByteReader(body)
        let count = try r.u8()
        var sets: [Data] = []
        for _ in 0 ..< count {
            let length = Int(try r.u16())
            sets.append(try r.take(length))
        }
        return FormatMessage(parameterSets: sets)
    }

    private static func encodeFrame(_ m: VideoFrame) -> Data {
        var out = Data()
        out.appendBigEndian(m.number)
        out.appendBigEndian(m.captureMicros)
        out.append(m.flags.rawValue)
        out.append(m.sample)
        return out
    }

    private static func decodeFrame(_ body: Data) throws -> VideoFrame {
        var r = ByteReader(body)
        let number = try r.u32()
        let capture = try r.u64()
        let flags = try r.u8()
        return VideoFrame(number: number, captureMicros: capture, flags: VideoFrame.Flags(rawValue: flags), sample: r.rest())
    }

    private static func encodeReport(_ m: ReceiverReport) -> Data {
        var out = Data()
        out.appendBigEndian(m.lastFrame)
        out.appendBigEndian(m.captureMicros)
        out.appendBigEndian(m.arrivalMicros)
        out.appendBigEndian(m.bytesSinceLast)
        out.append(m.flags.rawValue)
        return out
    }

    private static func decodeReport(_ body: Data) throws -> ReceiverReport {
        var r = ByteReader(body)
        let lastFrame = try r.u32()
        let capture = try r.u64()
        let arrival = try r.u64()
        let bytes = try r.u32()
        let flags = try r.u8()
        return ReceiverReport(lastFrame: lastFrame, captureMicros: capture, arrivalMicros: arrival,
                              bytesSinceLast: bytes, flags: ReceiverReport.Flags(rawValue: flags))
    }
}
