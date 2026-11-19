import Foundation
import BeautifulUI
import SwiftUI
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// A planned task's permission on its card and in Edit (#3026): Auto-review, which the hub may settle an unset task to from
/// Claude Code's default mode, has its own name instead of reading as "Default permissions".
@MainActor struct PlanPermissionTests {
    @Test func autoHasItsOwnName() {
        #expect(PlanTaskRow.permissions.contains("auto"))
        #expect(PlanTaskRow.permissionLabel("auto") == "自动审批")
        #expect(PlanTaskRow.permissionLabel("default") == "默认权限")
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

/// #10857 AC2: a node.sock script ask may mark its question as Markdown. The two wire shapes (a permission's summary
/// without options, a question with options) then use the chat reply's Markdown rendering; every other format keeps the
/// old CardCode / plain text. Raw JSON keeps these tests compiling before `Interaction.format` exists.
@MainActor @Suite(.serialized)
struct ScriptAskMarkdownTests {
    private static let source = """
    Choose **carefully** before continuing:

    - first item
    - second item

    | Choice | Result |
    | --- | --- |
    | A | Works |
    """
    private static let inlineSource = "Choose **carefully**, not `quickly`."

    @Test func markdownFormatSurvivesInteractionCoding() throws {
        for kind: InteractionKind in [.permission, .question] {
            let ix = try interaction(kind: kind, format: "markdown")
            let encoded = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(ix)) as? [String: Any])
            #expect(encoded["format"] as? String == "markdown", "the \(kind.rawValue) card drops its opt-in")
            #expect(ix.permissionTarget == nil || ix.permissionTarget == Self.source)
            #expect(ix.questions?.first.map { $0.question == Self.source } ?? true)

            let legacy = try interaction(kind: kind)
            let old = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(legacy)) as? [String: Any])
            #expect(old["format"] == nil)
        }
    }

    @Test(arguments: [AppStyle.glass, .beautiful])
    func permissionAskRendersMarkdownWhenOptedIn(style: AppStyle) async throws {
        try await rendersMarkdown(kind: .permission, style: style)
    }

    @Test(arguments: [AppStyle.glass, .beautiful])
    func questionAskRendersMarkdownWhenOptedIn(style: AppStyle) async throws {
        try await rendersMarkdown(kind: .question, style: style)
    }

    @Test(arguments: [AppStyle.glass, .beautiful], ["", "html"])
    func permissionAskKeepsCardCodeForMissingOrUnknownFormat(style: AppStyle, format: String) async throws {
        let labels = try await render(kind: .permission, format: format.isEmpty ? nil : format,
                                      source: Self.inlineSource, style: style, ready: Self.inlineSource)
        #expect(labels.map(\.label).joined(separator: "\n").contains(Self.inlineSource), "CardCode no longer shows the source: \(labels)")
        #expect(labels.contains { $0.id == "permission-command" }, "the existing CardCode is gone: \(labels.map(\.id))")
    }

    @Test(arguments: [AppStyle.glass, .beautiful], ["", "html"])
    func questionAskKeepsPlainTextForMissingOrUnknownFormat(style: AppStyle, format: String) async throws {
        let labels = try await render(kind: .question, format: format.isEmpty ? nil : format,
                                      source: Self.inlineSource, style: style, ready: Self.inlineSource)
        #expect(labels.map(\.label).joined(separator: "\n").contains(Self.inlineSource), "plain question no longer shows the source: \(labels)")
    }

    @Test func questionAnswerKeepsTheOriginalMarkdownSourceAsItsKey() throws {
        let ix = try interaction(kind: .question, format: "markdown", source: Self.inlineSource)
        let question = try #require(ix.questions?.first)
        #expect(question.question == Self.inlineSource)
        let picked = ApprovalAnswers(picks: [0: [0]], custom: [:])
        #expect(BUIQuestionForm.answers(picked, [question]) == [Self.inlineSource: "Proceed"])
    }

    private func rendersMarkdown(kind: InteractionKind, style: AppStyle) async throws {
        let labels = try await render(kind: kind, format: "markdown", source: Self.source, style: style, ready: "first item")
        let text = labels.map(\.label).joined(separator: "\n")
        for expected in ["Choose carefully before continuing:", "first item", "second item", "Choice", "Result", "Works"] {
            #expect(text.contains(expected), "missing rendered \(expected) in \(kind.rawValue): \(labels.map(\.label))")
        }
        #expect(!text.contains("**carefully**"), "inline Markdown is still literal in \(kind.rawValue): \(labels.map(\.label))")
        #expect(!text.contains("| --- | --- |"), "the short table is still literal in \(kind.rawValue): \(labels.map(\.label))")
    }

    private func interaction(kind: InteractionKind, format: String? = nil,
                             source: String = ScriptAskMarkdownTests.source) throws -> Interaction {
        var json: [String: Any] = [
            "id": "ix_script_ask", "machine_id": "m", "session_id": "", "kind": kind.rawValue,
            "agent": "script", "agent_label": "Script", "tool_name": "personad_ask",
        ]
        if kind == .question {
            json["questions"] = [[
                "question": source, "header": "Script", "multi_select": false,
                "options": [["label": "Proceed"], ["label": "Stop"]],
            ]]
        } else {
            json["summary"] = source
        }
        if let format { json["format"] = format }
        return try JSON.decoder.decode(Interaction.self, from: JSONSerialization.data(withJSONObject: json))
    }

    private struct Label {
        var id: String
        var label: String
    }

    private func render(kind: InteractionKind, format: String?, source: String, style: AppStyle,
                        ready: String) async throws -> [Label] {
        try automation(true)
        defer { try? automation(false) }
        let previous = AppStyle.current
        AppStyle.current = style
        defer { AppStyle.current = previous }
        let ix = try interaction(kind: kind, format: format, source: source)
        let (window, _) = try host(ix)
        defer { window.isHidden = true }

        var labels = cardElements(in: window)
        for _ in 0..<30 where !labels.contains(where: { $0.label.contains(ready) }) {
            try await Task.sleep(for: .milliseconds(100))
            labels = cardElements(in: window)
        }
        if ProcessInfo.processInfo.environment["PERSONA_SHOTS"] != nil {
            // Let BeautifulUI's entrance and the table's last layout settle before the evidence capture.
            try await Task.sleep(for: .milliseconds(1000))
        }
        shot(window, "ios-\(kind.rawValue)-\(style)-\(format ?? "plain")-\(appearance)-\(largeText ? "large" : "standard")")
        return labels.map { Label(id: $0.id, label: $0.label) }
    }

    private var appearance: String { ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light" }
    private var largeText: Bool { ProcessInfo.processInfo.environment["PERSONA_SHOTS_LARGE"] == "1" }

    private func host(_ ix: Interaction) throws -> (UIWindow, AppModel) {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "script-ask-\(UUID().uuidString)"))
        model.forgetRestoredForTests()
        model.apiForTests = FakeHub().client
        model.replaceInteractions("", [ix])
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        let scheme: ColorScheme = appearance == "dark" ? .dark : .light
        let size: ContentSizeCategory = largeText ? .accessibilityExtraExtraExtraLarge : .large
        window.rootViewController = UIHostingController(rootView: Group {
            if ix.kind == .permission { AgentPermissionForm(interaction: ix) } else { QuestionForm(interaction: ix) }
        }.environment(model).environment(\.colorScheme, scheme).environment(\.sizeCategory, size))
        window.overrideUserInterfaceStyle = scheme == .dark ? .dark : .light
        window.backgroundColor = .systemBackground
        window.isHidden = false
        window.layoutIfNeeded()
        return (window, model)
    }

    private func cardElements(in window: UIWindow) -> [RuntimeCLI.Element] {
        RuntimeCLI.elements(matching: [:]).filter { RuntimeCLI.view(of: $0.object)?.window === window }
    }

    /// Hosted-card pictures for UI review; ordinary unit runs do not write files.
    private func shot(_ window: UIWindow, _ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let image = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
            window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
        }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? image.pngData()?.write(to: URL(fileURLWithPath: dir + "/\(name).png"))
    }

    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
}
