import SwiftUI

// A presented page's bar buttons (#9653, docs/design/DESIGN_SYSTEM.md sections 3.5 and 3.6): every sheet, full-screen
// cover and self-drawn layer over a page leaves at its top leading corner, an ✕ (`SheetClose`); a pushed page keeps its
// ‹ there. The trailing corner holds only the page's own actions: its ⋯ menu and its confirming action (`SheetConfirm`'s
// ✓, or a verb that says more). Pages never write their own close; `SheetCloseTests` counts those.
//
// The way out of a sheet on a Mac (#1248). A Mac sheet has no navigation bar: only its bottom row of actions is drawn,
// so a Close at the leading edge (`.topBarLeading`, `.navigation` there) never shows (#743). It cannot be swiped down
// and a click outside it only beeps, so that row's Cancel / Done (Esc) is the only way out, and Esc is easy to hit.

/// A presented page's ✕ at the top leading corner (#9653): a close-role button with the style's ✕ in the primary ink.
/// The system's own ✕ (`Button(role: .close)` without a label) takes the app's tint, and the accent on the bar's glass
/// read 2–3:1 in dark, as the Cancel it replaces did (#9634). VoiceOver reads Close; Esc closes it from a keyboard.
struct SheetClose: ToolbarContent {
    private let id: String
    private let disabled: Bool
    private let action: () -> Void

    /// `id`: the accessibility identifier UI tests find it by. `disabled`: while work that cannot be left runs.
    init(id: String = "sheet-close", disabled: Bool = false, action: @escaping () -> Void) {
        self.id = id
        self.disabled = disabled
        self.action = action
    }

    var body: some ToolbarContent {
        ToolbarItem(placement: .cancellationAction) {
            closeButton
                .tint(Color.primary)
                .disabled(disabled)
                .keyboardShortcut(.cancelAction)
                .accessibilityLabel(Text("Close"))
                .accessibilityIdentifier(id)
        }
    }

    @ViewBuilder private var closeButton: some View {
        // The ink on the icon itself: a bar ignores a foreground style set on the button (docs/lessons/ios.md).
        if #available(iOS 26.0, *) {
            Button(role: .close, action: action) { ThemedIcon(Theme.Icon.close).sheetBarLabel() }
        } else {
            // iOS 18 has no close role (#6730): the same ✕ on a plain button.
            Button(action: action) { ThemedIcon(Theme.Icon.close).sheetBarLabel() }
        }
    }
}

/// An edit sheet's ✓ at the top trailing corner (#9653), as iOS 26's Calendar and Reminders draw it: the system's
/// confirm button, a white tick on the accent (at least 3.5:1 by `PersonaPalette`), for Save, a Done that saves and Add.
/// A verb that says more (Send, Connect, Install) stays a word at `.confirmationAction`.
struct SheetConfirm: ToolbarContent {
    private let id: String
    private let name: String?
    private let disabled: Bool
    private let busy: Bool
    private let action: () -> Void

    /// `name`: what VoiceOver reads, the word the ✓ stands for (Save, Add); the system's Done without it. `busy`: a
    /// spinner in its place while the save runs.
    init(id: String = "sheet-confirm", name: String? = nil, disabled: Bool = false, busy: Bool = false,
         action: @escaping () -> Void) {
        self.id = id
        self.name = name
        self.disabled = disabled
        self.busy = busy
        self.action = action
    }

    var body: some ToolbarContent {
        ToolbarItem(placement: .confirmationAction) {
            if busy {
                ProgressView().accessibilityIdentifier(id)
            } else {
                confirmButton
                    .disabled(disabled)
                    .accessibilityIdentifier(id)
            }
        }
    }

    @ViewBuilder private var confirmButton: some View {
        if #available(iOS 26.0, *) {
            if let name {
                Button(role: .confirm, action: action).accessibilityLabel(Text(name))
            } else {
                Button(role: .confirm, action: action)
            }
        } else {
            // iOS 18 has no confirm role (#6730): the word, as iOS 18's own sheets say it. A ✓ there read like the
            // selection ticks of the form under it.
            Button(action: action) {
                if let name { Text(name).lineLimit(1) } else { Text("Done").lineLimit(1) }
            }
        }
    }
}

extension View {
    /// An edit sheet's ✕ (`SheetClose`; Esc on a Mac): leaves without saving. On a Mac, with unsaved edits it asks
    /// before throwing them away; iOS leaves at once, as it always has.
    /// `when`: only in a sheet, for a page that is also pushed.
    func sheetCancel(unsaved: Bool, when active: Bool = true, id: String = "sheet-close",
                     cancel: @escaping () -> Void) -> some View {
        modifier(SheetCancel(unsaved: unsaved, active: active, id: id, cancel: cancel))
    }

    /// A word on a sheet's bar that only closes, in the primary ink, not the accent (#9460): the bar's glass turns
    /// lighter in a dark sheet, the more so over a light page, and the accent on it read 2.1:1 (Send To over Export
    /// Diagnostics) and 3.1:1 (Forward To over a chat); iOS draws a bar's plain items in the label color. A bar's way
    /// out is `SheetClose`'s ✕ now (#9653); a confirming button keeps the accent.
    func sheetBarLabel() -> some View {
        foregroundStyle(Color.primary)
    }

    /// The way out of a page that only shows things (#9653): `SheetClose`'s ✕ at the top leading corner on the iPhone
    /// and iPad, Done in a Mac sheet's bottom row. Nothing when `detailPresentation` pushed the page into a column
    /// (`detailPushed`): there its ‹ goes back.
    func sheetDone(_ done: @escaping () -> Void) -> some View {
        modifier(SheetDone(done: done))
    }

    /// Done for a sheet that only shows things, in a Mac sheet's bottom row (Esc too). Nothing on iOS: there the page
    /// keeps its own Close or the swipe down.
    func macSheetDone(_ done: @escaping () -> Void) -> some View {
        #if os(macOS)
        toolbar { ToolbarItem(placement: .cancellationAction) { Button(action: done) { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) } } }
        #else
        self
        #endif
    }
}

private struct SheetDone: ViewModifier {
    var done: () -> Void
    @Environment(\.detailPushed) private var pushed

    func body(content: Content) -> some View {
        #if os(macOS)
        content.macSheetDone(done)
        #else
        if pushed { content } else { content.pageToolbar { SheetClose(action: done) } }
        #endif
    }
}

private struct SheetCancel: ViewModifier {
    var unsaved: Bool
    var active: Bool
    var id: String
    var cancel: () -> Void
    @State private var asking = false

    func body(content: Content) -> some View {
        if active { guarded(content) } else { content }
    }

    private func guarded(_ content: Content) -> some View {
        content
            .pageToolbar {
                SheetClose(id: id) {
                    #if os(macOS)
                    if unsaved { asking = true; return }
                    #endif
                    cancel()
                }
            }
            // On the content, not on the bar's button: the bar is hosted apart from the page.
            .confirmationDialog("Discard Unsaved Changes?", isPresented: $asking, titleVisibility: .visible) {
                Button("Discard Changes", role: .destructive, action: cancel)
                Button("Keep Editing", role: .cancel) {}
            } message: {
                Text("If you close it, the changes you made won't be saved.")
            }
            #if DEBUG && os(macOS)
            // `-showDiscardConfirm YES`: the question up as the sheet opens, for screenshots.
            .task {
                guard UserDefaults.standard.bool(forKey: "showDiscardConfirm") else { return }
                try? await Task.sleep(for: .seconds(1))
                asking = true
            }
            #endif
    }
}
