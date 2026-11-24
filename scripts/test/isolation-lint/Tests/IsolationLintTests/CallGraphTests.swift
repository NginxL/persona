import XCTest
import IsolationLintCore

/// Code that is not isolated runs where it is called. The lint follows calls by name, labels and the declared type of
/// what they are made on; where it cannot read that, the answer is `unknown`, never `off`.
final class CallGraphTests: XCTestCase {
    func testTheReasonListsEveryUseTheVerdictRestsOn() {
        let why = reason("""
        nonisolated func read() { _ = try? Data(contentsOf: url) }
        func screen() {
            Task.detached { read() }
            Task.detached { read() }
        }
        """)
        XCTAssertTrue(why.contains("every use the lint found is off the main actor (2: App/Sample.swift:5, App/Sample.swift:6)"), why)
    }

    func testOneCallFromTheMainActorIsEnough() {
        expect("""
        nonisolated func read() { _ = try? Data(contentsOf: url) } // main
        func screen() {
            Task.detached { read() }
            read()
        }
        """)
    }

    func testCallsAreFollowedThroughSeveralFunctions() {
        expect("""
        nonisolated enum Disk {
            static func read() -> Data? { try? Data(contentsOf: url) } // main
            static func cached() -> Data? { read() }
            static func value() -> Data? { cached() }
        }
        nonisolated enum Other {
            static func read() -> Data? { try? Data(contentsOf: url) } // off
            static func cached() -> Data? { read() }
        }
        func screen() {
            _ = Disk.value()
            Task.detached { _ = Other.cached() }
        }
        """)
    }

    /// `Failure.read(errno)` makes a case of the sources' enum `Failure`: it calls no `read(_:)` of another type, even
    /// where the name may also stand for any type (here a generic parameter) (#8994).
    func testAnEnumCaseWrittenOnItsTypeIsNoCall() {
        expect("""
        nonisolated enum Avatar {
            static func read(_ from: URL) -> Data? { try? Data(contentsOf: from) } // off
        }
        nonisolated struct Socket {
            enum Failure: Error { case read(Int32), pathTooLong }
            func receive() throws { throw Failure.read(errno) }
        }
        func wrapped<Failure: Error>(_ error: Failure) -> Error { error }
        func load() { Task.detached { _ = Avatar.read(url) } }
        """)
    }

    /// A static method of the enum, called on its name, is still a call.
    func testAStaticMethodOnAnEnumsNameIsStillACall() {
        expect("""
        nonisolated enum Failure: Error {
            case read(Int32)
            static func read(from: URL) -> Data? { try? Data(contentsOf: from) } // main
        }
        func screen() { _ = Failure.read(from: url) }
        """)
    }

    func testFunctionsThatCallEachOtherSettle() {
        expect("""
        nonisolated func ping(_ n: Int) { _ = try? Data(contentsOf: url); if n > 0 { pong(n - 1) } } // off
        nonisolated func pong(_ n: Int) { ping(n) }
        func screen() {
            Task.detached { ping(3) }
        }
        """)
    }

    func testCallsAcrossFilesAndTargets() {
        expect([
            "Kit/Sources/Disk.swift": """
            public struct Disk: Sendable {
                public init() { }
                public func read() -> Data? { try? Data(contentsOf: url) } // main
                public func readOff() -> Data? { try? Data(contentsOf: url) } // off
            }
            """,
            "App/Screen.swift": """
            final class Screen {
                let disk = Disk()
                func show() { _ = disk.read() }
                func refresh() {
                    let disk = self.disk
                    Task.detached { _ = disk.readOff() }
                }
            }
            """,
        ], targets: ["App": app, "Kit": package])
    }

    func testAMemberOfAnotherTypeWithTheSameNameIsNotACall() {
        expect("""
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) } // off
        }
        nonisolated struct Label {
            func read() -> String { "" }
        }
        func screen(_ label: Label) {
            _ = label.read()
            _ = Label().read()
            Task.detached { _ = Disk().read() }
        }
        """)
    }

