import Foundation
import Testing
@testable import Persona

/// Which chats' waiting messages go out, by group (#4704): a group's messages go once its own link is up, whatever the
/// hub-level group's link is doing, and a group that is sending already or out of reach keeps no other group back.
struct OutboxFlushTests {
    private let home = SessionKey(machineID: "mac", sessionID: "a")
    private let homeB = SessionKey(machineID: "mac", sessionID: "b")
    private let work = SessionKey(group: "work", machineID: "box", sessionID: "c")

    @Test func aGroupSendsWhileTheHubLevelOneIsDown() {
        let out = OutboxFlush.batches([home, work], ready: { $0 == "work" }, busy: [])
        #expect(out == ["work": [work]])
    }

    @Test func everyGroupThatIsUpGetsItsOwnBatch() {
        let out = OutboxFlush.batches([work, homeB, home], ready: { _ in true }, busy: [])
        #expect(out == ["": [home, homeB], "work": [work]])
    }

    @Test func aGroupSendingAlreadyHoldsBackOnlyItself() {
        let out = OutboxFlush.batches([home, work], ready: { _ in true }, busy: [""])
        #expect(out == ["work": [work]])
    }

    @Test func nothingGoesWhileNoGroupIsUp() {
        #expect(OutboxFlush.batches([home, work], ready: { _ in false }, busy: []).isEmpty)
    }
}

/// A message that keeps coming back waiting while its group's link is up fails after `OutboxWait.connectedLimit`;
/// waiting while the link is down has no limit.
struct OutboxWaitTests {
    private static let t0 = Date(timeIntervalSince1970: 1_000_000)

    struct Case: Sendable, CustomTestStringConvertible {
        let label: String
        let outcome: SendOutcome
        let since: Date?
        let after: TimeInterval
        let linkUp: Bool
        let expectedOutcome: SendOutcome
        let expectedSince: Date?
        var expectedGaveUp = false
        var testDescription: String { label }
    }

    static let cases: [Case] = [
        Case(label: "a first wait on an up link starts the run", outcome: .waiting, since: nil, after: 0, linkUp: true,
             expectedOutcome: .waiting, expectedSince: t0),
        Case(label: "a wait within the limit keeps the run's start", outcome: .waiting, since: t0,
             after: OutboxWait.connectedLimit - 1, linkUp: true, expectedOutcome: .waiting, expectedSince: t0),
        Case(label: "a wait at the limit fails the message", outcome: .waiting, since: t0, after: OutboxWait.connectedLimit,
             linkUp: true, expectedOutcome: .failed, expectedSince: nil, expectedGaveUp: true),
        Case(label: "a wait while the link is down never fails and ends the run", outcome: .waiting, since: t0, after: 3600,
             linkUp: false, expectedOutcome: .waiting, expectedSince: nil),
        Case(label: "effort can wait through a long turn on a connected link", outcome: .effortWaiting, since: t0, after: 3600,
             linkUp: true, expectedOutcome: .effortWaiting, expectedSince: nil),
        Case(label: "a send ends the run", outcome: .sent, since: t0, after: 5, linkUp: true,
             expectedOutcome: .sent, expectedSince: nil),
    ]

    @Test(arguments: cases) func nextFollowsTheRunRules(_ c: Case) {
        let next = OutboxWait.next(c.outcome, since: c.since, now: Self.t0.addingTimeInterval(c.after), linkUp: c.linkUp)
        #expect(next == .init(outcome: c.expectedOutcome, since: c.expectedSince, gaveUp: c.expectedGaveUp), "\(c.label)")
    }
}
