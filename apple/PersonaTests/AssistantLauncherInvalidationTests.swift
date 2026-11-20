import Foundation
import Testing
@testable import Persona

@MainActor
struct AssistantLauncherInvalidationTests {
    private let base = AppModel.AssistantLauncherSignature(group: "hub", requestedManager: "work", respondingManager: "work",
                                                           machineID: "computer", launcher: "claude")
    private let now = Date(timeIntervalSince1970: 10_000)

    @Test func theFirstSnapshotInvalidates() {
        #expect(AppModel.shouldInvalidateAssistantLaunchers(signature: base, lastSignature: nil,
                                                            lastInvalidation: nil, hasCachedLaunchers: true, now: now))
        #expect(AppModel.shouldInvalidateAssistantLaunchers(signature: base, lastSignature: base,
                                                            lastInvalidation: nil, hasCachedLaunchers: true, now: now))
    }

    @Test func theSameSignatureWithinTenMinutesDoesNotInvalidate() {
        let last = now.addingTimeInterval(-AppModel.assistantLauncherRecheckInterval + 1)
        #expect(!AppModel.shouldInvalidateAssistantLaunchers(signature: base, lastSignature: base,
                                                             lastInvalidation: last, hasCachedLaunchers: true, now: now))
    }

    @Test func anEmptyCacheWithinTenMinutesInvalidates() {
        let last = now.addingTimeInterval(-AppModel.assistantLauncherRecheckInterval + 1)
        #expect(AppModel.shouldInvalidateAssistantLaunchers(signature: base, lastSignature: base,
                                                            lastInvalidation: last, hasCachedLaunchers: false, now: now))
    }

    @Test func theSameSignatureAtTenMinutesInvalidates() {
        let last = now.addingTimeInterval(-AppModel.assistantLauncherRecheckInterval)
        #expect(AppModel.shouldInvalidateAssistantLaunchers(signature: base, lastSignature: base,
                                                            lastInvalidation: last, hasCachedLaunchers: true, now: now))
    }

    @Test(arguments: ["group", "requested manager", "responding manager", "machine", "launcher"])
    func aLauncherScopeChangeInvalidates(_ change: String) {
        var changed = base
        switch change {
        case "group": changed.group = "other"
        case "requested manager": changed.requestedManager = "other"
        case "responding manager": changed.respondingManager = "other"
        case "machine": changed.machineID = "other"
        case "launcher": changed.launcher = "other"
        default: Issue.record("unknown change \(change)")
        }
        #expect(AppModel.shouldInvalidateAssistantLaunchers(signature: changed, lastSignature: base,
                                                            lastInvalidation: now, hasCachedLaunchers: true, now: now), "\(change) changed")
    }
}
