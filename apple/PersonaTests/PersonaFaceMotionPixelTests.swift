#if os(iOS) || os(visionOS)
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The decomposed fixed poses are captured through the same hosted UIKit path as the original vector face. No golden
/// images enter the repository: the original PersonaFace at the same time is the oracle.
@MainActor @Suite(.serialized) struct PersonaFaceMotionPixelTests {
    private struct Pixels {
        var width: Int, height: Int
        var bytes: [UInt8]
        var image: CGImage
    }

    private struct Difference: CustomStringConvertible {
        var maximum = 0
        var maximumPoint = CGPoint(x: -1, y: -1)
        var over16 = 0
        var pixelCount = 0
        var byteTotal = 0
        var bounds = CGRect.null
        var description: String {
            let summary = "max channel difference \(maximum), \(over16)/\(pixelCount) pixels over 16, mean byte difference \(Double(byteTotal) / Double(max(pixelCount * 4, 1)))"
            return "\(summary); maximum at (\(Int(maximumPoint.x)), \(Int(maximumPoint.y))), over-16 bounds \(bounds)"
        }
    }

    @MainActor private final class Capture {
        let size: CGFloat, scheme: ColorScheme, scale: CGFloat
        let window: UIWindow
        let host: UIHostingController<AnyView>

        init(size: CGFloat, scheme: ColorScheme, scale: CGFloat) throws {
            self.size = size
            self.scheme = scheme
            self.scale = scale
            let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
            window = UIWindow(windowScene: scene)
            host = UIHostingController(rootView: AnyView(EmptyView()))
            window.frame = CGRect(origin: .zero, size: CGSize(width: size, height: size))
            window.backgroundColor = .clear
            window.windowLevel = .alert + 1
            window.overrideUserInterfaceStyle = scheme == .dark ? .dark : .light
            host.safeAreaRegions = []
            host.sizingOptions = []
            host.view.backgroundColor = .clear
            host.view.contentScaleFactor = scale
            window.rootViewController = host
            window.makeKeyAndVisible()
        }

        func close() {
            window.isHidden = true
            window.rootViewController = nil
        }

        func pixels<V: View>(_ view: V) async throws -> Pixels {
            host.rootView = AnyView(view
                .frame(width: size, height: size)
                .background(Color.clear)
                .environment(\.colorScheme, scheme)
                .environment(\.displayScale, scale))
            host.view.contentScaleFactor = scale
            window.layoutIfNeeded()
            try await Task.sleep(for: .milliseconds(100))
            window.layoutIfNeeded()

            return try captureOnce()
        }

        private func captureOnce() throws -> Pixels {
            let format = UIGraphicsImageRendererFormat()
            format.scale = scale
            format.opaque = false
            format.preferredRange = .standard
            let image = UIGraphicsImageRenderer(size: window.bounds.size, format: format).image { _ in
                window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
            }
            let cg = try #require(image.cgImage)
            guard cg.bitsPerComponent == 8, cg.bitsPerPixel == 32 else {
                throw NSError(domain: "PersonaFaceMotionPixelTests", code: 1)
            }
            let order = cg.bitmapInfo.rawValue & CGBitmapInfo.byteOrderMask.rawValue
            let alpha = try #require(CGImageAlphaInfo(rawValue: cg.bitmapInfo.rawValue & CGBitmapInfo.alphaInfoMask.rawValue))
            let little = order == CGBitmapInfo.byteOrder32Little.rawValue
            let big = order == CGBitmapInfo.byteOrder32Big.rawValue || order == CGBitmapInfo.byteOrderDefault.rawValue
            guard little || big else { throw NSError(domain: "PersonaFaceMotionPixelTests", code: 2) }
            let data = try #require(cg.dataProvider?.data)
            let source = try #require(CFDataGetBytePtr(data))
            var bytes = [UInt8](repeating: 0, count: cg.width * cg.height * 4)
            for y in 0..<cg.height {
                for x in 0..<cg.width {
                    let input = y * cg.bytesPerRow + x * 4, output = (y * cg.width + x) * 4
                    switch (alpha, little) {
                    case (.premultipliedFirst, true): // BGRA
                        bytes[output] = source[input + 2]; bytes[output + 1] = source[input + 1]
                        bytes[output + 2] = source[input]; bytes[output + 3] = source[input + 3]
                    case (.premultipliedLast, false): // RGBA
                        for channel in 0..<4 { bytes[output + channel] = source[input + channel] }
                    case (.premultipliedFirst, false): // ARGB
                        bytes[output] = source[input + 1]; bytes[output + 1] = source[input + 2]
                        bytes[output + 2] = source[input + 3]; bytes[output + 3] = source[input]
                    case (.premultipliedLast, true): // ABGR
                        bytes[output] = source[input + 3]; bytes[output + 1] = source[input + 2]
                        bytes[output + 2] = source[input + 1]; bytes[output + 3] = source[input]
                    default:
                        throw NSError(domain: "PersonaFaceMotionPixelTests", code: 3)
                    }
                }
            }
            return Pixels(width: cg.width, height: cg.height, bytes: bytes, image: cg)
        }
    }

