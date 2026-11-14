import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Send Now on the queue strip sends at one tap (#7169, as the web since #7154), so what its confirmation used to say is
/// its VoiceOver hint: for a Claude, a running command keeps running in the background and a reply being written starts
/// again; for Persona, the step under way stops or goes on in the background. The UI tests (`QueueSteerUITests`,
/// `PersonaQueueUITests`) see that one tap sends; XCUITest cannot read a hint, so this reads it off the hosted strip.
@MainActor struct QueueStripHintTests {
    @Test(arguments: [nil, "Persona"] as [String?])
    func sendNowsHintSaysWhatSendingNowDoes(persona: String?) async throws {
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
        for _ in 0..<10 {
            try? await Task.sleep(for: .milliseconds(30))
        }

        let button = try #require(element(in: window, id: "queue-send-now"), "no Send Now in the strip")
        let expected = if let persona {
            String(localized: "\(persona) gets them right away instead of at its next step. What it is doing now stops, or goes on in the background when it can, and it starts again with your messages. [Queue Strip]")
        } else {
            String(localized: "Sends the queued messages to the agent now. A command it is running keeps running in the background; a reply it is writing starts again with your message. [Queue Strip]")
        }
        #expect(!expected.isEmpty && !expected.contains("[Queue Strip]"), "the catalog has no words for the hint: \(expected)")
        #expect(button.accessibilityHint == expected, "Send Now's hint: \(String(describing: button.accessibilityHint))")
        #expect(button.accessibilityLabel == String(localized: "Send Now [Queue Strip]"), "Send Now's name: \(String(describing: button.accessibilityLabel))")
    }

    /// The accessibility element with identifier `id` under `root`, through SwiftUI's elements and UIKit's subviews.
    private func element(in root: NSObject, id: String) -> NSObject? {
        var seen = Set<ObjectIdentifier>()
        var stack: [NSObject] = [root]
        while let node = stack.popLast() {
            guard seen.insert(ObjectIdentifier(node)).inserted else { continue }
            if (node as? UIAccessibilityIdentification)?.accessibilityIdentifier == id { return node }
            if let elements = node.accessibilityElements {
                stack += elements.compactMap { $0 as? NSObject }
            } else {
                let count = node.accessibilityElementCount()
                if count != NSNotFound, count > 0 {
                    stack += (0..<count).compactMap { node.accessibilityElement(at: $0) as? NSObject }
                }
            }
            if let view = node as? UIView { stack += view.subviews }
        }
        return nil
    }
}
