import Combine
import SwiftUI
import UIKit

/// The interface's cap (`Theme.interfaceLargestTypeSize`) on the windows' own text size. The environment's cap does not
/// reach a sheet, a full screen cover or a popover: each takes its size from its own trait collection, which is the
/// system's (Settings was drawn at AX5 under the capped page). A trait override on the window reaches everything shown
/// in it. Reading content lifts the cap in SwiftUI with `.readingContent()` and in UIKit by overriding its own view's
/// trait with `readingCategory`. visionOS has no window cap (#7420), but its RootView caps the environment too, so a
/// lifted message's text view (`LiftSelection`) still takes `readingCategory` there, the size the chat's words draw at.
enum WindowTextSize {
    /// The system's size, not the window's (which this overrides).
    @MainActor static var system: DynamicTypeSize { DynamicTypeSize(UIApplication.shared.preferredContentSizeCategory) ?? .large }

    /// What the reader chose (the system's, moved by Small / Standard / Large): reading content draws at it.
    @MainActor static var readingCategory: UIContentSizeCategory {
        UIContentSizeCategory(AppModel.shared.textSize.applied(to: system))
    }

    #if os(iOS)
    /// On every window of every scene; RootView runs it when it shows and when the system's size changes.
    @MainActor static func apply() {
        let capped = min(system, Theme.interfaceLargestTypeSize)
        for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
            for w in scene.windows {
                if capped < system {
                    w.traitOverrides.preferredContentSizeCategory = UIContentSizeCategory(capped)
                } else {
                    w.traitOverrides.remove(UITraitPreferredContentSizeCategory.self)
                }
            }
        }
    }
    #endif
}

private struct AccessibleConfirmationDialog<Actions: View, Message: View>: ViewModifier {
    let title: Text
    let isPresented: Binding<Bool>
    let titleVisibility: Visibility
    let hasCancel: Bool
    let showsMessage: Bool
    let actions: () -> Actions
    let message: () -> Message
    @State private var systemSize = WindowTextSize.system

    private var useAlert: Bool {
        #if os(iOS)
        systemSize > Theme.interfaceLargestTypeSize
        #else
        false // visionOS has no window cap, so its dialog labels and capsules already use the same size.
        #endif
    }

    @ViewBuilder func body(content: Content) -> some View {
        Group {
            if useAlert {
                if showsMessage {
                    content.alert(title, isPresented: isPresented) { alertActions() } message: { message() }
                } else {
                    content.alert(title, isPresented: isPresented) { alertActions() }
                }
            } else {
                if showsMessage {
                    content.confirmationDialog(title, isPresented: isPresented, titleVisibility: titleVisibility) {
                        actions()
                    } message: {
                        message()
                    }
                } else {
                    content.confirmationDialog(title, isPresented: isPresented, titleVisibility: titleVisibility) {
                        actions()
                    }
                }
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: UIContentSizeCategory.didChangeNotification)) { _ in
            let size = WindowTextSize.system
            if size != systemSize { systemSize = size }
        }
    }

    @ViewBuilder private func alertActions() -> some View {
        actions()
        if !hasCancel { Button("Cancel", role: .cancel) {} }
    }
}

private struct AccessibleItemConfirmationDialog<Actions: View, Message: View, Item>: ViewModifier {
    let title: Text
    let isPresented: Binding<Bool>
    let titleVisibility: Visibility
    let hasCancel: Bool
    let showsMessage: Bool
    let data: Item?
    let actions: (Item) -> Actions
    let message: (Item) -> Message
    @State private var systemSize = WindowTextSize.system

    private var useAlert: Bool {
        #if os(iOS)
        systemSize > Theme.interfaceLargestTypeSize
        #else
        false
        #endif
    }

    @ViewBuilder func body(content: Content) -> some View {
        Group {
            if useAlert {
                if showsMessage {
                    content.alert(title, isPresented: isPresented, presenting: data) { item in
                        alertActions(item)
                    } message: { item in
                        message(item)
                    }
                } else {
                    content.alert(title, isPresented: isPresented, presenting: data) { item in
                        alertActions(item)
                    }
                }
            } else {
                if showsMessage {
                    content.confirmationDialog(title, isPresented: isPresented, titleVisibility: titleVisibility,
                                               presenting: data) { item in
                        actions(item)
                    } message: { item in
                        message(item)
                    }
                } else {
                    content.confirmationDialog(title, isPresented: isPresented, titleVisibility: titleVisibility,
                                               presenting: data) { item in
                        actions(item)
                    }
                }
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: UIContentSizeCategory.didChangeNotification)) { _ in
            let size = WindowTextSize.system
            if size != systemSize { systemSize = size }
        }
    }

