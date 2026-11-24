import CoreGraphics
import Foundation

/// Two fingers on the Mac's picture in Mouse Mode (#6196): a scroll (zoomed in, a move of the picture) or a pinch,
/// whichever the fingers show first, kept until they lift. Pinch and the two-finger pan are recognized together, and
/// any change in the fingers' spread used to zoom, so a scroll whose fingers drifted apart a little zoomed too (and,
/// zoomed in, turned into moving the picture). A pinch now takes the spread changing by `least` of what it was (and
/// `leastPoints` at least) and by more than the fingers travel together; a scroll, the fingers travelling `travel`
/// points together, at least as far as the spread changed. Until one shows, neither acts: the pan's travel is kept
/// for the scroll. Trackpad Mode has no pinch: its two fingers only scroll.
nonisolated struct VNCTwoFingers {
    enum Meaning { case undecided, scroll, zoom }

    /// The share of the fingers' spread it changes by before a pinch zooms.
    static let least: CGFloat = 0.15
    /// Points the spread changes by at least (fingers close together: their settling is not a pinch).
    static let leastPoints: CGFloat = 10
    /// Points the fingers travel together before it is a scroll.
    static let travel: CGFloat = 6

    private var center = CGPoint.zero
    private var spread: CGFloat = 0
    private var started = false
    /// The touch it was about has lifted: the next one starts afresh (its meaning is kept for the last callbacks).
    private var over = false
    private(set) var meaning = Meaning.undecided

    /// The second finger came down: a new touch, the fingers `spread` points apart around `center`.
    mutating func began(center: CGPoint, spread: CGFloat) {
        self = VNCTwoFingers()
        self.center = center
        self.spread = spread
        started = true
    }

    /// The fingers lifted.
    mutating func ended() { over = true }

    /// The fingers are now around `center`, `spread` points apart: what the touch is. A touch whose start was not seen
    /// starts here.
    @discardableResult
    mutating func moved(center: CGPoint, spread: CGFloat) -> Meaning {
        if over || !started { began(center: center, spread: spread) }
        guard meaning == .undecided else { return meaning }
        let opened = abs(spread - self.spread)
        let travelled = hypot(center.x - self.center.x, center.y - self.center.y)
        if opened >= max(self.spread * Self.least, Self.leastPoints), opened > travelled {
            meaning = .zoom
        } else if travelled >= Self.travel, travelled >= opened {
            meaning = .scroll
        }
        return meaning
    }
}

/// Where a zoomed-in picture of the Mac's screen goes in its view (#6196), in the view's points: a rect for the picture,
/// as `VNCScreenUIView.shown` keeps it.
nonisolated enum VNCZoom {
    /// What ⋯ › Zoom In zooms to: twice the picture as fitted.
    static let step: CGFloat = 2
    /// The most a pinch zooms in.
    static let most: CGFloat = 5

    /// A picture of `size` as large as it fits in `bounds`, in the middle.
    static func fit(_ size: CGSize, in bounds: CGRect) -> CGRect {
        guard size.width > 0, size.height > 0 else { return bounds }
        let k = min(bounds.width / size.width, bounds.height / size.height)
        let w = size.width * k, h = size.height * k
        return CGRect(x: bounds.midX - w / 2, y: bounds.midY - h / 2, width: w, height: h)
    }

    /// Phone Screen's picture (#9500): as wide as the view, never shrunk to fit its height. With the keyboard up the view is
    /// shorter than the picture: it stays at its size, its top at the view's top, moved up just enough that `pointer`
    /// (a point of the picture, 0…1, where the user last tapped) is `margin` above the view's bottom, as far as the picture
    /// reaches. The page uses it only with the phone upright (`VNCScreenUIView.fillsWidth`); on its side the plain fit.
    static func phoneFit(_ size: CGSize, in bounds: CGRect, pointer: CGPoint?, margin: CGFloat) -> CGRect {
        let plain = fit(size, in: bounds)
        guard size.width > 0, size.height > 0, bounds.width > 0, plain.width < bounds.width - 0.5,
              size.height / size.width > bounds.height / bounds.width else { return plain }
        let h = bounds.width * size.height / size.width
        var y = bounds.minY
        if let p = pointer, (0...1).contains(p.y) {
            let at = p.y * h // the pointer's height in the picture
            let room = bounds.height - margin
            if at > room { y = bounds.minY - min(at - room, h - bounds.height) }
        }
        return CGRect(x: bounds.minX, y: y, width: bounds.width, height: h)
    }

    /// `r` scaled by `k` with the view's point `anchor` staying where it is.
    static func zoomed(_ r: CGRect, by k: CGFloat, at anchor: CGPoint) -> CGRect {
        CGRect(x: anchor.x - (anchor.x - r.minX) * k, y: anchor.y - (anchor.y - r.minY) * k, width: r.width * k, height: r.height * k)
    }

    /// A zoomed picture kept over a view of `size` (centered on an axis it is narrower on).
    static func clamp(_ r: CGRect, in size: CGSize) -> CGRect {
        var r = r
        r.origin.x = r.width <= size.width ? (size.width - r.width) / 2 : min(0, max(size.width - r.width, r.minX))
        r.origin.y = r.height <= size.height ? (size.height - r.height) / 2 : min(0, max(size.height - r.height, r.minY))
        return r
    }

    /// The picture moved just enough that the view's point `p` (the pointer) is `margin` inside a view of `size`, as far
    /// as the picture reaches: in Trackpad Mode a zoomed-in picture follows the pointer.
    static func following(_ r: CGRect, keep p: CGPoint, in size: CGSize, margin: CGFloat) -> CGRect {
        var dx: CGFloat = 0, dy: CGFloat = 0
        if p.x < margin { dx = margin - p.x } else if p.x > size.width - margin { dx = size.width - margin - p.x }
        if p.y < margin { dy = margin - p.y } else if p.y > size.height - margin { dy = size.height - margin - p.y }
        guard dx != 0 || dy != 0 else { return r }
        return clamp(r.offsetBy(dx: dx, dy: dy), in: size)
    }

    /// A zoomed-in picture of `picture`'s size once its view went from `old` to `new` (the phone turned): as many times
    /// the new fit as it was the old one, the part that was in the middle of the view in the middle again. Nil: fitted.
    static func turned(_ r: CGRect, picture: CGSize, from old: CGRect, to new: CGRect) -> CGRect? {
        let before = fit(picture, in: old), after = fit(picture, in: new)
        guard before.width > 0, r.width > 0 else { return nil }
        let k = r.width / before.width
        guard k > 1.01 else { return nil }
        let u = CGPoint(x: (old.midX - r.minX) / r.width, y: (old.midY - r.minY) / r.height)
        let w = after.width * k, h = after.height * k
        return clamp(CGRect(x: new.midX - u.x * w, y: new.midY - u.y * h, width: w, height: h), in: new.size)
    }
}
