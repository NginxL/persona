import XCTest
import Synchronization
@testable import Persona
import PersonaCoreTestSupport

@MainActor
final class PersonaCoreContinuityTests: XCTestCase {
    override func setUp() {
        super.setUp()
        ProductionContinuityShadow.shared.installForTests()
    }

    func testEveryContinuityFixtureThroughRealCBinding() async throws {
        let result = try await ContinuityBindingReplay.replayAll()
        XCTAssertEqual(result.cases, 39)
        XCTAssertEqual(result.steps, 95)
        print("iOS C binding: \(result.cases) cases, \(result.steps) steps, \(result.patches) patches")
    }

    func testNativeAndCoreShadowReplayMatchesFoldAndReloadRules() async throws {
        let cases = try await ContinuityCase.all(in: ContinuityCase.repositoryDirectory)
        for `case` in cases {
            try await NativeChatStoreContinuityReplay.replayShadow(`case`)
        }
        XCTAssertEqual(cases.count, 39)
    }

    func testEveryContinuityFixtureAgainstNativeChatStore() async throws {
        let result = try await NativeChatStoreContinuityReplay.replayAll()
        XCTAssertEqual(result.cases, 39)
        XCTAssertEqual(result.steps, 95)
        print("iOS native ChatStore: \(result.cases) cases, \(result.steps) steps")
    }

    /// Exercises the actual iOS scheduling path, rather than translating a ScheduleFill enum into a sleep. Current
    /// AppModel.settle starts a fetch immediately (#4267), with no 1.5-second delay before the gap is filled.
    func testGapStartsNativeAppModelFillImmediately() async throws {
        let key = SessionKey(machineID: "fixture", sessionID: UUID().uuidString)
        let folder = FileManager.default.temporaryDirectory.appending(path: "persona-fill-\(UUID())", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let item = Item(id: "1#0", seq: 1, ts: nil, role: "assistant", kind: .text, text: "cached", toolUseID: nil,
                        toolName: nil, toolInput: nil, isError: nil, image: nil)
        ChatCache.save(key, items: [item], hasMore: false, history: "h", covered: 1, in: folder)
        let model = AppModel(stateFolder: folder)
        model.linksUpForTests = false
        let store = model.chat(key)
        store.setContinuityShadowEnabledForFixtureReplay(true)
        XCTAssertEqual(store.covered, 1)
        let requests = Mutex<[String]>([])
        model.linkHooksForTests = (
            transport: { request in
                requests.withLock { $0.append(request.query["after_seq"] ?? "") }
                let pushed = Item(id: "2#0", seq: 2, ts: nil, role: "assistant", kind: .text, text: "push", toolUseID: nil,
                                  toolName: nil, toolInput: nil, isError: nil, image: nil)
                let data = try JSONSerialization.data(withJSONObject: [
                    "items": [["id": pushed.id, "seq": pushed.seq, "role": pushed.role, "kind": pushed.kind.rawValue, "text": pushed.text ?? ""]],
                    "history": "h", "from_seq": 1, "to_seq": 2, "more_after": false,
                ])
                return data
            },
            socket: { SyncSocket() }
        )
        model.connectOnly(.demo)
        for link in model.links { link.live.disconnect() }
        let pushed = Item(id: "3#0", seq: 3, ts: nil, role: "assistant", kind: .text, text: "later", toolUseID: nil,
                          toolName: nil, toolInput: nil, isError: nil, image: nil)
        let started = ContinuousClock.now
        let event = ServerEvent.items(key, [pushed], backlog: false, hasMore: nil,
                                      span: ItemSpan(history: "h", from: 2, to: 3), moreAfter: false)
        model.handle(event, group: "")
        for _ in 0..<200 where requests.withLock({ $0.isEmpty }) { try await Task.sleep(for: .milliseconds(5)) }
        XCTAssertEqual(requests.withLock { $0 }, ["1"])
        XCTAssertLessThan(started.duration(to: .now), .milliseconds(500))
        for _ in 0..<200 where store.covered != 2 { try await Task.sleep(for: .milliseconds(5)) }
        XCTAssertEqual(store.covered, 2)
        let shadow = try await ProductionContinuityShadow.shared.compare(store)
        XCTAssertGreaterThan(shadow.compared, 0)
        XCTAssertEqual(shadow.mismatches, 0)
    }
}
