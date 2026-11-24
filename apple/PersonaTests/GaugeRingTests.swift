import Testing
@testable import Persona

struct GaugeRingTests {
    @Test func valuesPastTheGoalStayWithinOneRing() {
        #expect(GaugeRing.progress(1.8) == 1)
        #expect(GaugeRing.progress(-0.5) == 0)
        #expect(GaugeRing.progress(0.375) == 0.375)
    }
}
