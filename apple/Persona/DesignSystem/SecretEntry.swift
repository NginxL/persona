import SwiftUI
import UIKit

extension View {
    /// A hidden field for a credential the app hands to something else (a token, an API key, a plugin's secret), not
    /// the user's password for this app: typed as is (no capitalization, no autocorrect), and marked a one-time code
    /// so AutoFill does not fill a saved password into it. On iOS that alone does not keep the system from offering to
    /// save it ("Save Password?"): use `SecretField` there (#1512).
    func secretEntry() -> some View {
        textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .textContentType(.oneTimeCode)
    }
}

/// A hidden field for a token, an API key, a plugin's secret: never offered to the system's password manager.
///
/// iOS offers to save what a hidden field still holds when its page goes away (back, a sheet closing, another tab)
/// once a text field sits beside it — what a login form looks like — even when the field says it is a one-time code
/// (seen on iOS 27, #1512). So on iOS the field holds what is typed only while it has the keyboard: the moment it
/// gives the keyboard up or leaves the window it empties, the value staying in `text`, and while `text` is filled in
/// the field says so (`filled`). The secret is never shown again, as the hub never sends it back.
struct SecretField: View {
    var placeholder: String
    /// Said in the empty field when `text` is filled in and the field is not being typed in.
    var filled: String
    @Binding var text: String
    var identifier: String?
    var onSubmitAction: (() -> Void)?
    var onFocusAction: ((Bool) -> Void)?

    init(_ placeholder: String, filled: String = String(localized: "Filled in"), text: Binding<String>, identifier: String? = nil) {
        self.placeholder = placeholder
        self.filled = filled
        _text = text
        self.identifier = identifier
    }

    /// Runs `action` when Return is pressed in the field, passed through to the real input control on every platform.
    func onSubmit(_ action: @escaping () -> Void) -> SecretField {
        var copy = self
        copy.onSubmitAction = action
        return copy
    }

    /// Runs `action` with true when the field takes the keyboard (on a Mac, the focus) and false when it gives it up.
    func onFocusChange(_ action: @escaping (Bool) -> Void) -> SecretField {
        var copy = self
        copy.onFocusAction = action
        return copy
    }

    var body: some View {
        SecretTextField(placeholder: placeholder, filled: filled, identifier: identifier, text: $text,
                        onSubmit: onSubmitAction, onFocus: onFocusAction)
    }
}

private struct SecretTextField: UIViewRepresentable {
    var placeholder: String
    var filled: String
    var identifier: String?
    @Binding var text: String
    var onSubmit: (() -> Void)?
    var onFocus: ((Bool) -> Void)?

    func makeUIView(context: Context) -> EmptyingSecureTextField {
        let f = EmptyingSecureTextField()
        f.isSecureTextEntry = true
        f.textContentType = .oneTimeCode
        f.autocapitalizationType = .none
        f.autocorrectionType = .no
        f.spellCheckingType = .no
        f.font = Theme.Font.uiBody
        f.adjustsFontForContentSizeCategory = true
        f.setContentHuggingPriority(.defaultLow, for: .horizontal)
        f.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        let c = context.coordinator
        f.addTarget(c, action: #selector(Coordinator.changed(_:)), for: .editingChanged)
        f.addTarget(c, action: #selector(Coordinator.refresh(_:)), for: [.editingDidBegin, .editingDidEnd])
        f.addTarget(c, action: #selector(Coordinator.refresh(_:)), for: .editingDidEndOnExit) // return closes the keyboard
        f.addTarget(c, action: #selector(Coordinator.submit(_:)), for: .editingDidEndOnExit) // return runs onSubmit
        f.addTarget(c, action: #selector(Coordinator.began(_:)), for: .editingDidBegin)
        f.addTarget(c, action: #selector(Coordinator.ended(_:)), for: .editingDidEnd)
        // Show / hide while typing, to check what was just typed or pasted (a token cut short is a common mistake). The
        // field goes back to hidden, and empty, whenever it gives the keyboard up (EmptyingSecureTextField); what was
        // typed before is not put back, since pasting into a hidden field that still holds text can add to it.
        let reveal = UIButton(configuration: .plain())
        reveal.addTarget(c, action: #selector(Coordinator.toggle(_:)), for: .primaryActionTriggered)
        reveal.accessibilityIdentifier = identifier.map { $0 + "-reveal" }
        f.rightView = reveal
        f.rightViewMode = .whileEditing
        f.revealButton = reveal
        f.showRevealState()
        c.textField = f
        return f
    }

    func updateUIView(_ f: EmptyingSecureTextField, context: Context) {
        context.coordinator.field = self
        f.accessibilityIdentifier = identifier
        f.tintColor = UIColor(Theme.accent)
        // `.multilineTextAlignment` as on a SwiftUI field: a form row's value sits on the trailing side.
        f.textAlignment = switch context.environment.multilineTextAlignment {
        case .leading: .natural
        case .center: .center
        case .trailing: f.effectiveUserInterfaceLayoutDirection == .rightToLeft ? .left : .right
        }
        if !f.isFirstResponder && !(f.text ?? "").isEmpty { f.text = "" }
        context.coordinator.refresh(f)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: EmptyingSecureTextField, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? uiView.intrinsicContentSize.width, height: uiView.intrinsicContentSize.height)
    }

    func makeCoordinator() -> Coordinator { Coordinator(field: self) }

    final class Coordinator: NSObject {
        var field: SecretTextField
        weak var textField: EmptyingSecureTextField?
        init(field: SecretTextField) { self.field = field }

        @objc func changed(_ f: UITextField) { field.text = f.text ?? "" }

        @objc func submit(_ f: UITextField) { field.onSubmit?() }

        @objc func began(_: UITextField) { report(true) }

        @objc func ended(_: UITextField) { report(false) }

        /// Reported a moment later, in order: UIKit also ends editing as SwiftUI takes the field out of the window,
        /// inside a view update, where the owner's state must not change.
        private func report(_ focused: Bool) {
            Task { @MainActor [weak self] in self?.field.onFocus?(focused) }
        }

        @objc func refresh(_ f: UITextField) {
            f.placeholder = f.isFirstResponder || field.text.isEmpty ? field.placeholder : field.filled
        }

        @objc func toggle(_: UIButton) {
            guard let f = textField else { return }
            f.isSecureTextEntry.toggle()
            f.showRevealState()
        }
    }
}

/// Empties itself before it gives the keyboard up or leaves the window: AutoFill finds nothing to offer to save.
/// The text is set, not typed, so the binding keeps the value (`editingChanged` is only sent for typing).
private final class EmptyingSecureTextField: UITextField {
    weak var revealButton: UIButton?

    override func resignFirstResponder() -> Bool {
        hideAndEmpty()
        return super.resignFirstResponder()
    }

    override func willMove(toWindow newWindow: UIWindow?) {
        if newWindow == nil { hideAndEmpty() }
        super.willMove(toWindow: newWindow)
    }

    /// Hidden again and empty: a shown value never stays in the field once it is not being typed in.
    private func hideAndEmpty() {
        if !isSecureTextEntry {
            isSecureTextEntry = true
            showRevealState()
        }
        text = ""
    }

    /// The reveal button says what tapping it does: Show contents while hidden, Hide contents while shown.
    func showRevealState() {
        let hidden = isSecureTextEntry
        revealButton?.setImage(UIImage(systemName: hidden ? Theme.Icon.view : Theme.Icon.hide), for: .normal)
        revealButton?.accessibilityLabel = hidden ? String(localized: "Show contents") : String(localized: "Hide contents")
    }
}
