import CoreGraphics
import Foundation
import Synchronization

/// Where the small window's source goes (#6333). The system's Picture in Picture transition starts from that layer's
/// frame and, on Restore, ends there: it used to sit in the bottom-left corner of the main window, so a small copy of
/// the picture popped up there before it jumped into the small window. With the page up it lies exactly over the page's
/// picture (the whole framebuffer as the page draws it, in the window's points), so the picture itself shrinks into the
/// small window and grows back; with no page, in the top trailing corner, where the small window comes up.
nonisolated enum VNCPiPPlacement {
    /// The source's frame in a window of `bounds`: over `page` (the page's picture, in the same window's points) when
    /// there is one, else `size` in the top trailing corner, `margin` in from the side and under the safe area.
    static func source(page: CGRect?, in bounds: CGRect, safeTop: CGFloat, size: CGSize, margin: CGFloat) -> CGRect {
        if let page, page.width >= 1, page.height >= 1 { return page }
        return CGRect(x: bounds.maxX - size.width - margin, y: bounds.minY + safeTop + margin, width: size.width, height: size.height)
    }

    /// The page's picture the source lies over: the part the small window shows. With frames cut to one display (Phone
    /// Screen) that is the display as the page draws it; with whole frames, the whole picture, even where only one
    /// display shows.
    static func page(_ picture: VNCPagePicture?, cut: Bool) -> CGRect? {
        guard let picture else { return nil }
        return cut ? picture.shown : picture.whole
    }

    /// The source's size where no page shows the picture: `width` wide in the shape of what the small window shows
    /// (`picture`, pixels: the Mac's whole picture or Phone Screen's display), 16:10 before it is known; a portrait one
    /// is `width` wide too, so it is taller.
    static func size(_ picture: CGSize, width: CGFloat) -> CGSize {
        let ratio = picture.width > 1 && picture.height > 1 ? picture.height / picture.width : 10.0 / 16
        return CGSize(width: width, height: (width * ratio).rounded())
    }
}

/// Where the live screen's page draws the Mac's picture, in screen points (VNCScreenUIView.onPicture): all of the
/// framebuffer (zoomed in or showing one display, partly off the view), and the part shown (the display shown, or all
/// of it).
nonisolated struct VNCPagePicture: Equatable, Sendable {
    var whole: CGRect
    var shown: CGRect
}

/// Restore's completion (#6333): the system grows the small window back into the source once it is called, so it waits
/// until the page has come back and the source lies over its picture, or `wait` at the latest. Answered once, by
/// whichever comes first; the wait runs on a timer of its own, so a main thread kept busy (the page opening on a loaded
/// device) never holds the system's restore. A wait left over from an earlier restore leaves a newer one alone.
nonisolated final class VNCPiPRestore: Sendable {
    static let wait: Duration = .milliseconds(600)
    private let state = Mutex<(turn: Int, done: (@Sendable (Bool) -> Void)?)>((0, nil))

    var pending: Bool { state.withLock { $0.done != nil } }

    /// A restore began; one still waiting (it cannot, but) is answered first.
    func arm(after wait: Duration = VNCPiPRestore.wait, _ done: @escaping @Sendable (Bool) -> Void) {
        finish()
        let turn = state.withLock { s in
            s.turn += 1
            s.done = done
            return s.turn
        }
        let (seconds, attoseconds) = wait.components
        DispatchQueue.global(qos: .userInteractive).asyncAfter(deadline: .now() + Double(seconds) + Double(attoseconds) / 1e18) { [self] in
            answer(state.withLock { s in s.turn == turn ? take(&s) : nil })
        }
    }

    /// The page's picture is in place, or the restore is over: the system may animate.
    func finish() {
        answer(state.withLock { s in take(&s) })
    }

    private func take(_ s: inout (turn: Int, done: (@Sendable (Bool) -> Void)?)) -> (@Sendable (Bool) -> Void)? {
        let d = s.done
        s.done = nil
        return d
    }

    private func answer(_ done: (@Sendable (Bool) -> Void)?) {
        done?(true)
    }
}
