import UIKit

// Pages that draw their own header hide the navigation bar (the Agents chat), and UIKit then switches off the
// edge swipe back. Keep it on whenever there is somewhere to go back to.
extension UINavigationController: @retroactive UIGestureRecognizerDelegate {
    override open func viewDidLoad() {
        super.viewDidLoad()
        interactivePopGestureRecognizer?.delegate = self
        #if os(iOS)
        ContentPopPriority.install(on: self)
        #endif
    }

    public func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
        viewControllers.count > 1
    }
}
#if os(iOS)
/// iOS 26's swipe back from anywhere on a page (`interactiveContentPopGestureRecognizer`) keeps the system's delegate for
/// everything (when it may begin, which touches it takes, who else may run), with one answer of its own: a scroll view that
/// cannot move sideways (the chat's list, a task's page) still begins its pan on a sideways drag and took the swipe from
/// the page, so its pan waits for the swipe back to fail. That happens at once when the finger goes up, down or left, so
/// scrolling and the leftward drag that quotes a message are as before (#9971). UIKit gives this swipe touches only on a
/// page that shows the system's Back (`docs/lessons/ios.md`); a delegate cannot change that.
final class ContentPopPriority: NSObject, UIGestureRecognizerDelegate {
    nonisolated(unsafe) private static var key: UInt8 = 0
    /// Read from NSObject's message forwarding, which is not on the main actor; UIKit asks only on the main thread.
    nonisolated(unsafe) private weak var system: UIGestureRecognizerDelegate?

    static func install(on navigation: UINavigationController) {
        // Before iOS 26 there is no swipe back from anywhere: the edge swipe is the only one.
        guard #available(iOS 26.0, *),
              let pop = navigation.interactiveContentPopGestureRecognizer, !(pop.delegate is ContentPopPriority) else { return }
        let priority = ContentPopPriority()
        priority.system = pop.delegate
        pop.delegate = priority
        objc_setAssociatedObject(navigation, &key, priority, .OBJC_ASSOCIATION_RETAIN_NONATOMIC)
    }

    func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer) -> Bool {
        if let scroll = other.view as? UIScrollView, other === scroll.panGestureRecognizer, !Self.scrollsSideways(scroll) { return true }
        return system?.gestureRecognizer?(gestureRecognizer, shouldBeRequiredToFailBy: other) ?? false
    }

    private static func scrollsSideways(_ scroll: UIScrollView) -> Bool {
        let inset = scroll.adjustedContentInset
        return scroll.alwaysBounceHorizontal || scroll.contentSize.width + inset.left + inset.right > scroll.bounds.width + 0.5
    }

    nonisolated override func responds(to aSelector: Selector!) -> Bool {
        super.responds(to: aSelector) || (system?.responds(to: aSelector) ?? false)
    }

    nonisolated override func forwardingTarget(for aSelector: Selector!) -> Any? {
        if let system, system.responds(to: aSelector) { return system }
        return super.forwardingTarget(for: aSelector)
    }
}
#endif
