import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Bounded iOS evidence for the exact shared source also consumed by #9694's Android test.
/// This is a test-only comparison, not a production integration or a performance measurement.
@MainActor @Suite(.serialized) struct AboutVersionRefractTests {
    @Test func theImmutableAdapterPreservesTheCombinedVersion() {
        let snapshot = AboutVersionSnapshot(shortVersion: "1.42.0", buildNumber: "37")
        #expect(snapshot.displayVersion == "1.42.0 (37)")
        #expect(ImmutableAboutVersionModel(snapshot: snapshot).versionName == "1.42.0 (37)")
        #expect(AboutVersionSnapshot(shortVersion: "", buildNumber: "").displayVersion == " ()")
    }

    nonisolated struct ComparisonCase: Sendable {
        let language: String
        let scheme: ColorScheme
        let textSize: DynamicTypeSize
    }

    nonisolated static let comparisons = [
        ComparisonCase(language: "en", scheme: .light, textSize: .large),
        ComparisonCase(language: "en", scheme: .light, textSize: .xxxLarge),
        ComparisonCase(language: "en", scheme: .dark, textSize: .large),
        ComparisonCase(language: "en", scheme: .dark, textSize: .xxxLarge),
        ComparisonCase(language: "zh-Hans", scheme: .light, textSize: .large),
        ComparisonCase(language: "zh-Hans", scheme: .light, textSize: .xxxLarge),
        ComparisonCase(language: "zh-Hans", scheme: .dark, textSize: .large),
        ComparisonCase(language: "zh-Hans", scheme: .dark, textSize: .xxxLarge),
        ComparisonCase(language: "ja", scheme: .light, textSize: .large),
        ComparisonCase(language: "ja", scheme: .light, textSize: .xxxLarge),
        ComparisonCase(language: "ja", scheme: .dark, textSize: .large),
        ComparisonCase(language: "ja", scheme: .dark, textSize: .xxxLarge),
    ]

    @Test(arguments: comparisons)
    func theSharedRowMatchesTheNativeControl(_ testCase: ComparisonCase) async throws {
        let (language, scheme, textSize) = (testCase.language, testCase.scheme, testCase.textSize)
        let snapshot = AboutVersionSnapshot(shortVersion: "1.42.0", buildNumber: "37")
        let stem = "\(language)-\(scheme)-\(textSize == .large ? "standard" : "xxxlarge")"
        let (native, shared) = try await compare(snapshot: snapshot, language: language,
                                                 scheme: scheme, textSize: textSize, stem: stem)
        #expect(shared.png == native.png, "Shared/native pixels differ: \(language), \(scheme), \(textSize)")
        #expect(shared.height >= Theme.Size.tapTarget && native.height >= Theme.Size.tapTarget)
        #expect(shared.size == native.size)
        #expect(shared.nonBackgroundPixelCount > 32)
    }

