import SwiftUI

import UIKit

/// Corrects an offset past the end after a text field grows (#909), without overriding manual draft scrolling (#5650).
enum TextViewScroll {
    /// The offset to keep: the view's own, unless it is past the last position its content allows.
    nonisolated static func settled(offset: CGFloat, content: CGFloat, height: CGFloat, insetTop: CGFloat, insetBottom: CGFloat) -> CGFloat {
        let top = -insetTop
        let end = max(top, content + insetBottom - height)
        return min(offset, end)
    }

    @MainActor static func settle(_ view: UIScrollView) {
        guard !view.isTracking, !view.isDragging, !view.isDecelerating else { return }
        let inset = view.adjustedContentInset
        let y = settled(offset: view.contentOffset.y, content: view.contentSize.height, height: view.bounds.height,
                        insetTop: inset.top, insetBottom: inset.bottom)
        guard y < view.contentOffset.y - 0.5 else { return }
        view.setContentOffset(CGPoint(x: view.contentOffset.x, y: y), animated: false)
    }

    /// Only edits request caret following. Geometry changes just settle an out-of-range offset; UIKit already follows
    /// a cursor the person moves. Capture the original view, never a different responder that gained focus meanwhile.
    @MainActor static func settleFocused(revealCaret: Bool = false) {
        guard let view = FirstResponder.current as? UITextView else { return }
        scheduleSettling(view, revealCaret: revealCaret)
    }

    /// A later manual scroll or selection change wins over already queued edit work.
    @MainActor static func scheduleSettling(_ view: UITextView, revealCaret: Bool = false) {
        let offset = view.contentOffset
        let selection = view.selectedRange
        let text = view.text
        let focused = view.isFirstResponder
        DispatchQueue.main.async { [weak view] in
            guard let view, view.isFirstResponder == focused,
                  !view.isTracking, !view.isDragging, !view.isDecelerating else { return }
            let followsEdit = revealCaret && view.contentOffset == offset && view.selectedRange == selection && view.text == text
            if followsEdit { view.layoutIfNeeded() }
            settle(view)
            if followsEdit, selection.location != NSNotFound {
                view.scrollRangeToVisible(selection)
            }
        }
    }
}

/// Whatever UIKit says has focus: an action sent to no target goes to the first responder, which records itself.
enum FirstResponder {
    nonisolated(unsafe) fileprivate static weak var found: UIResponder?

    @MainActor static var current: UIResponder? {
        found = nil
        UIApplication.shared.sendAction(#selector(UIResponder.personaRecordFirstResponder(_:)), to: nil, from: nil, for: nil)
        defer { found = nil }
        return found
    }
}

extension UIResponder {
    @objc fileprivate func personaRecordFirstResponder(_ sender: Any?) { FirstResponder.found = self }
}
