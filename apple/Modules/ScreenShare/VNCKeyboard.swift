import SwiftUI
import UIKit

/// The keyboard for the Mac (#1343): the system's, typing straight to it (VNCKeyInputView), with a row of the Mac's
/// keys over it (VNCShortcutBar, on the page: it rides above the keyboard): ⌘ ⌥ ⌃ ⇧ held until tapped again, Esc,
/// Tab, the arrows, ⌘C ⌘V ⌘Z ⌘Tab ⌘Space.
struct VNCKeyCatcher: UIViewRepresentable {
    let session: RFBSession
    let modifiers: VNCModifiers
    @Binding var typing: Bool

    func makeUIView(context: Context) -> VNCKeyInputView {
        let v = VNCKeyInputView()
        v.session = session
        v.modifiers = modifiers
        v.onEnd = { typing = false }
        return v
    }

    func updateUIView(_ v: VNCKeyInputView, context: Context) {
        if typing, !v.isFirstResponder { DispatchQueue.main.async { v.becomeFirstResponder() } }
        if !typing, v.isFirstResponder { DispatchQueue.main.async { v.resignFirstResponder() } }
    }
}

/// The modifiers held down from the shortcut row, for the keys typed while they are.
@MainActor @Observable final class VNCModifiers {
    var held: Set<UInt32> = []
    var keys: [UInt32] { [RFBSession.command, RFBSession.option, RFBSession.control, RFBSession.shift].filter(held.contains) }
}

final class VNCKeyInputView: UIView, UIKeyInput {
    weak var session: RFBSession?
    var modifiers = VNCModifiers()
    var onEnd: () -> Void = {}

    override var canBecomeFirstResponder: Bool { true }
    var hasText: Bool { true }
    var autocorrectionType: UITextAutocorrectionType { get { .no } set {} }
    var autocapitalizationType: UITextAutocapitalizationType { get { .none } set {} }
    var smartQuotesType: UITextSmartQuotesType { get { .no } set {} }
    var smartDashesType: UITextSmartDashesType { get { .no } set {} }
    var spellCheckingType: UITextSpellCheckingType { get { .no } set {} }

    func insertText(_ text: String) {
        guard let s = session else { return }
        let held = modifiers.keys
        if held.isEmpty {
            s.type(text)
        } else {
            for ch in text.unicodeScalars { s.press(RFBSession.keysym(ch), with: held) }
        }
    }

    func deleteBackward() { session?.press(VNCKeys.backspace, with: modifiers.keys) }

    override func resignFirstResponder() -> Bool {
        let r = super.resignFirstResponder()
        onEnd()
        return r
    }

    override init(frame: CGRect) {
        super.init(frame: frame)
        isAccessibilityElement = true
        accessibilityIdentifier = "vnc-key-input"
    }

    required init?(coder: NSCoder) { fatalError() }
}

/// The row over the keyboard: the Mac's modifiers (held while lit) and the keys the phone's keyboard has not.
struct VNCShortcutBar: View {
    let session: RFBSession
    let modifiers: VNCModifiers

    private struct Key: Identifiable {
        let id: String
        let label: String
        var keysym: UInt32
        var with: [UInt32] = []
    }

    private static let mods: [(String, String, UInt32)] = [("cmd", "⌘", RFBSession.command), ("opt", "⌥", RFBSession.option),
                                                           ("ctrl", "⌃", RFBSession.control), ("shift", "⇧", RFBSession.shift)]
    private static let keys: [Key] = [
        Key(id: "esc", label: "Esc", keysym: VNCKeys.escape), Key(id: "tab", label: "Tab", keysym: VNCKeys.tab),
        Key(id: "left", label: "←", keysym: VNCKeys.left), Key(id: "up", label: "↑", keysym: VNCKeys.up),
        Key(id: "down", label: "↓", keysym: VNCKeys.down), Key(id: "right", label: "→", keysym: VNCKeys.right),
        Key(id: "copy", label: "⌘C", keysym: 0x63, with: [RFBSession.command]), Key(id: "paste", label: "⌘V", keysym: 0x76, with: [RFBSession.command]),
        Key(id: "undo", label: "⌘Z", keysym: 0x7a, with: [RFBSession.command]), Key(id: "switch", label: "⌘Tab", keysym: VNCKeys.tab, with: [RFBSession.command]),
        Key(id: "spotlight", label: "⌘Space", keysym: 0x20, with: [RFBSession.command]),
    ]

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: Theme.Space.xs) {
                ForEach(Self.mods, id: \.0) { id, label, keysym in
                    let on = modifiers.held.contains(keysym)
                    Button {
                        if on { modifiers.held.remove(keysym) } else { modifiers.held.insert(keysym) }
                    } label: {
                        Text(label).font(Theme.Font.labelStrong).lineLimit(1)
                    }
                    .themedButton(prominent: on)
                    .accessibilityIdentifier("vnc-key-\(id)")
                    .accessibilityValue(on ? String(localized: "Held") : "")
                }
                ForEach(Self.keys) { k in
                    Button { session.press(k.keysym, with: Array(Set(k.with + modifiers.keys))) } label: {
                        Text(k.label).font(Theme.Font.labelStrong.monospaced()).lineLimit(1)
                    }
                    .themedButton()
                    .accessibilityIdentifier("vnc-key-\(k.id)")
                }
            }
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.xs)
        }
        .scrollIndicators(.hidden)
        .background(.bar)
    }
}
