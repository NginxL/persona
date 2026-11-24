import Foundation
import CoreFoundation
import PersonaCore

/// Repository JSON, shared by the C-binding and native iOS ChatStore replays. It deliberately knows nothing about
/// either implementation: both runners consume the same fixture and are checked against the same hand-written state.
public enum ContinuityJSON: Equatable, Sendable {
    case null
    case bool(Bool)
    case number(String)

    public static func == (lhs: Self, rhs: Self) -> Bool {
        switch (lhs, rhs) {
        case (.number(let left), .number(let right)):
            if let left = ExactJSONNumbers.normalizedInteger(left),
               let right = ExactJSONNumbers.normalizedInteger(right) { return left == right }
            return left == right
        case (.null, .null): return true
        case (.bool(let left), .bool(let right)): return left == right
        case (.string(let left), .string(let right)): return left == right
        case (.array(let left), .array(let right)): return left == right
        case (.object(let left), .object(let right)): return left == right
        default: return false
        }
    }
    case string(String)
    case array([ContinuityJSON])
    case object([String: ContinuityJSON])

    public var object: [String: ContinuityJSON]? { if case .object(let value) = self { value } else { nil } }
    public var array: [ContinuityJSON]? { if case .array(let value) = self { value } else { nil } }
    public var string: String? { if case .string(let value) = self { value } else { nil } }
    public var int: Int? {
        guard case .number(let text) = self else { return nil }
        // Only an integral decimal inside the platform Int range is a valid fixture index.
        guard let decimal = Decimal(string: text, locale: Locale(identifier: "en_US_POSIX")),
              let exact = Int(exactly: NSDecimalNumber(decimal: decimal)) else { return nil }
        return exact
    }
    public var bool: Bool? { if case .bool(let value) = self { value } else { nil } }
    public subscript(_ key: String) -> ContinuityJSON? { object?[key] }

    public static func load(file: URL) throws -> ContinuityJSON {
        let data = try Data(contentsOf: file)
        try ExactJSONNumbers.validate(data: data)
        return try from(try JSONSerialization.jsonObject(with: data))
    }

    public static func from(_ value: Any) throws -> ContinuityJSON {
        switch value {
        case is NSNull: .null
        case let value as NSNumber where CFGetTypeID(value) == CFBooleanGetTypeID(): .bool(value.boolValue)
        case let value as NSNumber: try jsonNumber(value)
        case let value as Bool: .bool(value)
        case let value as String: .string(value)
        case let value as [Any]: .array(try value.map(from))
        case let value as [String: Any]: .object(try value.mapValues(from))
        default: throw ContinuityFixtureError.invalidFixture("unsupported JSON value")
        }
    }

    public func encoded() throws -> String {
        if case .number(let text) = self {
            guard ExactJSONNumbers.isValid(text) else {
                throw ContinuityFixtureError.invalidFixture("non-finite or out-of-range JSON number")
            }
            return text
        }
        // Validate every numeric node before Foundation can raise an Objective-C exception for non-finite data.
        switch self {
        case .number(let text):
            guard ExactJSONNumbers.isValid(text) else {
                throw ContinuityFixtureError.invalidFixture("non-finite or out-of-range JSON number")
            }
        case .array(let values):
            for value in values { try value.validateNumbers() }
        case .object(let values):
            for value in values.values { try value.validateNumbers() }
        default: break
        }
        // Foundation otherwise rejects fragments with an Objective-C exception rather than a Swift error. Only
        // display_answer's bare JSON string is a C input; other scalar values here are diagnostic renderings.
        let options: JSONSerialization.WritingOptions = [.fragmentsAllowed]
        let data = try JSONSerialization.data(withJSONObject: foundation, options: options)
        return String(decoding: data, as: UTF8.self)
    }

    private func validateNumbers() throws {
        switch self {
        case .number(let text):
            guard ExactJSONNumbers.isValid(text) else {
                throw ContinuityFixtureError.invalidFixture("non-finite or out-of-range JSON number")
            }
        case .array(let values):
            for value in values { try value.validateNumbers() }
        case .object(let values):
            for value in values.values { try value.validateNumbers() }
        default: break
        }
    }

    private var foundation: Any {
        switch self {
        case .null: NSNull()
        case .bool(let value): value
        case .number(let value): NSDecimalNumber(string: value)
        case .string(let value): value
        case .array(let values): values.map(\.foundation)
        case .object(let values): values.mapValues(\.foundation)
        }
    }

    private static func jsonNumber(_ value: NSNumber) throws -> ContinuityJSON {
        let text = value.stringValue
        guard ExactJSONNumbers.isValid(text) else {
            throw ContinuityFixtureError.invalidFixture("out-of-range JSON number")
        }
        return .number(text)
    }
}

public enum ContinuityFixtureError: Error, CustomStringConvertible {
    case invalidFixture(String)
    case unknownEvent(String)
    case missingTimer
    case rejected(String)

    public var description: String {
        switch self {
        case .invalidFixture(let detail): "Invalid continuity fixture: \(detail)"
        case .unknownEvent(let kind): "Unknown continuity event: \(kind)"
        case .missingTimer: "A fill_timer event had no scheduled timer"
        case .rejected(let reason): "The core rejected an action: \(reason)"
        }
    }
}

