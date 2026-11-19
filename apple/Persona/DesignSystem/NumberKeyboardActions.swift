import SwiftUI

#if os(iOS)
/// A numeric form's actions above the keyboard. The page owns focus so only one toolbar is contributed, even when
/// its form has several numeric fields. Save uses the page's existing action; Done only ends editing.
struct NumberKeyboardActions: ToolbarContent {
    var focused: Bool
    var canSave: Bool
    var saving: Bool
    var configurationBusy: Bool
    var save: () -> Void
    var done: () -> Void

    var body: some ToolbarContent {
        if focused {
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                Button(action: save) {
                    Group {
                        if saving { ProgressView() } else { Text("Save").lineLimit(1) }
                    }
                    .font(Theme.Font.bodyStrong)
                    .tapTargetHeight()
                }
                .disabled(!canSave || configurationBusy)
                .accessibilityIdentifier("plugin-keyboard-save")
                Button(action: done) {
                    Text("Done").font(Theme.Font.body).lineLimit(1).tapTargetHeight()
                }
                .accessibilityIdentifier("plugin-keyboard-done")
            }
        }
    }
}
#endif