    private func difference(_ a: Pixels, _ b: Pixels) -> Difference {
        guard a.width == b.width, a.height == b.height, a.bytes.count == b.bytes.count else {
            return Difference(maximum: 255, over16: 1, pixelCount: 1, byteTotal: 255)
        }
        var result = Difference()
        for index in stride(from: 0, to: a.bytes.count, by: 4) {
            let pixel = index / 4, x = pixel % a.width, y = pixel / a.width
            var largest = 0
            for channel in 0..<4 {
                let delta = abs(Int(a.bytes[index + channel]) - Int(b.bytes[index + channel]))
                largest = max(largest, delta)
                result.byteTotal += delta
            }
            if largest > result.maximum {
                result.maximum = largest
                result.maximumPoint = CGPoint(x: x, y: y)
            }
            if largest > 16 {
                result.over16 += 1
                result.bounds = result.bounds.union(CGRect(x: x, y: y, width: 1, height: 1))
            }
            result.pixelCount += 1
        }
        return result
    }

    private func saveDiagnostic(_ pixels: Pixels, name: String, state: PersonaState, time: TimeInterval,
                                size: CGFloat, scheme: ColorScheme, scale: CGFloat) {
        guard let root = ProcessInfo.processInfo.environment["PERSONA_TEST_ARTIFACTS"] else { return }
        guard (size == 40 && scheme == .light && scale == 2) || (size == 46 && scheme == .light && scale == 2) ||
            (size == 32 && scheme == .light && scale == 3) || (size == 32 && scheme == .dark && scale == 2) ||
            (size == 44 && scheme == .dark && scale == 3) || (size > 120 && scheme == .light) else { return }
        let directory = URL(fileURLWithPath: root, isDirectory: true)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let file = "\(state.rawValue)-t\(time)-s\(size)-\(scheme)-\(scale)x-\(name).png"
        let url = directory.appending(path: file)
        guard let data = UIImage(cgImage: pixels.image).pngData() else { return }
        do {
            try data.write(to: url)
            print("PersonaFaceMotion diagnostic: \(url.path)")
        } catch {
            print("PersonaFaceMotion diagnostic write failed: \(error)")
        }
    }

    @Test func twoCapturesOfTheOriginalRendererAreIdentical() async throws {
        let original = PersonaFace(size: 40, design: .mochi, state: .working, time: 0.47, moving: true)
        let capture = try Capture(size: 40, scheme: .light, scale: 2)
        defer { capture.close() }
        let first = try await capture.pixels(original)
        let second = try await capture.pixels(original)
        #expect(first.bytes == second.bytes, Comment(rawValue: "original capture control: \(difference(first, second))"))
    }

    private func compare(state: PersonaState, time: TimeInterval, size: CGFloat, scheme: ColorScheme,
                         scale: CGFloat) async throws {
        let original = PersonaFace(size: size, design: .mochi, state: state, time: time, moving: true)
        let moving = PersonaFaceMotion(face: original, animating: false, time: time)
        let capture = try Capture(size: size, scheme: scheme, scale: scale)
        defer { capture.close() }
        let expected = try await capture.pixels(original), actual = try await capture.pixels(moving)
        let diff = difference(expected, actual)
        saveDiagnostic(expected, name: "expected", state: state, time: time, size: size, scheme: scheme, scale: scale)
        saveDiagnostic(actual, name: "actual", state: state, time: time, size: size, scheme: scheme, scale: scale)
        let label = "\(state) t=\(time) size=\(size) \(scheme) \(scale)x attempts=1: \(diff)"
        print("PersonaFaceMotion comparison: \(label)")

        // Hosted shadows and transformed static layers rasterize differently from the original vectors. Keep those
        // differences small and localized; the logged maximum and bounds make a shifted or missing piece visible.
        let identity = time == 0 || state == .thinking
        let maximum = state == .needsYou ? 16 : state == .thinking ? 170 : identity ? 210 : 200
        let fraction = state == .needsYou ? 0.001 : identity ? 0.01 : 0.07
        let mean = state == .needsYou ? 0.2 : identity ? 0.35 : 2.2
        #expect(diff.maximum <= maximum, Comment(rawValue: label))
        #expect(Double(diff.over16) / Double(max(diff.pixelCount, 1)) <= fraction, Comment(rawValue: label))
        #expect(Double(diff.byteTotal) / Double(max(diff.pixelCount * 4, 1)) <= mean, Comment(rawValue: label))
    }

    @Test func unchangedFaceInsideOneStaticHostControlsTheHostingPath() async throws {
        let original = PersonaFace(size: 40, design: .mochi, state: .working, time: 0, moving: true)
        let hosted = StaticHostedFace(face: original, scheme: .light, scale: 2)
        let capture = try Capture(size: 40, scheme: .light, scale: 2)
        defer { capture.close() }
        let expected = try await capture.pixels(original)
        let actual = try await capture.pixels(hosted)
        #expect(expected.bytes == actual.bytes, Comment(rawValue: "single static host control: \(difference(expected, actual))"))
    }

