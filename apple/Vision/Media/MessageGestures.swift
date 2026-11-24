import SwiftUI
import UIKit

// visionOS's versions of the UIKit gestures in MessageMenu.swift (UIGestureRecognizerRepresentable is iOS only):
// same names, same call sites, built from SwiftUI gestures, which visionOS feeds from look-and-pinch.

/// A long pinch on a message opens the action bar where the pinch landed (in the enclosing `messageActionBar` row's
/// coordinates, `MessageMenu.row`) and reports the press's phases, like the iPhone's long press.
struct LongPressAt: Gesture {
    var changed: (UIGestureRecognizer.State) -> Void = { _ in }
    var action: (CGPoint) -> Void

    var body: some Gesture {
        LongPressGesture(minimumDuration: 0.35)
            .sequenced(before: DragGesture(minimumDistance: 0, coordinateSpace: .named(MessageMenu.row)))
            .onChanged { value in
                // The drag half starts the moment the long press succeeds: its first value is the press point.
                guard case .second(true, let drag) = value, let drag, drag.translation == .zero else { return }
                changed(.began)
                action(drag.startLocation)
            }
            .onEnded { _ in changed(.ended) }
    }
}