    func testLabelsTellOverloadsApart() {
        expect("""
        nonisolated struct Disk {
            func read(at path: String) -> Data? { try? Data(contentsOf: url) } // off
            func read(named name: String) -> Data? { try? Data(contentsOf: url) } // main
        }
        func screen(_ disk: Disk) {
            _ = disk.read(named: "")
            Task.detached { _ = disk.read(at: "") }
        }
        """)
    }

    func testAValueTypedByWhatItIsMadeFrom() {
        expect("""
        nonisolated final class Store: Sendable {
            static let shared = Store()
            func read() -> Data? { try? Data(contentsOf: url) } // main
            func make() -> Reader { Reader() }
        }
        nonisolated struct Reader {
            func load() -> Data? { try? Data(contentsOf: url) } // main
        }
        func screen() {
            let store = Store.shared
            _ = store.read()
            _ = store.make().load()
        }
        """)
    }

    // MARK: Unknown

    func testACallOnAValueTheLintCannotTypeIsUnknownNotOff() {
        // `$0` has no declaration to read: it may be a Disk, so the call may be on the main actor.
        expect("""
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) } // unknown
        }
        func screen(_ items: [Any]) {
            Task.detached { _ = Disk().read() }
            _ = items.map { ($0 as AnyObject).read() }
        }
        """)
        let why = reason("""
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) }
        }
        func screen(_ items: [Any]) {
            Task.detached { _ = Disk().read() }
            _ = items.map { ($0 as AnyObject).read() }
        }
        """)
        XCTAssertTrue(why.contains("may"), why)
    }

    func testACallOnAnUntypedValueNeverMakesAMainVerdict() {
        // The same call cannot prove the function is on the main actor either.
        let findings = lint(["App/Sample.swift": prelude + """
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) }
        }
        func screen(_ items: [Any]) {
            _ = items.map { ($0 as AnyObject).read() }
        }
        """])
        XCTAssertEqual(findings.map(\.verdict), [.unknown])
    }

    func testAFunctionHandedOnAsAValueIsUnknown() {
        expect("""
        nonisolated func read() { _ = try? Data(contentsOf: url) } // unknown
        nonisolated func schedule(_ queue: OperationQueue) {
            queue.addOperation(read)
        }
        """)
    }

    func testAFunctionHandedToAnInlineCallIsACall() {
        expect("""
        nonisolated func read(_ at: URL) -> Data? { try? Data(contentsOf: at) } // main
        nonisolated func readOff(_ at: URL) -> Data? { try? Data(contentsOf: at) } // off
        func screen(_ urls: [URL]) {
            _ = urls.compactMap(read)
            Task.detached { _ = urls.compactMap(readOff) }
        }
        """)
    }

    func testANameInAStringInterpolationIsReadNotHandedOn() {
        // `$0` has no declaration to read, so `$0.pending` may name `Queue.pending`; in an interpolation it is only
        // described, never called (#7578). A labeled interpolation may still take a closure.
        expect("""
        nonisolated struct Checks { var pending = 0 }
        nonisolated final class Queue: Sendable {
            func pending() -> Data? { try? Data(contentsOf: url) } // off
        }
        func screen(_ all: [Checks]) -> [String] {
            Task.detached { _ = Queue().pending() }
            return all.map { "\\($0.pending) running" }
        }
        """)
        let why = reason("""
        nonisolated struct Checks { var pending = 0 }
        nonisolated final class Queue: Sendable {
            func pending() -> Data? { try? Data(contentsOf: url) }
        }
        extension DefaultStringInterpolation {
            mutating func appendInterpolation(_ value: Any, as words: String) { appendLiteral(words) }
        }
        func screen(_ all: [Checks]) -> [String] {
            Task.detached { _ = Queue().pending() }
            return all.map { "\\($0.pending, as: "running")" }
        }
        """)
        XCTAssertTrue(why.contains("may pass it on as a value"), why)
    }

