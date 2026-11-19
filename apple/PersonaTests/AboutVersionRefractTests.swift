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

    @Test func theNativeMappingUsesTheProductionLocalizedControl() throws {
        let mapping = try String(contentsOf: nativeMapping, encoding: .utf8)
        #expect(mapping.contains("let title: String"))
        #expect(mapping.contains("title == \"settings.about.version\" ? \"Version\""))
        #expect(mapping.contains("LabeledContent(title == \"settings.about.version\" ? \"Version\""))
        #expect(mapping.contains("protocol RefractModel {}"))
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
}

@MainActor @Suite(.serialized) struct AboutVersionCompactVisualTests {
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
        let pair = try await AboutVersionVisualHost.capture(sizeClass: .compact, stem: stem,
                                                            language: language, scheme: scheme, textSize: textSize)
        try pair.expectEqual(nonBackgroundPixels: 32)
    }

    @Test func theSharedRowMatchesTheActualAboutPageVersionCell() async throws {
        let snapshot = AboutVersionSnapshot(bundle: .main)
        #expect(snapshot.displayVersion.contains(" ("))
        #expect(!snapshot.displayVersion.hasSuffix(" ()"))
        let pair = try await AboutVersionVisualHost.captureProductionAndShared(sizeClass: .compact,
                                                                               stem: "actual-about-page-en-light-standard",
                                                                               snapshot: snapshot)
        try pair.expectEqual(nonBackgroundPixels: 32)
    }
}

@MainActor @Suite(.serialized) struct AboutVersionRegularVisualTests {
    @Test func theSharedRowMatchesTheNativeControlAtRegularWidth() async throws {
        let snapshot = AboutVersionSnapshot(shortVersion: "1.42.0", buildNumber: "37")
        let pair = try await AboutVersionVisualHost.capture(sizeClass: .regular,
                                                            stem: "regular-width-en-light-standard",
                                                            language: "en", scheme: .light, textSize: .large)
        try pair.expectEqual(nonBackgroundPixels: 32)
    }
}

@MainActor
private enum AboutVersionVisualHost {
    fileprivate struct Pair {
        let native: Drawing
        let shared: Drawing

        func expectEqual(nonBackgroundPixels: Int) throws {
            if native.pixels != shared.pixels {
                let difference = native.pixelDifference(shared.pixels, width: native.pixelWidth)
                print("ABOUT_PIXEL_DIFF \(difference)")
            }
            #expect(native.pixels == shared.pixels, "native/shared decoded cell pixels differ")
            #expect(native.scale == shared.scale && native.pixelWidth == shared.pixelWidth)
            #expect(native.nonBackgroundPixelCount > nonBackgroundPixels)
            #expect(shared.nonBackgroundPixelCount > nonBackgroundPixels)
            #expect(native.height >= Theme.Size.tapTarget && shared.height >= Theme.Size.tapTarget)
        }
    }

