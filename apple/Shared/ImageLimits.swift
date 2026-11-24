import Foundation
import PersonaWire

/// The pictures a message may carry (#12973, docs/protocol/protocol.md "Pictures in a message"): the hub publishes them in
/// its hello (`image_limits`) and refuses a message with more than `perMessage`. A hub without them takes the same numbers
/// (its body limit is the same and it counts nothing), so `standard` stands in until a hello says otherwise.
nonisolated struct ImageLimits: Codable, Hashable, Sendable {
    /// The body of a message that carries pictures.
    var requestBytes: Int
    /// One picture's bytes the app aims for when it scales one (the hub does not refuse a larger one).
    var imageBytes: Int
    /// The long side a picture is scaled down to.
    var maxSide: Int
    /// The pictures one message may carry.
    var perMessage: Int

    static let standard = ImageLimits(requestBytes: 32 << 20, imageBytes: 3_750_000, maxSide: 1600, perMessage: 20)
    /// Room a message keeps beside its pictures, for its words, quotes and other fields.
    static let bodyRoom = 1 << 20

    /// Whether one more picture fits: `count` past `perMessage`, `bytes` past what the body leaves for pictures.
    enum Room: Equatable, Sendable { case ok, count, bytes }

    init(requestBytes: Int, imageBytes: Int, maxSide: Int, perMessage: Int) {
        self.requestBytes = requestBytes
        self.imageBytes = imageBytes
        self.maxSide = maxSide
        self.perMessage = perMessage
    }

    /// Whether one more picture of `size` base64 characters fits beside those held (their base64 lengths).
    func room(held: [Int], size: Int) -> Room {
        if held.count >= perMessage { return .count }
        return held.reduce(0, +) + size > requestBytes - Self.bodyRoom ? .bytes : .ok
    }

    /// How many more pictures fit by count alone (a picker's selection limit).
    func roomLeft(holding count: Int) -> Int { max(0, perMessage - count) }

    enum CodingKeys: String, CodingKey {
        case requestBytes = "request_bytes", imageBytes = "image_bytes", maxSide = "max_side", perMessage = "per_message"
    }

    /// A value that is missing, not a number or not above zero keeps the standard one: never a lost hello.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func number(_ key: CodingKeys, _ standard: Int) -> Int {
            guard let n = (try? c.decodeIfPresent(Double.self, forKey: key)) ?? nil, n >= 1, n < Double(Int32.max) else { return standard }
            return Int(n)
        }
        requestBytes = number(.requestBytes, Self.standard.requestBytes)
        imageBytes = number(.imageBytes, Self.standard.imageBytes)
        maxSide = number(.maxSide, Self.standard.maxSide)
        perMessage = number(.perMessage, Self.standard.perMessage)
    }
}

/// A hello's `image_limits`: the limits for every session, and complete sets for the agents that need others (none today).
nonisolated struct ImageLimitsTable: Codable, Hashable, Sendable {
    var limits = ImageLimits.standard
    var agents: [String: ImageLimits] = [:]

    init(limits: ImageLimits = .standard, agents: [String: ImageLimits] = [:]) {
        self.limits = limits
        self.agents = agents
    }

    /// The limits a message to a session of `agent` is held to; an empty agent is Claude Code's, as on the hub.
    func limits(for agent: String?) -> ImageLimits {
        agents[agent.flatMap { $0.isEmpty ? nil : $0 } ?? "claude"] ?? limits
    }

    private enum AgentsKey: String, CodingKey { case agents }

    init(from decoder: Decoder) throws {
        limits = try ImageLimits(from: decoder)
        let c = try decoder.container(keyedBy: AgentsKey.self)
        agents = ((try? c.decodeIfPresent([String: ImageLimits].self, forKey: .agents)) ?? nil) ?? [:]
    }

    func encode(to encoder: Encoder) throws {
        try limits.encode(to: encoder)
        var c = encoder.container(keyedBy: AgentsKey.self)
        if !agents.isEmpty { try c.encode(agents, forKey: .agents) }
    }

    private struct Hello: Decodable {
        var table: ImageLimitsTable?
        enum CodingKeys: String, CodingKey { case table = "image_limits" }
        init(from decoder: Decoder) throws {
            table = (try? decoder.container(keyedBy: CodingKeys.self)).flatMap { try? $0.decodeIfPresent(ImageLimitsTable.self, forKey: .table) }
        }
    }

    /// The table a hello frame carries, read on its own so that no shape of it can lose the hello; nil from a hub
    /// without one.
    static func decode(hello data: Data) -> ImageLimitsTable? {
        (try? JSON.decoder.decode(Hello.self, from: data))?.table
    }
}