    @ViewBuilder private func alertActions(_ item: Item) -> some View {
        actions(item)
        if !hasCancel { Button("Cancel", role: .cancel) {} }
    }
}

extension View {
    /// Keeps a system confirmation's labels inside its buttons when the window cap reaches action-sheet capsules but
    /// not their labels (#8127). At ordinary sizes this is SwiftUI's confirmationDialog unchanged; above the interface
    /// cap it is an alert. Set hasCancel when actions already owns Cancel; otherwise the alert adds it exactly once.
    private func accessibleConfirmationDialog<Actions: View, Message: View>(
        title: Text, isPresented: Binding<Bool>, titleVisibility: Visibility, hasCancel: Bool, showsMessage: Bool,
        @ViewBuilder actions: @escaping () -> Actions, @ViewBuilder message: @escaping () -> Message
    ) -> some View {
        modifier(AccessibleConfirmationDialog(title: title, isPresented: isPresented, titleVisibility: titleVisibility,
                                              hasCancel: hasCancel, showsMessage: showsMessage,
                                              actions: actions, message: message))
    }

    private func accessibleItemConfirmationDialog<Actions: View, Message: View, Item>(
        title: Text, isPresented: Binding<Bool>, titleVisibility: Visibility, hasCancel: Bool, showsMessage: Bool, data: Item?,
        @ViewBuilder actions: @escaping (Item) -> Actions, @ViewBuilder message: @escaping (Item) -> Message
    ) -> some View {
        modifier(AccessibleItemConfirmationDialog(title: title, isPresented: isPresented, titleVisibility: titleVisibility,
                                                  hasCancel: hasCancel, showsMessage: showsMessage, data: data, actions: actions, message: message))
    }

    func accessibleConfirmationDialog<Actions: View>(
        _ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, @ViewBuilder actions: @escaping () -> Actions
    ) -> some View {
        accessibleConfirmationDialog(title: Text(titleKey), isPresented: isPresented, titleVisibility: titleVisibility,
                                     hasCancel: hasCancel, showsMessage: false, actions: actions, message: { EmptyView() })
    }

    func accessibleConfirmationDialog<S: StringProtocol, Actions: View>(
        _ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, @ViewBuilder actions: @escaping () -> Actions
    ) -> some View {
        accessibleConfirmationDialog(title: Text(title), isPresented: isPresented, titleVisibility: titleVisibility,
                                     hasCancel: hasCancel, showsMessage: false, actions: actions, message: { EmptyView() })
    }

    func accessibleConfirmationDialog<Actions: View>(
        _ title: Text, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, @ViewBuilder actions: @escaping () -> Actions
    ) -> some View {
        accessibleConfirmationDialog(title: title, isPresented: isPresented, titleVisibility: titleVisibility,
                                     hasCancel: hasCancel, showsMessage: false, actions: actions, message: { EmptyView() })
    }

    func accessibleConfirmationDialog<Actions: View, Message: View>(
        _ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, @ViewBuilder actions: @escaping () -> Actions,
        @ViewBuilder message: @escaping () -> Message
    ) -> some View {
        accessibleConfirmationDialog(title: Text(titleKey), isPresented: isPresented, titleVisibility: titleVisibility,
                                     hasCancel: hasCancel, showsMessage: true, actions: actions, message: message)
    }

    func accessibleConfirmationDialog<S: StringProtocol, Actions: View, Message: View>(
        _ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, @ViewBuilder actions: @escaping () -> Actions,
        @ViewBuilder message: @escaping () -> Message
    ) -> some View {
        accessibleConfirmationDialog(title: Text(title), isPresented: isPresented, titleVisibility: titleVisibility,
                                     hasCancel: hasCancel, showsMessage: true, actions: actions, message: message)
    }

    func accessibleConfirmationDialog<Actions: View, Message: View>(
        _ title: Text, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, @ViewBuilder actions: @escaping () -> Actions,
        @ViewBuilder message: @escaping () -> Message
    ) -> some View {
        accessibleConfirmationDialog(title: title, isPresented: isPresented, titleVisibility: titleVisibility,
                                     hasCancel: hasCancel, showsMessage: true, actions: actions, message: message)
    }

    func accessibleConfirmationDialog<Actions: View, Item>(
        _ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, presenting data: Item?, @ViewBuilder actions: @escaping (Item) -> Actions
    ) -> some View {
        accessibleItemConfirmationDialog(title: Text(titleKey), isPresented: isPresented, titleVisibility: titleVisibility,
                                         hasCancel: hasCancel, showsMessage: false, data: data, actions: actions,
                                         message: { _ in EmptyView() })
    }

