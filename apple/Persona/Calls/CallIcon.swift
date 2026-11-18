#if os(iOS)
import CoreGraphics
import Foundation
import ImageIO
import os
import SwiftUI
import UniformTypeIdentifiers

/// The system call UI's icon (`ConversationManager.Configuration.iconTemplateImageData`): the assistant's mark as a
/// template, an alpha silhouette the system tints itself, `Theme.Size.callIconTemplate` points at 3× as Apple's
/// guidance for a call app's template asks. Mochi's body (the app icon's character), its outline included and its eyes
/// cut out, so it reads as Mochi even that small. Mochi stands for every look: a package's or an uploaded picture has no
/// outline of its own to cut. Plain Core Graphics, drawn and encoded once, off the main actor (`prepare`), before the
/// call manager is made with it (`PersonaCalls.readySystem`).
nonisolated enum CallIcon {
    /// The icon's side in pixels.
    static var pixels: Int { Int(Theme.Size.callIconTemplate * Theme.Size.callIconTemplateScale) }

    /// The drawing, started the first time `prepare` is asked (never at launch), on a background thread.
    private static let drawing = Task.detached(priority: .userInitiated) { draw() }
    private static let made = OSAllocatedUnfairLock<Made?>(initialState: nil)

    /// What `prepare` made, and where it was drawn (the tests: never on the main thread).
    struct Made: Sendable {
        var png: Data?
        var onMain: Bool
    }

    /// The PNG, and the thread it was drawn on.
    private static func draw() -> Made {
        Made(png: templatePNG(), onMain: Thread.isMainThread)
    }

    /// Draws and encodes the icon off the main actor, once; returns when it is ready (`ready`).
    static func prepare() async {
        let done = await drawing.value
        made.withLock { $0 = done }
    }

    /// The icon `prepare` made: nil until then (a manager made before it shows no icon), or if it could not be drawn.
    static var ready: Data? { made.withLock { $0?.png } }

    /// Where `prepare` drew it: nil before.
    static var drawnOnMainThread: Bool? { made.withLock { $0?.onMain } }

    /// The PNG, or nil if it could not be drawn (the call UI then shows no icon, as before).
    static func templatePNG() -> Data? {
        guard let image = template() else { return nil }
        let data = NSMutableData()
        guard let out = CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil) else { return nil }
        CGImageDestinationAddImage(out, image, nil)
        guard CGImageDestinationFinalize(out) else { return nil }
        return data as Data
    }

    /// The silhouette: black where Mochi is, clear everywhere else (the eyes too).
    static func template() -> CGImage? {
        let side = pixels
        guard let space = CGColorSpace(name: CGColorSpace.sRGB),
              let ctx = CGContext(data: nil, width: side, height: side, bitsPerComponent: 8, bytesPerRow: 0, space: space,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        let w = CGFloat(side)
        // Mochi's geometry has its origin at the top left, as UIKit's; Core Graphics' is at the bottom left.
        ctx.translateBy(x: 0, y: w)
        ctx.scaleBy(x: 1, y: -1)
        let body = Mochi.body(w).cgPath
        ctx.setFillColor(CGColor(gray: 0, alpha: 1))
        ctx.setStrokeColor(CGColor(gray: 0, alpha: 1))
        ctx.addPath(body)
        ctx.fillPath()
        ctx.setLineWidth(max(Mochi.strokeMin, w * Mochi.stroke))
        ctx.setLineJoin(.round)
        ctx.setLineCap(.round)
        ctx.addPath(body)
        ctx.strokePath()
        // The eyes, cut out: compact features, as the face draws them at a small size.
        ctx.setBlendMode(.clear)
        let r = Mochi.eyeRadius * Mochi.compactFeatures * w
        for eye in Mochi.eyes {
            ctx.fillEllipse(in: CGRect(x: eye.x * w - r, y: eye.y * w - r, width: 2 * r, height: 2 * r))
        }
        return ctx.makeImage()
    }
}
#endif
