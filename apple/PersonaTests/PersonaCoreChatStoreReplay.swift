import Foundation
import PersonaCore
@testable import Persona

/// Replays the shared JSON against the actual iOS `ChatStore`, not a second translation of the Rust reducer. The
/// store is untouched: this type is used only by PersonaTests and compiled into that iOS test target. Scheduling is
/// covered separately through AppModel's real fill path in PersonaCoreContinuityTests.swift.
@MainActor
public struct NativeChatStoreContinuityReplay {
    public static func replayAll(in directory: URL = ContinuityCase.repositoryDirectory) throws -> (cases: Int, steps: Int) {
        let cases = try ContinuityCase.all(in: directory)
        print("native continuity directory: \(directory.path), cases: \(cases.count)")
        for `case` in cases { try replay(`case`) }
        return (cases.count, cases.reduce(0) { $0 + $1.events.count })
    }

    private static func replay(_ case: ContinuityCase) throws {
        let initial = `case`.initial
        let key = SessionKey(machineID: "fixture", sessionID: "continuity-\(UUID())")
        let folder = FileManager.default.temporaryDirectory.appending(path: "persona-continuity-\(UUID())", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let items = try decodeItems(initial)
        ChatCache.save(key, items: items, hasMore: initial.boolean("has_more") ?? false,
                       history: initial.text("history"), covered: initial.number("covered"), in: folder)
        let store = ChatStore(key: key, cacheFolder: folder)
        if initial.flag("loaded_once") { store.merge([], backlog: true) }
        var opened: Item?
        var range: [Item] = []
        for (index, event) in `case`.events.enumerated() {
            let label = "\(`case`.file)/\(`case`.name) step \(index)"
            var gap: ChatStore.Gap = .none
            switch event.text("kind") {
            case "http_latest", "ws_items":
                gap = store.apply(try decodeItems(event), span: span(event), backlog: event.text("kind") == "http_latest" || event.flag("backlog"),
                                  hasMore: event.boolean("has_more"), moreAfter: event.flag("more_after"))
            case "http_after":
                gap = store.apply(try decodeItems(event), span: span(event), moreAfter: event.flag("more_after"))
            case "http_reload":
                store.replace(with: try decodeItems(event), hasMore: try required(event.boolean("has_more"), "http_reload has_more"), span: span(event))
            case "http_older":
                store.prepend(try decodeItems(event), hasMore: try required(event.boolean("has_more"), "http_older has_more"))
            case "open_fold":
                let id = try required(event.text("fold_id"), "open_fold fold_id")
                opened = store.items.first { $0.id == id }
                range = []
            case "http_range":
                range += try decodeItems(event)
                if !event.flag("more"), let opened { store.unfold(opened, into: range) }
            case "continue_fill":
                // Compatibility only. Current AppModel.settle starts fill immediately, and the shared fixtures use
                // http_after directly rather than inventing a wait the iOS scheduler no longer has.
                break
            case "reconnect", "load_older":
                // These only emit a request; the transport-free Store has no corresponding state transition.
                break
            default: throw ContinuityFixtureError.invalidFixture("\(label): unknown event")
            }
            let expected = `case`.expected[index]
            let actualItems = try normalized(store.items)
            try check(actualItems == expected["items"], "\(label) items", actualItems, expected["items"])
            try check(store.items.map(\.id) == (expected["item_ids"]?.array?.compactMap(\.string) ?? []), "\(label) ids", store.items.map(\.id), expected["item_ids"])
            try check(store.covered == expected.number("covered"), "\(label) covered", store.covered, expected["covered"])
            try check(store.history == expected.text("history"), "\(label) history", store.history, expected["history"])
            try check(store.hasMore == (expected.boolean("has_more") ?? false), "\(label) has_more", store.hasMore, expected["has_more"])
            try check(store.loadedOnce == expected.flag("loaded_once"), "\(label) loaded_once", store.loadedOnce, expected["loaded_once"])
            let expectedGap: ChatStore.Gap = switch expected.text("gap") { case "fill": .fill; case "reload": .reload; default: .none }
            try check(gap == expectedGap, "\(label) gap", gap, expected["gap"])
        }
    }

    private static func normalized(_ items: [Item]) throws -> ContinuityJSON {
        // Item.CodingKeys spells this locally retained field `supervisionUser`, while the hub/fixture uses
        // `supervision_user`. This is only the native encoder's key normalization; the actual Store value is unchanged.
        let encoded = try JSON.encoder.encode(items)
        guard let objects = try JSONSerialization.jsonObject(with: encoded) as? [[String: Any]] else {
            throw ContinuityFixtureError.invalidFixture("native items encoding")
        }
        return .array(try objects.map { item in
            var item = item
            if let value = item.removeValue(forKey: "supervisionUser") { item["supervision_user"] = value }
            return try ContinuityJSON.from(item)
        })
    }

    private static func decodeItems(_ object: ContinuityJSON) throws -> [Item] {
        guard let data = try (object["items"] ?? .array([])).encoded().data(using: .utf8) else {
            throw ContinuityFixtureError.invalidFixture("items encoding")
        }
        return try JSON.decoder.decode([Item].self, from: data)
    }

    private static func span(_ event: ContinuityJSON) -> ItemSpan {
        ItemSpan(history: event.text("history"), from: event.number("from_seq"), to: event.number("to_seq"))
    }

    private static func required<T>(_ value: T?, _ field: String) throws -> T {
        guard let value else { throw ContinuityFixtureError.invalidFixture("missing \(field)") }
        return value
    }

    private static func check<T>(_ condition: Bool, _ label: String, _ actual: T, _ expected: Any?) throws {
        guard condition else {
            throw ContinuityFixtureError.invalidFixture("\(label)\nactual: \(actual)\nexpected: \(String(describing: expected))")
        }
    }
}