    func accessibleConfirmationDialog<S: StringProtocol, Actions: View, Item>(
        _ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, presenting data: Item?, @ViewBuilder actions: @escaping (Item) -> Actions
    ) -> some View {
        accessibleItemConfirmationDialog(title: Text(title), isPresented: isPresented, titleVisibility: titleVisibility,
                                         hasCancel: hasCancel, showsMessage: false, data: data, actions: actions,
                                         message: { _ in EmptyView() })
    }

    func accessibleConfirmationDialog<Actions: View, Item>(
        _ title: Text, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, presenting data: Item?, @ViewBuilder actions: @escaping (Item) -> Actions
    ) -> some View {
        accessibleItemConfirmationDialog(title: title, isPresented: isPresented, titleVisibility: titleVisibility,
                                         hasCancel: hasCancel, showsMessage: false, data: data, actions: actions,
                                         message: { _ in EmptyView() })
    }

    func accessibleConfirmationDialog<Actions: View, Message: View, Item>(
        _ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, presenting data: Item?, @ViewBuilder actions: @escaping (Item) -> Actions,
        @ViewBuilder message: @escaping (Item) -> Message
    ) -> some View {
        accessibleItemConfirmationDialog(title: Text(titleKey), isPresented: isPresented, titleVisibility: titleVisibility,
                                         hasCancel: hasCancel, showsMessage: true, data: data, actions: actions, message: message)
    }

    func accessibleConfirmationDialog<S: StringProtocol, Actions: View, Message: View, Item>(
        _ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, presenting data: Item?, @ViewBuilder actions: @escaping (Item) -> Actions,
        @ViewBuilder message: @escaping (Item) -> Message
    ) -> some View {
        accessibleItemConfirmationDialog(title: Text(title), isPresented: isPresented, titleVisibility: titleVisibility,
                                         hasCancel: hasCancel, showsMessage: true, data: data, actions: actions, message: message)
    }

    func accessibleConfirmationDialog<Actions: View, Message: View, Item>(
        _ title: Text, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
        hasCancel: Bool = false, presenting data: Item?, @ViewBuilder actions: @escaping (Item) -> Actions,
        @ViewBuilder message: @escaping (Item) -> Message
    ) -> some View {
        accessibleItemConfirmationDialog(title: title, isPresented: isPresented, titleVisibility: titleVisibility,
                                         hasCancel: hasCancel, showsMessage: true, data: data, actions: actions, message: message)
    }
}

extension View {
    /// Exposes the real UIKit label frames to DEBUG UI tests; SwiftUI's dialog accessibility tree omits them (#8127).
    @ViewBuilder func confirmationDialogLabelProbe() -> some View {
        #if DEBUG
        if ScreenProbe.enabled {
            overlay(alignment: .topLeading) {
                ConfirmationDialogLabelProbe()
                    .frame(width: Theme.Size.line, height: Theme.Size.line)
                    .allowsHitTesting(false)
            }
        } else {
            self
        }
        #else
        self
        #endif
    }
}

#if DEBUG
private struct ConfirmationDialogLabelProbe: UIViewRepresentable {
    func makeUIView(context: Context) -> ConfirmationDialogLabelProbeView { ConfirmationDialogLabelProbeView() }
    func updateUIView(_ view: ConfirmationDialogLabelProbeView, context: Context) {}
}

@MainActor private final class ConfirmationDialogLabelProbeView: UIView {
    private struct Label: Encodable {
        let text: String
        let x, y, width, height: Double
    }

    init() {
        super.init(frame: .zero)
        isUserInteractionEnabled = false
        isAccessibilityElement = true
        accessibilityIdentifier = "probe.confirmation-dialog-labels"
    }

    required init?(coder: NSCoder) { nil }

    override var accessibilityValue: String? {
        get {
            var controller = window?.rootViewController
            while let presented = controller?.presentedViewController { controller = presented }
            guard let alert = controller as? UIAlertController else { return "[]" }
            let values = labels(in: alert.view).map { label in
                let frame = label.convert(label.bounds, to: nil)
                return Label(text: label.text ?? "", x: Double(frame.minX), y: Double(frame.minY),
                             width: Double(frame.width), height: Double(frame.height))
            }
            return (try? JSONEncoder().encode(values)).flatMap { String(data: $0, encoding: .utf8) } ?? "[]"
        }
        set {}
    }

    private func labels(in view: UIView) -> [UILabel] {
        var found = (view as? UILabel).flatMap { label in
            label.isHidden || label.alpha == 0 || (label.text ?? "").isEmpty ? nil : [label]
        } ?? []
        for child in view.subviews { found.append(contentsOf: labels(in: child)) }
        return found
    }
}
#endif