    func testAFunctionInAStringInterpolationIsCalledOnlyByAnInterpolationThatTakesOne() {
        // The default interpolation prints a function as "(Function)". An `appendInterpolation` of the sources that
        // takes a function unlabeled may call it, so there the name is handed on (#7578).
        expect("""
        nonisolated func label() -> String { _ = try? Data(contentsOf: url); return "" } // off
        func screen() -> String {
            Task.detached { _ = label() }
            return "\\(label)"
        }
        """)
        let findings = expect("""
        nonisolated func label() -> String { _ = try? Data(contentsOf: url); return "" } // unknown
        extension DefaultStringInterpolation {
            mutating func appendInterpolation(_ make: () -> String) { appendLiteral(make()) }
        }
        func screen() -> String {
            Task.detached { _ = label() }
            return "\\(label)"
        }
        """)
        let why = findings.first?.reason ?? ""
        XCTAssertTrue(why.contains("passes it on as a value"), why)
    }

    func testAFunctionInAStringInterpolationMayBeCalledByAnInterpolationThatTakesAnyValue() {
        // An unlabeled `appendInterpolation` whose parameter may hold a function may call `label`: a protocol
        // (`CustomStringConvertible`, one of the sources), `Any`, `some` / `any`, a generic parameter or associated
        // type, an alias, and a struct's name that is also a generic parameter or an alias elsewhere (#7578 review).
        let parameters = ["(_ value: CustomStringConvertible)", "(_ value: Any?)", "(_ value: some Sendable)",
                          "(_ value: Shown)", "<T: Sendable>(_ value: T)", "<T: Sequence>(_ value: T.Element)",
                          "(_ value: Make)", "(_ value: Item)", "(_ value: Recipe)"]
        for parameter in parameters {
            let findings = expect("""
            protocol Shown {}
            typealias Make = () -> String
            struct Item {}
            struct Element {}
            struct Recipe {}
            enum Kitchen { typealias Recipe = () -> String }
            func pick<Item>(_ all: [Item]) -> Item? { all.first }
            nonisolated func label() -> String { _ = try? Data(contentsOf: url); return "" } // unknown
            extension DefaultStringInterpolation {
                mutating func appendInterpolation\(parameter) { if let make = value as? () -> String { appendLiteral(make()) } }
            }
            func screen() -> String {
                Task.detached { _ = label() }
                return "\\(label)"
            }
            """)
            let why = findings.first?.reason ?? ""
            XCTAssertTrue(why.contains("passes it on as a value"), "\(parameter): \(why)")
        }
    }

    func testAFunctionInAStringInterpolationIsNotCalledByAnInterpolationThatTakesAConcreteType() {
        // No function converts to these, and `"\(label)"` cannot call one whose first parameter has a label, so the
        // default interpolation still only describes `label` (#7578 review).
        let parameters = ["(_ value: Int)", "(_ value: String?)", "(_ value: Badge)", "(_ value: Kitchen.Order)",
                          "(_ value: [Any])", "(value: Any)"]
        for parameter in parameters {
            let findings = expect("""
            struct Badge { var count = 0 }
            enum Kitchen { struct Order {} }
            nonisolated func label() -> String { _ = try? Data(contentsOf: url); return "" } // off
            extension DefaultStringInterpolation {
                mutating func appendInterpolation\(parameter) { appendLiteral("") }
            }
            func screen() -> String {
                Task.detached { _ = label() }
                return "\\(label)"
            }
            """)
            XCTAssertEqual(findings.map(\.verdict.rawValue), ["off"], parameter)
        }
    }

    func testACallInAStringInterpolationIsACall() {
        expect("""
        nonisolated func count() -> Int { (try? Data(contentsOf: url))?.count ?? 0 } // main
        nonisolated final class Queue: Sendable {
            func pending() -> Data? { try? Data(contentsOf: url) } // main
        }
        func show(_ work: @escaping () -> Int) -> String { "\\(work())" }
        func screen(_ queue: Queue) -> [String] {
            Task.detached { _ = count(); _ = queue.pending() }
            return [show { count() }, "\\(queue.pending()) waiting"]
        }
        """)
    }

