import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// A planned task's permission on its card and in Edit (#3026): Auto, which the hub may settle an unset task to from
/// Claude Code's default mode, has its own name instead of reading as "Ask every step".
@MainActor struct PlanPermissionTests {
    @Test func autoHasItsOwnName() {
        #expect(PlanTaskRow.permissions.contains("auto"))
        #expect(PlanTaskRow.permissionLabel("auto") == "自动判断")
        #expect(PlanTaskRow.permissionLabel("default") == "每步都问")
        #expect(Set(PlanTaskRow.permissions.map(PlanTaskRow.permissionLabel)).count == PlanTaskRow.permissions.count)
    }

    @Test func readOnlyIsARequestNotASandbox() {
        #expect(String(localized: "Read-only") == "已要求只读")
    }
}

/// #3784 AC2: a plan-mode start's recorded post-plan mode reaches its existing card. Keep decoding independent of the
/// property's Swift name so this regression fails at runtime on a client that does not know the wire field yet.
@MainActor @Suite(.serialized)
struct PlanThenCardTests {
    private let folder = FileManager.default.temporaryDirectory.appending(path: "plan-then-\(UUID().uuidString)")
    private let autoRun = String(localized: "Approve & Auto-Run")
    private let approve = String(localized: "Approve [Interaction Views 905819]")

    @Test func planThenSurvivesInteractionDecodingAndSnapshotCoding() throws {
        let with = try interaction(#","plan_then":"default""#)
        let encoded = try JSONSerialization.jsonObject(with: JSON.encoder.encode(with)) as? [String: Any]
        #expect(encoded?["plan_then"] as? String == "default")

        let without = try interaction()
        let legacy = try JSONSerialization.jsonObject(with: JSON.encoder.encode(without)) as? [String: Any]
        #expect(legacy?["plan_then"] == nil)
    }

    @Test(arguments: [(AppStyle.glass, "default"), (.beautiful, "default"), (.glass, "auto"), (.beautiful, "auto")])
    func recordedModeIsShownWithOneApprovalThatSendsIt(style: AppStyle, mode: String) async throws {
        try automation(true)
        defer { try? automation(false) }
        let previous = AppStyle.current
        AppStyle.current = style
        defer { AppStyle.current = previous }
        let ix = try interaction(",\"plan_then\":\"\(mode)\"")
        let hub = FakeHub()
        let path = "/api/v1/interactions/ix_plan/resolve"
        hub.on("POST", path, json: #"{"ok":true}"#)
        let (window, _) = try host(ix, hub: hub)
        defer { window.isHidden = true }

        let elements = await waitForApproval(in: window)
        let labels = elements.map(\.label)
        let modeLabel = PlanTaskRow.permissionLabel(mode)
        let edits = [String(localized: "Auto-Accept Edits"), String(localized: "Approve, Auto-Accept Edits")]
        #expect(labels.contains { $0.contains(modeLabel) }, "the card names the recorded mode: \(labels)")
        #expect(!labels.contains { $0.contains(autoRun) }, "a conflicting Allow Everything action is absent: \(labels)")
        #expect(!labels.contains { label in edits.contains { label.contains($0) } },
                "a conflicting edit-mode action is absent: \(labels)")
        let button = try #require(elements.first { element in
            element.traits.contains(.button) && element.label.contains(approve)
                && !element.label.contains(autoRun) && !edits.contains { element.label.contains($0) }
        }, "one plan approval, rather than a choice of modes: \(labels)")
        try #require(button.object.accessibilityActivate())

        var requests = hub.sent("POST", path)
        for _ in 0..<30 where requests.isEmpty {
            try? await Task.sleep(for: .milliseconds(100))
            requests = hub.sent("POST", path)
        }
        let body = try #require(requests.first?.body)
        let answer = try JSON.decoder.decode(Resolution.self, from: body)
        #expect(answer.approve == true)
        #expect(answer.mode == mode)
    }

    @Test(arguments: [AppStyle.glass, .beautiful])
    func noRecordedModeKeepsBothExistingModeActions(style: AppStyle) async throws {
        try automation(true)
        defer { try? automation(false) }
        let previous = AppStyle.current
        AppStyle.current = style
        defer { AppStyle.current = previous }
        let ix = try interaction()
        let (window, _) = try host(ix, hub: FakeHub())
        defer { window.isHidden = true }
        let edits = String(localized: style.usesBeautifulUI ? "Approve, Auto-Accept Edits" : "Auto-Accept Edits")

        var labels = cardElements(in: window).map(\.label)
        for _ in 0..<30 where !labels.contains(where: { $0.contains(autoRun) })
            || !labels.contains(where: { $0.contains(edits) }) {
            try? await Task.sleep(for: .milliseconds(100))
            labels = cardElements(in: window).map(\.label)
        }
        #expect(labels.contains { $0.contains(autoRun) }, "legacy Allow Everything remains: \(labels)")
        #expect(labels.contains { $0.contains(edits) }, "legacy Auto-Accept Edits remains: \(labels)")
    }

    private func interaction(_ extra: String = "") throws -> Interaction {
        let json = #"{"id":"ix_plan","machine_id":"m","session_id":"","kind":"plan","plan":"Ship the change""# + extra + "}"
        return try JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
    }

    private func host(_ ix: Interaction, hub: FakeHub) throws -> (UIWindow, AppModel) {
        let model = AppModel(stateFolder: folder)
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.replaceInteractions("", [ix])
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        window.rootViewController = UIHostingController(rootView: PlanForm(interaction: ix, showFull: .constant(false)).environment(model))
        window.isHidden = false
        window.layoutIfNeeded()
        return (window, model)
    }

    private func waitForApproval(in window: UIWindow) async -> [RuntimeCLI.Element] {
        let approvalLabels = [autoRun, approve]
        var elements = cardElements(in: window)
        for _ in 0..<30 where !elements.contains(where: { element in
            element.traits.contains(.button) && approvalLabels.contains { element.label.contains($0) }
        }) {
            try? await Task.sleep(for: .milliseconds(100))
            elements = cardElements(in: window)
        }
        return elements
    }

    private func cardElements(in window: UIWindow) -> [RuntimeCLI.Element] {
        RuntimeCLI.elements(matching: [:]).filter { RuntimeCLI.view(of: $0.object)?.window === window }
    }

    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
}
