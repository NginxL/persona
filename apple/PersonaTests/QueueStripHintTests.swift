import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Send Now on the queue strip sends at one tap (#7169, as the web since #7154), so what its confirmation used to say is
/// its VoiceOver hint: for a Claude, a running command keeps running in the background and a reply being written starts
/// again; for Persona, the step under way stops or goes on in the background. The UI tests (`QueueSteerUITests`,
/// `PersonaQueueUITests`) see that one tap sends; XCUITest cannot read a hint, so this reads what VoiceOver gets off the
/// hosted strip (`RuntimeCLI.elements`, the elements VoiceOver moves through). The button sits in the disclosure's row,
/// so VoiceOver has one element there: the row, whose actions offer Send Now and whose hint is the button's.
@MainActor struct QueueStripHintTests {
    @Test(arguments: [nil, "Persona"] as [String?])
    func voiceOverHearsWhatSendingNowDoes(persona: String?) async throws {
        // With no assistive technology running SwiftUI builds no accessibility elements: turn on what XCUITest turns on
        // (`RuntimeCLI.enableAccessibilityTree`), and off again after, for the tests that follow in this process.
        try automation(true)
        defer { try? automation(false) }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 240)
        let strip = QueueStrip(key: SessionKey(machineID: "macbook", sessionID: "queue-hint"), queued: ["Update the screenshots too"],
                               own: 1, steers: true, persona: persona)
            .environment(AppModel())
        window.rootViewController = UIHostingController(rootView: strip)
        window.isHidden = false
        window.layoutIfNeeded()

        var row = RuntimeCLI.elements(matching: ["id": "queue-strip"]).first?.object
        for _ in 0..<30 where row == nil {
            try? await Task.sleep(for: .milliseconds(100))
            row = RuntimeCLI.elements(matching: ["id": "queue-strip"]).first?.object
        }
        let element = try #require(row, "VoiceOver has no queue strip")
        let expected = if let persona {
            String(localized: "\(persona) gets them right away instead of at its next step. What it is doing now stops, or goes on in the background when it can, and it starts again with your messages. [Queue Strip]")
        } else {
            String(localized: "Sends the queued messages to the agent now. A command it is running keeps running in the background; a reply it is writing starts again with your message. [Queue Strip]")
        }
        #expect(!expected.isEmpty && !expected.contains("[Queue Strip]"), "the catalog has no words for the hint: \(expected)")
        #expect(element.accessibilityHint == expected, "the hint VoiceOver reads: \(String(describing: element.accessibilityHint))")
        let actions = (element.accessibilityCustomActions ?? []).map(\.name)
        #expect(actions == [String(localized: "Send Now [Queue Strip]")], "Send Now among the row's actions: \(actions)")
    }

    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
}
