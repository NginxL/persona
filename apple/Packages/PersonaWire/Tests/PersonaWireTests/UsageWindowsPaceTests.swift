import Foundation
import Testing

@testable import PersonaWire

@Suite struct UsageWindowsPaceTests {
    private let now = Date(timeIntervalSince1970: 1_793_600_000)

    private func fiveHours(used: Double) -> UsageWindow {
        UsageWindow(label: "5h", windowMinutes: 300, usedPercent: used, resetsAt: now.addingTimeInterval(3 * 3600))
    }

    @Test func freshReadingComputesPace() {
        #expect(UsageWindows.pace(fiveHours(used: 50), stale: false, now: now)?.state == .over)
        #expect(UsageWindows.pace(fiveHours(used: 30), stale: false, now: now)?.state == .under)
    }

    @Test func outOfDateReadingHasNoPace() {
        // #12411: an out-of-date reading keeps its numbers and reset, without a pace verdict.
        #expect(UsageWindows.pace(fiveHours(used: 50), stale: true, now: now) == nil)
        #expect(UsageWindows.pace(fiveHours(used: 30), stale: true, now: now) == nil)
    }
}
