import SwiftUI

/// Renaming a recording (#8873, docs/apple/recording.md「Titles」): an alert with its title in a field. The owner's title
/// comes before every name the hub gives, so it is never renamed automatically again; left empty, the name goes back to
/// the calendar's or the one written from what was said, which the field shows as its placeholder.
private struct RecordingRenameAlert: ViewModifier {
    /// The recording being renamed; nil: no alert.
    @Binding var renaming: UUID?
    @State private var text = ""
    /// What the field started with: saved unchanged, a name the hub gave stays the hub's (it keeps following it).
    @State private var initial = ""

    func body(content: Content) -> some View {
        content
            .alert("Rename", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
                TextField(placeholder, text: $text)
                    .accessibilityIdentifier("recording-rename-field")
                Button("Cancel", role: .cancel) { renaming = nil }
                Button("Save") { save() }
                    .accessibilityIdentifier("recording-rename-save")
            } message: {
                Text("Leave it empty to name it automatically.")
            }
            .onChange(of: renaming, initial: true) { _, id in
                guard let entry = id.flatMap({ RecordingLibrary.shared.entry($0) }) else { return }
                initial = RecordingTitle.source(entry) == .time ? "" : entry.displayTitle
                text = initial
            }
    }

    /// What it is called when the field is left empty.
    private var placeholder: String {
        guard let entry = renaming.flatMap({ RecordingLibrary.shared.entry($0) }) else { return "" }
        return entry.hubName ?? RecordingTitle.untitled(entry)
    }

    private func save() {
        defer { renaming = nil }
        guard let id = renaming, let entry = RecordingLibrary.shared.entry(id) else { return }
        if text == initial, RecordingTitle.source(entry) == .hub { return }
        RecordingLibrary.shared.rename(id, to: text)
    }
}

extension View {
    /// The Rename alert for the recording `renaming` names (#8873).
    func recordingRename(_ renaming: Binding<UUID?>) -> some View { modifier(RecordingRenameAlert(renaming: renaming)) }
}
