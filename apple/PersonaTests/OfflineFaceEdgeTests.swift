import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The offline face's edge reads as sharp as the online one's (#11283). In dark mode the offline outline used to be a
/// mid gray between the dark disc and the gray body, so the body's edge was two small steps and looked blurred at a
/// glance (the owner on an iPhone Duo's top bar), although every step was one pixel wide. Measured as the largest
/// luminance change within one point (3 pixels at 3x) across the body's left edge, on the page's color. Light mode
/// keeps its look (the owner asked for no change there), so only dark is held to the online face's edge.
@MainActor struct OfflineFaceEdgeTests {
    private static let side: CGFloat = 44
    private static let scale: CGFloat = 3

    /// The largest change (0…1 luminance) within one point along a row through the body, from the disc's rim to the
    /// middle of the face.
    private func edgeStep(_ state: PersonaState, dark: Bool) throws -> Double {
        let view = ZStack {
            (dark ? Color.black : Color.white)
            PersonaFace(size: Self.side, design: .mochi, state: state, time: 0, moving: false, blink: nil)
        }
        .frame(width: Self.side, height: Self.side)
        .environment(\.colorScheme, dark ? .dark : .light)
        let r = ImageRenderer(content: view)
        r.scale = Self.scale
        let image = try #require(r.cgImage)
        let w = image.width, h = image.height
        var buf = [UInt8](repeating: 0, count: w * h * 4)
        let ctx = try #require(CGContext(data: &buf, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                         space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                         bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
        func luminance(_ x: Int, _ y: Int) -> Double {
            let i = (y * w + x) * 4
            return (0.2126 * Double(buf[i]) + 0.7152 * Double(buf[i + 1]) + 0.0722 * Double(buf[i + 2])) / 255
        }
        // A row through the body below the eyes and the cheek, clear of the shadow under it.
        let y = Int(Double(h) * 0.6)
        var best = 0.0
        let span = Int(Self.scale)
        for x in Int(Double(w) * 0.06)..<Int(Double(w) * 0.45) {
            best = max(best, abs(luminance(x + span, y) - luminance(x, y)))
        }
        return best
    }

    @Test func darkOfflineEdgeIsAsSharpAsOnline() throws {
        let offline = try edgeStep(.offline, dark: true)
        let online = try edgeStep(.idle, dark: true)
        print("edge step dark: offline \(offline), online \(online); light: offline \(try edgeStep(.offline, dark: false)), online \(try edgeStep(.idle, dark: false))")
        #expect(offline >= online * 0.8, "dark offline edge \(offline) against online \(online)")
    }
}