public struct ContinuityCase: Sendable {
    public let file: String
    public let name: String
    public let input: ContinuityJSON
    public let initial: ContinuityJSON
    public let expected: [ContinuityJSON]
    public var events: [ContinuityJSON] { input["events"]?.array ?? [] }

    @concurrent public static func all(in directory: URL) async throws -> [ContinuityCase] {
        let files = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            .filter { $0.pathExtension == "json" }.sorted { $0.lastPathComponent < $1.lastPathComponent }
        guard !files.isEmpty else { throw ContinuityFixtureError.invalidFixture("no JSON files in \(directory.path)") }
        return try files.flatMap { file in
            let document = try ContinuityJSON.load(file: file)
            guard let cases = document["cases"]?.array else {
                throw ContinuityFixtureError.invalidFixture("\(file.lastPathComponent): cases")
            }
            return try cases.map { item in
                guard let name = item["name"]?.string, let input = item["input"],
                      let initial = input["initial"], let expected = item["expected"]?.array else {
                    throw ContinuityFixtureError.invalidFixture("\(file.lastPathComponent): name/input/expected")
                }
                guard input["events"]?.array?.count == expected.count else {
                    throw ContinuityFixtureError.invalidFixture("\(file.lastPathComponent)/\(name): step count")
                }
                return ContinuityCase(file: file.lastPathComponent, name: name, input: input, initial: initial, expected: expected)
            }
        }
    }

    public static let repositoryDirectory = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent() // PersonaCoreTestSupport
        .deletingLastPathComponent() // Tests
        .deletingLastPathComponent() // PersonaCore package
        .deletingLastPathComponent() // Packages
        .deletingLastPathComponent() // apple
        .deletingLastPathComponent() // repository
        .appending(path: "spec/fixtures/continuity")
}

public extension ContinuityJSON {
    func text(_ key: String) -> String? { self[key]?.string }
    func number(_ key: String) -> Int? { self[key]?.int }
    func boolean(_ key: String) -> Bool? { self[key]?.bool }
    func flag(_ key: String) -> Bool { boolean(key) == true }
    func itemsJSON() throws -> String { try (self["items"] ?? .array([])).encoded() }

    var wireBatch: WireBatch {
        get throws {
            WireBatch(
                itemsJson: try itemsJSON(), history: text("history"),
                fromSeq: number("from_seq").map(Int64.init), toSeq: number("to_seq").map(Int64.init),
                backlog: flag("backlog"), hasMore: boolean("has_more"), moreAfter: flag("more_after")
            )
        }
    }

    func chatEvent() throws -> ChatEvent? {
        switch text("kind") {
        case "http_latest": .latest(batch: try wireBatch)
        case "ws_items": .pushed(batch: try wireBatch)
        case "http_after": .after(batch: try wireBatch)
        case "http_reload": .reloaded(
            items_json: try itemsJSON(), history: text("history"), to_seq: number("to_seq").map(Int64.init),
            has_more: try require(boolean("has_more"), "http_reload has_more")
        )
        case "http_older": .older(items_json: try itemsJSON(), has_more: try require(boolean("has_more"), "http_older has_more"))
        case "http_range": .range(fold_id: nil, items_json: try itemsJSON(), more: flag("more"))
        case "reconnect": .reconnected
        case "load_older": .loadOlder
        case "open_fold": .openFold(fold_id: try require(text("fold_id"), "open_fold fold_id"))
        case "continue_fill": nil
        case let kind?: throw ContinuityFixtureError.unknownEvent(kind)
        case nil: throw ContinuityFixtureError.invalidFixture("event kind is missing")
        }
    }

    private func require<T>(_ value: T?, _ field: String) throws -> T {
        guard let value else { throw ContinuityFixtureError.invalidFixture("missing \(field)") }
        return value
    }
}

public extension Effect {
    var requestJSON: ContinuityJSON? {
        get throws {
            switch self {
            case let .fetch(chat: _, request: request): request.requestJSON
            case let .rejected(reason: reason): throw ContinuityFixtureError.rejected(reason)
            }
        }
    }
}

public extension Request {
    var requestJSON: ContinuityJSON {
        var object: [String: ContinuityJSON]
        switch self {
        case let .fetchAfter(after_seq: after):
            object = ["kind": .string("fetch_after"), "after_seq": .number(String(after))]
        case let .fetchLatest(fold_work: fold):
            object = ["kind": .string("fetch_latest"), "fold_work": .bool(fold)]
        case let .subscribe(since_seq: since, fold_work: fold):
            object = ["kind": .string("subscribe"), "fold_work": .bool(fold)]
            if let since { object["since_seq"] = .number(String(since)) }
        case let .fetchOlder(before_seq: before, fold_work: fold):
            object = ["kind": .string("fetch_older"), "before_seq": .number(String(before)), "fold_work": .bool(fold)]
        case let .fetchRange(fold_id: _, from_seq: from, through_seq: through):
            object = ["kind": .string("fetch_range"), "from_seq": .number(String(from)), "through_seq": .number(String(through))]
        }
        return .object(object)
    }
}