    fileprivate struct Drawing {
        let png: Data
        let pixels: [UInt8]
        let scale: CGFloat
        let pixelWidth: Int
        let height: CGFloat
        let nonBackgroundPixelCount: Int

        func pixelDifference(_ other: [UInt8], width: Int) -> String {
            guard pixels.count == other.count, width > 0 else { return "different buffer sizes" }
            var first = -1, last = -1, count = 0, minX = width, maxX = -1, minY = Int.max, maxY = -1
            for i in stride(from: 0, to: pixels.count, by: 4) where pixels[i] != other[i] ||
                pixels[i + 1] != other[i + 1] || pixels[i + 2] != other[i + 2] || pixels[i + 3] != other[i + 3] {
                if first < 0 { first = i / 4 }
                last = i / 4
                let x = (i / 4) % width, y = (i / 4) / width
                minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y)
                count += 1
            }
            return "differentPixels=\(count) first=\(first) last=\(last) bbox=(\(minX),\(minY))-(\(maxX),\(maxY))"
        }
    }

    fileprivate static func capture(sizeClass: UIUserInterfaceSizeClass, stem: String,
                                    language: String, scheme: ColorScheme, textSize: DynamicTypeSize) async throws -> Pair {
        let snapshot = AboutVersionSnapshot(shortVersion: "1.42.0", buildNumber: "37")
        let window = try makeWindow(sizeClass: sizeClass)
        let native = UIHostingController(rootView: SettingsForm {
            Section { LabeledContent("Version", value: snapshot.displayVersion) }
        }
            .environment(\.locale, Locale(identifier: language))
            .environment(\.colorScheme, scheme)
            .environment(\.dynamicTypeSize, textSize))
        let shared = UIHostingController(rootView: SettingsForm {
            Section { AboutVersionRow(model: ImmutableAboutVersionModel(snapshot: snapshot)) }
        }
            .environment(\.locale, Locale(identifier: language))
            .environment(\.colorScheme, scheme)
            .environment(\.dynamicTypeSize, textSize))
        return try await render(window: window, native: native, shared: shared, stem: stem)
    }

    fileprivate static func captureProductionAndShared(sizeClass: UIUserInterfaceSizeClass, stem: String,
                                                       snapshot: AboutVersionSnapshot) async throws -> Pair {
        let window = try makeWindow(sizeClass: sizeClass)
        let production = UIHostingController(rootView: SettingsAboutPage()
            .environment(\.locale, Locale(identifier: "en"))
            .environment(\.colorScheme, ColorScheme.light)
            .environment(\.dynamicTypeSize, DynamicTypeSize.large)
            .environment(AppModel.shared))
        let shared = UIHostingController(rootView: SettingsForm {
            Section { AboutVersionRow(model: ImmutableAboutVersionModel(snapshot: snapshot)) }
        }
            .environment(\.locale, Locale(identifier: "en"))
            .environment(\.colorScheme, ColorScheme.light)
            .environment(\.dynamicTypeSize, DynamicTypeSize.large))
        return try await render(window: window, native: production, shared: shared, stem: stem)
    }

    private static func makeWindow(sizeClass: UIUserInterfaceSizeClass) throws -> UIWindow {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first {
            $0.activationState == .foregroundActive && $0.traitCollection.horizontalSizeClass == sizeClass
        }, "No foreground scene with the suite's real size class")
        let window = UIWindow(windowScene: scene)
        window.frame = scene.screen.bounds
        window.makeKeyAndVisible()
        #expect(window.windowScene === scene)
        #expect(window.traitCollection.horizontalSizeClass == sizeClass)
        #expect(window.frame == scene.screen.bounds)
        #expect(window.bounds.size == scene.screen.bounds.size)
        return window
    }

    private static func render(window: UIWindow, native: UIViewController, shared: UIViewController,
                               stem: String) async throws -> Pair {
        let container = UIViewController()
        for (index, child) in [native, shared].enumerated() {
            container.addChild(child)
            child.view.frame = CGRect(x: 0, y: CGFloat(index) * 500, width: window.bounds.width, height: 500)
            child.view.autoresizingMask = [.flexibleWidth]
            container.view.addSubview(child.view)
            child.didMove(toParent: container)
        }
        window.rootViewController = container
        defer { window.isHidden = true }
        for _ in 0..<24 {
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(25))
        }
        let nativeCell = try #require(versionCell(in: native.view))
        let sharedCell = try #require(versionCell(in: shared.view))
        let windowPNG = try windowFrame(window)
        let nativeDrawing = try crop(nativeCell, of: window, windowPNG: windowPNG)
        let sharedDrawing = try crop(sharedCell, of: window, windowPNG: windowPNG)
        try save(window: window, native: nativeDrawing.png, shared: sharedDrawing.png, stem: stem)
        return Pair(native: nativeDrawing, shared: sharedDrawing)
    }

    private static func visibleCells(in host: UIView) -> [UIView] {
        allSubviews(of: host).filter {
            NSStringFromClass(type(of: $0)).contains("ListCollectionViewCell") && !$0.isHidden && $0.alpha == 1 &&
                $0.bounds.width > 0 && $0.bounds.height >= Theme.Size.tapTarget &&
                $0.convert($0.bounds, to: host).intersects(host.bounds)
        }.sorted { $0.convert($0.bounds, to: host).minY < $1.convert($0.bounds, to: host).minY }
    }

    private static func versionCell(in host: UIView) -> UIView? {
        visibleCells(in: host).first
    }

    private static func crop(_ cell: UIView, of window: UIWindow, windowPNG: Data? = nil) throws -> Drawing {
        let windowPNG = try windowPNG ?? windowFrame(window)
        let frame = cell.convert(cell.bounds, to: window)
        #expect(window.bounds.contains(frame))
        let image = try #require(UIImage(data: windowPNG)?.cgImage)
        let scale = CGFloat(image.width) / window.bounds.width
        let crop = frame.applying(CGAffineTransform(scaleX: scale, y: scale))
        let cropped = try #require(image.cropping(to: crop))
        let pixels = try rgbaPixels(cropped)
        let size = CGSize(width: cropped.width, height: cropped.height)
        let renderer = UIGraphicsImageRenderer(size: size)
        let png = try #require(renderer.image { context in
            context.cgContext.translateBy(x: 0, y: size.height)
            context.cgContext.scaleBy(x: 1, y: -1)
            context.cgContext.draw(cropped, in: CGRect(origin: .zero, size: size))
        }.pngData())
        let colored = stride(from: 0, to: pixels.count, by: 4).reduce(into: 0) { count, offset in
            if pixels[offset + 3] > 0, pixels[offset] != pixels[offset + 1] || pixels[offset + 1] != pixels[offset + 2] {
                count += 1
            }
        }
        return Drawing(png: png, pixels: pixels, scale: scale, pixelWidth: cropped.width,
                       height: cell.bounds.height,
                       nonBackgroundPixelCount: colored)
    }

    private static func windowFrame(_ window: UIWindow) throws -> Data {
        window.layoutIfNeeded()
        let format = UIGraphicsImageRendererFormat()
        format.scale = window.screen.scale
        let image = UIGraphicsImageRenderer(bounds: window.bounds, format: format).image { _ in
            #expect(window.drawHierarchy(in: window.bounds, afterScreenUpdates: true))
        }
        return try #require(image.pngData())
    }

    private static func rgbaPixels(_ image: CGImage) throws -> [UInt8] {
        var pixels = [UInt8](repeating: 0, count: image.width * image.height * 4)
        let context = try #require(CGContext(data: &pixels, width: image.width, height: image.height,
                                            bitsPerComponent: 8, bytesPerRow: image.width * 4,
                                            space: CGColorSpaceCreateDeviceRGB(),
                                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        context.draw(image, in: CGRect(x: 0, y: 0, width: image.width, height: image.height))
        return pixels
    }

    private static func saveDiagnostic(window: UIWindow, stem: String) throws {
        let root = try outputDirectory(stem: "diagnostic-" + stem)
        try windowFrame(window).write(to: root.appendingPathComponent("window-diagnostic-\(stem).png"))
        let description = allSubviews(of: window).map { view in
            let label = (view as? UILabel)?.text ?? ""
            return "\(NSStringFromClass(type(of: view))) frame=\(view.frame) bounds=\(view.bounds) hidden=\(view.isHidden) alpha=\(view.alpha) label=\(label)"
        }.joined(separator: "\n")
        try description.write(to: root.appendingPathComponent("hierarchy-\(stem).txt"), atomically: true, encoding: .utf8)
    }

    private static func save(window: UIWindow, native: Data, shared: Data, stem: String) throws {
        let root = try outputDirectory(stem: stem)
        try windowFrame(window).write(to: root.appendingPathComponent("window.png"))
        try native.write(to: root.appendingPathComponent("native.png"))
        try shared.write(to: root.appendingPathComponent("shared.png"))
    }

    private static func outputDirectory(stem: String) throws -> URL {
        let directory = try #require(ProcessInfo.processInfo.environment["PERSONA_SHOTS"])
        let root = URL(fileURLWithPath: directory, isDirectory: true).appendingPathComponent(stem, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

    private static func allSubviews(of view: UIView) -> [UIView] {
        view.subviews.flatMap { [$0] + allSubviews(of: $0) }
    }
}
