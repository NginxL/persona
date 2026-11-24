import SwiftUI

import UIKit

/// What the user typed, as the field shows it, for a send (#2203). With the WeChat keyboard (a third-party keyboard)
/// a message sent from the main conversation lost its end: the field showed all of it, the message had about half.
/// A keyboard keeps the words it is still composing as marked text (often Chinese characters already, underlined,
/// looking typed) and the SwiftUI field's binding is not guaranteed to have them, or every edit a keyboard extension
/// makes. So before sending, the composition is confirmed (`unmarkText`, as a tap elsewhere would) and the binding
/// takes what the text view holds. Afterwards nothing is left marked for the keyboard to put back into the emptied box.
enum TypedText {
    /// Confirms the focused text view's composition and puts its text into `text` when the two differ.
    @MainActor static func commitFocused(into text: Binding<String>) {
        commit(FirstResponder.current as? UIResponder & UITextInput, into: text)
    }

    /// `commitFocused` for a given view (tests).
    @MainActor static func commit(_ view: (UIResponder & UITextInput)?, into text: Binding<String>) {
        guard let view else { return }
        if view.markedTextRange != nil { view.unmarkText() }
        guard let all = view.textRange(from: view.beginningOfDocument, to: view.endOfDocument),
              let shown = view.text(in: all), shown != text.wrappedValue else { return }
        text.wrappedValue = shown
    }

    /// The focused text view still holds marked text (Pinyin's letters before a candidate is picked): what reads the
    /// draft as typed waits, and nothing writes it, which would confirm the composition early (the @ list, #2319).
    @MainActor static var composingFocused: Bool { composing(FirstResponder.current as? UITextInput) }

    /// `composingFocused` for a given view (tests).
    @MainActor static func composing(_ view: UITextInput?) -> Bool { view?.markedTextRange != nil }
}
