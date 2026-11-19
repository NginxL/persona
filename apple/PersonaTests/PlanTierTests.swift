import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// Plan tasks carry an Agent preset (#2225). These tests read the real decoded task and the real row's accessibility
/// labels, rather than a duplicate label helper, so the untouched client fails at runtime.
@MainActor @Suite(.serialized)
struct PlanTierTests {
    private func task(_ extra: String = "") throws -> PlanTask {
        let json = #"{"title":"Review the change","prompt":"Review it","machine":"macbook","cwd":"/Users/me/code/weather""# + extra + "}"
        return try JSON.decoder.decode(PlanTask.self, from: Data(json.utf8))
    }

    @Test func decodesTierAndLeavesItAbsentOnAnOlderPlan() throws {
        #expect(TierWireProbe.string("tier", in: try task(#","tier":"review""#)) == "review")
        #expect(TierWireProbe.string("tier", in: try task()) == nil)
    }

    enum Presentation: String, Sendable {
        case tierWithLauncher, tierOnly, explicitModelWins, launcherOnly, defaultModel
    }

    /// The plan chip's precedence: model, then tier (with the task's launcher), then the existing launcher/default.
    @Test(arguments: [Presentation.tierWithLauncher, .tierOnly, .explicitModelWins, .launcherOnly, .defaultModel])
    func planRowShowsThePresetWithTheExistingModelPrecedence(_ presentation: Presentation) async throws {
        let extra: String
        let expected: String
        var forbidden: [String] = []
        let preset = String(localized: "Preset \("review")")
        switch presentation {
        case .tierWithLauncher:
            extra = #","launcher":"claude","tier":"review""#
            expected = "claude · \(preset)"
        case .tierOnly:
            extra = #","tier":"review""#
            expected = preset
        case .explicitModelWins:
            extra = #","launcher":"claude","tier":"review","model":"gw/model-x""#
            expected = "gw/model-x"
            forbidden = [preset]
        case .launcherOnly:
            extra = #","launcher":"claude""#
            expected = "claude"
        case .defaultModel:
            extra = ""
            expected = String(localized: "Default Model")
            forbidden = [preset]
        }
        let task = try task(extra)
        try automation(true)
        defer { try? automation(false) }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true }
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 300)
        window.rootViewController = UIHostingController(rootView:
            PlanTaskRow(number: 1, task: task, numbered: false).padding().environment(AppModel.shared))
        window.isHidden = false
        window.layoutIfNeeded()

        var labels: [String] = []
        for _ in 0..<40 {
            labels = RuntimeCLI.elements(matching: [:]).filter {
                RuntimeCLI.view(of: $0.object)?.window === window
            }.map(\.label)
            if labels.contains(task.title) { break }
            try await Task.sleep(for: .milliseconds(50))
        }
        #expect(labels.contains(task.title), "the hosted plan row did not render: \(labels)")
        #expect(labels.contains(expected), "expected ‘\(expected)’ in the plan row: \(labels)")
        for text in forbidden {
            #expect(!labels.contains(text), "an explicit model must win over its tier: \(labels)")
        }
    }

    /// Visual evidence of the real row, independent of any cached Assistant navigation state.
    @Test func tierRowsCaptureInLightAndDark() async throws {
        try automation(true)
        defer { try? automation(false) }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let inputs = [
            ("Tiered review", #","launcher":"claude","tier":"review""#),
            ("Tier without a launcher", #","tier":"review""#),
            ("Explicit model wins", #","launcher":"claude","tier":"review","model":"gw/model-x""#),
            ("Ordinary default model", ""),
        ]
        var tasks: [PlanTask] = []
        for (title, extra) in inputs {
            var item = try task(extra)
            item.title = title
            tasks.append(item)
        }
        for (name, appearance) in [("light", UIUserInterfaceStyle.light), ("dark", .dark)] {
            for (suffix, typeSize) in [("", DynamicTypeSize.large), ("-xxxl", .xxxLarge)] {
                let window = UIWindow(windowScene: scene)
                defer { window.isHidden = true }
                window.overrideUserInterfaceStyle = appearance
                window.backgroundColor = .systemBackground
                window.frame = CGRect(x: 0, y: 0, width: 390, height: 560)
                window.rootViewController = UIHostingController(rootView:
                    VStack(alignment: .leading, spacing: Theme.Space.xl) {
                        ForEach(tasks.indices, id: \.self) { index in
                            PlanTaskRow(number: index + 1, task: tasks[index], numbered: true)
                        }
                    }
                    .padding()
                    .background(Color(uiColor: .systemBackground))
                    .environment(\.dynamicTypeSize, typeSize)
                    .environment(AppModel.shared))
                window.isHidden = false
                window.layoutIfNeeded()
                for _ in 0..<4 {
                    window.layoutIfNeeded()
                    try await Task.sleep(for: .milliseconds(25))
                }
                let image = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
                    window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
                }
                let data = try #require(image.pngData())
                #expect(!data.isEmpty)
                if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
                    let folder = URL(fileURLWithPath: directory, isDirectory: true)
                    try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
                    try data.write(to: folder.appendingPathComponent("agent-preset-plan-card-\(name)\(suffix).png"))
                }
            }
        }
    }

    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
}