    @Test func theSharedRowMatchesTheActualAboutPageVersionCell() async throws {
        let snapshot = AboutVersionSnapshot(bundle: .main)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 402, height: 874)
        window.rootViewController = UIHostingController(rootView: SettingsForm {
            Section { SettingsAboutPage() }
            Section { AboutVersionRow(model: ImmutableAboutVersionModel(snapshot: snapshot)) }
        }
            .environment(\.locale, Locale(identifier: "en"))
            .environment(\.colorScheme, ColorScheme.light)
            .environment(\.dynamicTypeSize, DynamicTypeSize.large)
            .environment(AppModel.shared))
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        for _ in 0..<12 {
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(25))
        }
        let cells = allSubviews(of: window).filter {
            NSStringFromClass(type(of: $0)).contains("ListCollectionViewCell") && !$0.isHidden && $0.alpha > 0
        }.sorted { $0.frame.minY < $1.frame.minY }
        #expect(cells.count >= 2)
        let production = try capture(cells[0], in: window)
        let shared = try capture(cells[cells.count - 1], in: window)
        #expect(shared.png == production.png)
        #expect(snapshot.displayVersion.contains(" ("))
        #expect(!snapshot.displayVersion.hasSuffix(" ()"))
        try save(native: production.png, shared: shared.png, stem: "actual-about-page-en-light-standard")
    }

    @Test func theNativeMappingUsesTheProductionLocalizedControl() throws {
        let mapping = try String(contentsOf: nativeMapping, encoding: .utf8)
        #expect(mapping.contains("let title: String"))
        #expect(mapping.contains("title == \"settings.about.version\" ? \"Version\""))
        #expect(mapping.contains("LabeledContent(title == \"settings.about.version\" ? \"Version\""))
        #expect(mapping.contains("protocol RefractModel {}"))
        #expect(expectedVersionTitle("en") == "Version")
        #expect(expectedVersionTitle("zh-Hans") == "版本")
        #expect(expectedVersionTitle("ja") == "バージョン")
        // The test host does not expose this List cell as a UIKit accessibility element.
        // This source-contract check is not a VoiceOver or spoken-label/value verification.
    }

    @Test func theUnchangedProductionPageIsNotRewritten() throws {
        let source = try String(contentsOf: productionAbout, encoding: .utf8)
        #expect(source.contains(#"Section { LabeledContent("Version", value: version) }"#))
        #expect(source.contains(#"Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString")"#))
        #expect(source.contains(#"Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion")"#))
        #expect(!source.contains("AboutVersionRow("))
    }

    private let productionAbout = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "Persona/Views/Settings/SettingsNavigation.swift")
    private let nativeMapping = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().appending(path: "AboutVersionRefractSupport.swift")

    private struct Drawing {
        let png: Data
        let size: CGSize
        let height: CGFloat
        let nonBackgroundPixelCount: Int
    }

    private func compare(snapshot: AboutVersionSnapshot, language: String, scheme: ColorScheme,
                         textSize: DynamicTypeSize, stem: String) async throws -> (Drawing, Drawing) {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 402, height: 874)
        window.overrideUserInterfaceStyle = scheme == .dark ? .dark : .light
        window.rootViewController = UIHostingController(rootView: SettingsForm {
            Section { LabeledContent("Version", value: snapshot.displayVersion) }
            Section { AboutVersionRow(model: ImmutableAboutVersionModel(snapshot: snapshot)) }
        }
            .environment(\.locale, Locale(identifier: language))
            .environment(\.colorScheme, scheme)
            .environment(\.dynamicTypeSize, textSize))
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        for _ in 0..<12 {
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(25))
        }
        let cells = allSubviews(of: window).filter {
            NSStringFromClass(type(of: $0)).contains("ListCollectionViewCell") && !$0.isHidden && $0.alpha > 0
        }.sorted { $0.frame.minY < $1.frame.minY }
        #expect(cells.count == 2)
        let native = try capture(cells[0], in: window)
        let shared = try capture(cells[1], in: window)
        try save(native: native.png, shared: shared.png, stem: stem)
        return (native, shared)
    }

    private func capture(_ row: UIView, in window: UIWindow) throws -> Drawing {
        let parent = try #require(row.superview)
        let bounds = parent.bounds
        #expect(bounds.width > 0 && bounds.height >= Theme.Size.tapTarget)
        let format = UIGraphicsImageRendererFormat()
        format.scale = window.screen.scale
        let image = UIGraphicsImageRenderer(bounds: bounds, format: format).image { _ in
            #expect(parent.drawHierarchy(in: bounds, afterScreenUpdates: true))
        }
        return try Drawing(png: #require(image.pngData()), size: bounds.size, height: bounds.height,
                           nonBackgroundPixelCount: try nonBackgroundPixels(image))
    }

    private func nonBackgroundPixels(_ image: UIImage) throws -> Int {
        let cg = try #require(image.cgImage)
        var pixels = [UInt8](repeating: 0, count: cg.width * cg.height * 4)
        let context = try #require(CGContext(data: &pixels, width: cg.width, height: cg.height,
                                            bitsPerComponent: 8, bytesPerRow: cg.width * 4,
                                            space: CGColorSpaceCreateDeviceRGB(),
                                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        context.draw(cg, in: CGRect(x: 0, y: 0, width: cg.width, height: cg.height))
        return stride(from: 0, to: pixels.count, by: 4).reduce(into: 0) { count, offset in
            if pixels[offset + 3] > 0, pixels[offset] != pixels[offset + 1] || pixels[offset + 1] != pixels[offset + 2] {
                count += 1
            }
        }
    }

    private func allSubviews(of view: UIView) -> [UIView] {
        view.subviews.flatMap { [$0] + allSubviews(of: $0) }
    }

    private func expectedVersionTitle(_ language: String) -> String {
        switch language {
        case "en": "Version"
        case "zh-Hans": "版本"
        case "ja": "バージョン"
        default: "Version"
        }
    }

    private func save(native: Data, shared: Data, stem: String) throws {
        guard let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let root = URL(fileURLWithPath: directory, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try native.write(to: root.appendingPathComponent("native-\(stem).png"))
        try shared.write(to: root.appendingPathComponent("shared-\(stem).png"))
    }
}
