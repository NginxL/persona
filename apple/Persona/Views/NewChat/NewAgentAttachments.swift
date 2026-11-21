import PhotosUI
import SwiftUI

// New Agent's attachments (#2321): photos and files go with the first message, as on the web (`deliverLaunch`,
// web/src/lib/new-agent.ts). Photos are the composer's (`MessagePhotos`); files are uploaded to the chosen computer
// when picked (`FileUploads`) and their paths follow what was typed.

/// The first message New Agent sends once the Agent is starting.
nonisolated enum FirstMessage {
    /// A to-do's context block, then what was typed, then the uploaded files' paths one per line, as the web joins
    /// them; with nothing typed and only photos, the composer's words for them. "": nothing to send.
    static func text(prompt: String, contextBlock: String?, paths: [String], photos: Int) -> String {
        let typed = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        let lead = contextBlock.map { typed.isEmpty ? $0 : "\($0)\n\n\(typed)" } ?? typed
        let text = ([lead] + paths.map(PathInsert.quoted)).filter { !$0.isEmpty }.joined(separator: "\n")
        return text.isEmpty && photos > 0 ? String(localized: "Please look at the image [Assistant View 5ad878]") : text
    }

    /// What the attachments hold Start Chat back for, said under it in a line; nil: nothing. A Codex or omp launcher
    /// takes none: it starts with its first message as text (`codexRefusal` says what to do). Paths go only once the
    /// files are on the computer.
    static func hold(codex: Bool, omp: Bool = false, attached: Bool, uploading: Bool) -> String? {
        if codex && attached { return String(localized: "Codex takes no attachments") }
        if omp && attached { return String(localized: "omp takes no attachments") }
        return uploading ? String(localized: "Waiting for the files to finish uploading") : nil
    }

    /// Under the attachments a Codex or omp launcher can't take: the web's words when it refuses such a start.
    static var codexRefusal: String {
        String(localized: "This launcher can't take attachments in the first message. Pick another launcher, or start with text and send the attachments in the session.")
    }
}

/// What New Agent sends besides the text. A file is on the computer it was uploaded to: when another one is picked, the
/// uploaded files come out with a note saying so, and uploads still going stop. Nothing is uploaded again; photos stay.
@MainActor @Observable
final class NewAgentAttachments {
    struct File: Identifiable {
        let id = UUID()
        var name: String
        var path: String
        var size: Int64
    }

    var images: [(UIImage, ImagePayload)] = []
    private(set) var files: [File] = []
    let uploads = FileUploads()
    /// Why files went: they are on the computer picked before.
    private(set) var note: String?
    /// The computer the files are uploaded to (`MachineRef.path`); "" before the first.
    @ObservationIgnored private var computer = ""

    var isEmpty: Bool { images.isEmpty && files.isEmpty && uploads.items.isEmpty }
    var uploading: Bool { !uploads.items.isEmpty }
    var paths: [String] { files.map(\.path) }

    func add(_ img: UIImage, model: AppModel) {
        note = nil
        MessagePhotos.add(img, to: &images, model: model)
    }

    func load(_ items: [PhotosPickerItem], model: AppModel) async {
        await MessagePhotos.load(items, model: model) { add($0, model: model) }
    }

    /// Uploads urls to the computer one after another; each joins `files` once it is there.
    func upload(_ urls: [URL], to ref: MachineRef, model: AppModel) {
        note = nil
        computer = ref.path
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            let size = Int64((try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
            if scoped { url.stopAccessingSecurityScopedResource() }
            let name = url.lastPathComponent
            uploads.start([url], machine: ref, model: model) { [weak self] path in
                self?.uploaded(File(name: name, path: path, size: size), on: ref.path)
            }
        }
    }

    /// A file now on `on`: kept unless another computer was picked meanwhile.
    func uploaded(_ file: File, on: String) {
        guard on == computer else { return }
        files.append(file)
    }

    func remove(_ file: File) { files.removeAll { $0.id == file.id } }

    /// Another computer was picked; `name` gives a computer's name for the note.
    func computerChanged(to ref: String, name: (String) -> String?) {
        guard !computer.isEmpty, ref != computer else { return }
        if !files.isEmpty || uploading {
            note = String(localized: "The files uploaded to “\(name(computer) ?? computer)” were removed. Add them again for this computer.")
        }
        cancelUploads()
        files = []
        computer = ref
    }

    /// Uploads still on their way stop (the sheet was cancelled).
    func cancelUploads() {
        for item in uploads.items { uploads.cancel(item.id) }
    }
}

/// The attachments' rows under First Message: the photos (× takes one out), the files on their way and those on the
/// computer, and the menu that adds them with the composer's sources. `adds` false (a Codex launcher): no menu, only
/// what is left to take out.
struct NewAgentAttachmentRows: View {
    @Bindable var attachments: NewAgentAttachments
    var adds: Bool
    var photos: () -> Void
    /// Take Photo; nil where there is no camera.
    var camera: (() -> Void)?
    var files: () -> Void

    var body: some View {
        if !attachments.images.isEmpty {
            ComposerImages(images: $attachments.images)
        }
        ForEach(attachments.uploads.items) { item in
            UploadRow(item: item, uploads: attachments.uploads, boxed: false)
        }
        ForEach(attachments.files) { file in
            AttachmentRow(symbol: Theme.Icon.file, name: file.name, status: file.size > 0 ? file.size.storageText : "", progress: nil,
                          removeLabel: String(localized: "Remove \(file.name)"), boxed: false) { attachments.remove(file) }
        }
        if adds {
            Menu {
                Button(action: photos) { Label("Photos", systemImage: Theme.Icon.photos) }
                if let camera {
                    Button(action: camera) { Label("Take Photo [Chat View 6e3a10]", systemImage: Theme.Icon.camera) }
                }
                Button(action: files) { Label("Files", systemImage: Theme.Icon.file) }
            } label: {
                // The whole row opens it, as a form's button row does.
                Label("Add Images or Files", systemImage: Theme.Icon.add).lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(.rect)
            }
            .accessibilityIdentifier("newChat.attach")
        }
    }
}