    @Test func unchangedFractionalFaceInsideOneStaticHostControlsTheHostingPath() async throws {
        let size = Theme.Size.callAvatar * Theme.Size.callAvatarFace
        let original = PersonaFace(size: size, design: .mochi, state: .needsYou, time: 0, moving: true)
        let hosted = StaticHostedFace(face: original, scheme: .light, scale: 3)
        let capture = try Capture(size: size, scheme: .light, scale: 3)
        defer { capture.close() }
        let expected = try await capture.pixels(original)
        let actual = try await capture.pixels(hosted)
        print("PersonaFaceMotion fractional static-host control: \(difference(expected, actual))")
        #expect(expected.bytes == actual.bytes, Comment(rawValue: "fractional static host control: \(difference(expected, actual))"))
    }

    @Test func fixedRockKeepsTheBodyPivotStationary() throws {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 160, height: 160))
        let view = PersonaFaceMotionView(frame: CGRect(x: 10, y: 20, width: 40, height: 40))
        window.addSubview(view)
        defer { view.removeFromSuperview() }
        let face = PersonaFace(size: 40, design: .mochi, state: .needsYou, moving: true)
        view.configure(face: face, scheme: .light, displayScale: 2, animating: false, time: 0)
        let point = CGPoint(x: view.body.bounds.width * Mochi.pivot.x, y: view.body.bounds.height * Mochi.pivot.y)
        let before = view.body.layer.convert(point, to: view.layer)
        view.configure(face: face, scheme: .light, displayScale: 2, animating: false, time: 0.47)
        let after = view.body.layer.convert(point, to: view.layer)
        print("PersonaFaceMotion pivot: anchor \(view.body.layer.anchorPoint) bounds \(view.body.bounds) position \(view.body.layer.position) super \(view.body.superview?.bounds ?? .zero) before \(before) after \(after) transform \(view.body.layer.transform)")
        #expect(abs(before.x - after.x) < 0.001 && abs(before.y - after.y) < 0.001)
    }

    private var sizes: [CGFloat] {
        [32, Theme.Size.avatar, Theme.Size.tapTarget, Theme.Size.avatarL, 88,
         Theme.Size.callAvatar * Theme.Size.callAvatarFace]
    }

    @Test func identityPosesStayWithinTheMeasuredRasterizationTolerance() async throws {
        // Identity transforms avoid resampling, but nested hosting and shadow rasterization can still differ slightly.
        // Native BGRA/RGBA byte reordering avoids a second color conversion in the comparator.
        for state in [PersonaState.thinking, .working, .needsYou] {
            for size in sizes {
                for scheme in [ColorScheme.light, .dark] {
                    for scale in [CGFloat(2), 3] {
                        try await compare(state: state, time: 0, size: size, scheme: scheme, scale: scale)
                    }
                }
            }
        }
    }

    @Test func thinkingPhasePosesStayWithinTheMeasuredRasterizationTolerance() async throws {
        for size in sizes {
            for scheme in [ColorScheme.light, .dark] {
                for scale in [CGFloat(2), 3] {
                    try await compare(state: .thinking, time: 0.47, size: size, scheme: scheme, scale: scale)
                }
            }
        }
    }

    @Test func rotatedPosesStayWithinTheMeasuredRasterizationTolerance() async throws {
        for state in [PersonaState.working, .needsYou] {
            for size in sizes {
                for scheme in [ColorScheme.light, .dark] {
                    for scale in [CGFloat(2), 3] {
                        try await compare(state: state, time: 0.47, size: size, scheme: scheme, scale: scale)
                    }
                }
            }
        }
    }
}

private final class StaticHostedFaceView: UIView {
    private let host: UIHostingController<AnyView>

    init(face: PersonaFace, scheme: ColorScheme, scale: CGFloat) {
        host = UIHostingController(rootView: AnyView(face.environment(\.colorScheme, scheme).environment(\.displayScale, scale)))
        super.init(frame: .zero)
        host.safeAreaRegions = []
        host.sizingOptions = []
        host.view.backgroundColor = .clear
        host.view.contentScaleFactor = scale
        addSubview(host.view)
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }
    override func layoutSubviews() { super.layoutSubviews(); host.view.frame = bounds }
}

private struct StaticHostedFace: UIViewRepresentable {
    var face: PersonaFace
    var scheme: ColorScheme
    var scale: CGFloat

    func makeUIView(context: Context) -> StaticHostedFaceView {
        StaticHostedFaceView(face: face, scheme: scheme, scale: scale)
    }
    func updateUIView(_ view: StaticHostedFaceView, context: Context) {}
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: StaticHostedFaceView, context: Context) -> CGSize? {
        CGSize(width: face.size, height: face.size)
    }
}
#endif