    func testWhatObjectiveCCanCallIsUnknown() {
        expect("""
        import UIKit
        nonisolated final class Target: NSObject {
            @objc func fire() { _ = try? Data(contentsOf: url) } // unknown
            func viaSelector() { _ = try? Data(contentsOf: url) } // unknown
            func hook() -> Selector { #selector(viaSelector) }
        }
        """)
    }

    func testAnOverrideOfAClassTheLintHasNoSourceForIsUnknown() {
        expect("""
        nonisolated final class Work: Operation, @unchecked Sendable {
            override func main() { _ = try? Data(contentsOf: url) } // unknown
        }
        """)
    }

    func testADelegateCallbackOfAnSDKProtocolIsUnknown() {
        // Nothing in the scanned sources calls it, and the lint has no list of what `URLSessionDelegate` calls.
        expect("""
        nonisolated final class Listener: NSObject, URLSessionDataDelegate {
            func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
                _ = try? Data(contentsOf: url) // unknown
            }
        }
        """)
    }

    func testAFileNoTargetCompilesIsUnknown() {
        let findings = lint(["Loose/Sample.swift": prelude + "func read() { _ = try? Data(contentsOf: url) }\n"])
        XCTAssertEqual(findings.map(\.verdict), [.unknown])
        XCTAssertTrue(findings[0].reason.contains("no target or package the lint knows compiles this file"), findings[0].reason)
    }

    func testAKeyPathToANonisolatedPropertyIsUnknown() {
        expect("""
        nonisolated struct Entry {
            var data: Data? { try? Data(contentsOf: url) } // unknown
        }
        nonisolated func all(_ entries: [Entry]) -> [Data?] {
            entries.map(\\.data)
        }
        """)
    }

    /// An untyped `guard let activity = External.activities.first(...)` calling `activity.update(_:)` inside a
    /// `nonisolated async` helper called from MainActor creates a weak edge to an unrelated `nonisolated` store's
    /// `update(_:)`. Making the helper `@concurrent` and typing `activity` keeps the store's file read off MainActor
    /// (#13770).
    func testTypedConcurrentActivityUpdateDoesNotTaintUnrelatedStoreUpdate() {
        expect("""
        nonisolated struct Store {
            func savedState() -> Data? { try? Data(contentsOf: url) } // unknown
            func update(_ change: (inout Int) -> Void) { _ = savedState() }
        }
        struct Intent {
            @concurrent nonisolated func perform() async {
                Store().update { _ in }
            }
        }
        enum Tool {
            private nonisolated static func updateLiveActivity(_ content: Int) async {
                guard let activity = ExternalActivity.activities.first(where: { $0 == 1 }) else { return }
                await activity.update(content)
            }
            struct Backend {
                var update: @MainActor @Sendable (Int) async -> Void
                static var live: Self {
                    Self(update: { content in await updateLiveActivity(content) })
                }
            }
        }
        """)
        expect("""
        nonisolated struct Store {
            func savedState() -> Data? { try? Data(contentsOf: url) } // off
            func update(_ change: (inout Int) -> Void) { _ = savedState() }
        }
        struct Intent {
            @concurrent nonisolated func perform() async {
                Store().update { _ in }
            }
        }
        enum Tool {
            @concurrent
            private nonisolated static func updateLiveActivity(_ content: Int) async {
                guard let activity: ExternalActivity = ExternalActivity.activities.first(where: { $0 == 1 }) else { return }
                await activity.update(content)
            }
            struct Backend {
                var update: @MainActor @Sendable (Int) async -> Void
                static var live: Self {
                    Self(update: { content in await updateLiveActivity(content) })
                }
            }
        }
        """)
    }
}
