#if os(iOS)
import SwiftUI

/// Editing a text file (#5829): the editor under Cancel and Save. Cancel with changes asks first; Save writes only over
/// the version opened, and when the file changed on the computer meanwhile the user chooses: Save as Copy, Discard Mine
/// and Open the New Version, Overwrite Anyway (asked again), or Keep Editing.
struct FileEditSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let edit: FileEditModel
    /// The file on the computer changed (saved, a copy saved, or the newer version wanted): the viewer reads it again.
    var reload: () -> Void
    @State private var editor = CodeEditorHandle()
    @State private var discarding = false
    @State private var changed: Date??
    @State private var overwriting = false

    var body: some View {
        NavigationStack {
            CodeEditorView(initial: edit.original.body, language: edit.language, handle: editor) {
                if !edit.dirty { edit.dirty = true }
            }
            .ignoresSafeArea(.container, edges: .bottom)
            .navigationTitle(edit.name)
            .navigationSubtitleWhenSupported(edit.dirty ? String(localized: "Edited") : model.machine(edit.ref.machineRef)?.name ?? edit.ref.machineID)
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                SheetClose(id: "editor.cancel") { edit.dirty ? discarding = true : dismiss() }
                SheetConfirm(id: "editor.save", disabled: !edit.dirty, busy: edit.saving) { Task { await save(force: false) } }
            }
        }
        .interactiveDismissDisabled(edit.dirty)
        .accessibleConfirmationDialog("Discard your changes?", isPresented: $discarding, titleVisibility: .visible, hasCancel: true) {
            Button("Discard Changes", role: .destructive) { dismiss() }
            Button("Keep Editing", role: .cancel) {}
        } message: {
            Text("They haven’t been saved to the computer.")
        }
        .alert(String(localized: "“\(edit.name)” changed on the computer"),
               isPresented: Binding(get: { changed != nil }, set: { if !$0 { changed = nil } })) {
            Button("Save as Copy") { Task { await saveCopy() } }
            Button("Discard Mine and Open the New Version") {
                dismiss()
                reload()
            }
            Button("Overwrite Anyway", role: .destructive) { overwriting = true }
            Button("Keep Editing", role: .cancel) {}
        } message: {
            if let at = changed ?? nil {
                Text("It changed after you opened it (at \(at.formatted(date: .omitted, time: .shortened))), maybe by an Agent or another app. Saving now would overwrite those changes.")
            } else {
                Text("It changed after you opened it, maybe by an Agent or another app. Saving now would overwrite those changes.")
            }
        }
        .alert("Overwrite the newer version?", isPresented: $overwriting) {
            Button("Overwrite", role: .destructive) { Task { await save(force: true) } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("What was changed on the computer since you opened the file will be lost.")
        }
    }

    private func save(force: Bool) async {
        switch await edit.save(editor.text, force: force) {
        case .saved:
            Haptics.success()
            model.toast = Toast(String(localized: "Saved"), style: .success)
            dismiss()
            reload()
        case .changed(let at):
            Haptics.error()
            editor.dismissKeyboard()
            changed = .some(at)
        case .failed(let message):
            Haptics.error()
            model.toast = Toast(String(localized: "Couldn’t save: \(message)"), style: .error)
        }
    }

    private func saveCopy() async {
        switch await edit.saveCopy(editor.text) {
        case .success(let name):
            Haptics.success()
            model.toast = Toast(String(localized: "Saved as “\(name)”"), style: .success)
            dismiss()
            reload()
        case .failure(let error):
            if !error.isCancellation { model.toast = Toast(String(localized: "Couldn’t save: \(FolderEdits.message(error))"), style: .error) }
        }
    }
}
#endif
