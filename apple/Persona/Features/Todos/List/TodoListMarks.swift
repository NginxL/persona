#if os(iOS)
import SwiftUI

/// The mark before a row's second line (#10465; docs/design/proposals/todos-redesign.md 3.4): ● needs you breathes
/// exactly as on Agents rows (`StatusDot`, DESIGN_SYSTEM 2.7), ◐ working turns its amber arc (`WorkingArc`), ✓ just
/// finished in the unread green, ◷ waiting an idle clock. Both loops pause off screen and hold still with Reduce Motion.
struct TodoMarkView: View {
    var mark: TodoMark

    var body: some View {
        // A letter's room in the text beside it, the mark over its middle: the row lines up on that letter's baseline.
        // A drawn mark has none of its own, and a UIKit one (the arc) reports its top as its baseline.
        Text(verbatim: "M").hidden()
            .overlay { drawn }
            .accessibilityHidden(true) // the row's label says the state in words
    }

    @ViewBuilder private var drawn: some View {
        switch mark {
        case .needs:
            StatusDot(state: .needsYou, size: Theme.Size.dot)
        case .working:
            WorkingArc()
        case .ready:
            ThemedIcon(Theme.Icon.done).font(Theme.Font.labelHeavy).foregroundStyle(StatusPalette.unread.dot)
        case .waiting:
            ThemedIcon(Theme.Icon.time).font(Theme.Font.label).foregroundStyle(StatusPalette.idle.dot)
        }
    }
}
#endif
