import SwiftUI
import UniformTypeIdentifiers

// The file page's Info sheet (#5827): what a file or folder is, where, how big, its times, who may do what with it,
// and for a symlink what it says and where it leads (`GET …/stat?detail=1`, docs/protocol/protocol.md "File details and
// links"). Permissions are left out for a Windows computer, which has none of these bits.

/// Permission bits in words and as `ls` and `chmod` write them.
nonisolated enum FilePermissions {
    /// rw-r--r-- (with s / t where setuid, setgid, sticky are on).
    static func symbolic(_ mode: Int) -> String {
        var out = ""
        for (i, shift) in [6, 3, 0].enumerated() {
            let bits = (mode >> shift) & 7
            out += bits & 4 != 0 ? "r" : "-"
            out += bits & 2 != 0 ? "w" : "-"
            let special = [0o4000, 0o2000, 0o1000][i] & mode != 0
            let x = bits & 1 != 0
            out += special ? (i == 2 ? (x ? "t" : "T") : (x ? "s" : "S")) : (x ? "x" : "-")
        }
        return out
    }

    /// 644, or 4755 with a special bit.
    static func octal(_ mode: Int) -> String { String(mode & 0o7777, radix: 8) }

    /// What one class (owner, group, others) may do, in words.
    static func access(_ bits: Int) -> String {
        switch bits & 7 {
        case 7: String(localized: "Read, Write & Run")
        case 6: String(localized: "Read & Write")
        case 5: String(localized: "Read & Run")
        case 4: String(localized: "Read Only")
        case 3: String(localized: "Write & Run")
        case 2: String(localized: "Write Only")
        case 1: String(localized: "Run Only")
        default: String(localized: "No Access")
        }
    }

    /// "Owner: Read & Write · Everyone else: Read Only" (the group named only when it differs from everyone else).
    static func words(_ mode: Int) -> String {
        let owner = (mode >> 6) & 7, group = (mode >> 3) & 7, others = mode & 7
        if group == others {
            return String(localized: "Owner: \(access(owner)) · Everyone else: \(access(others))")
        }
        return String(localized: "Owner: \(access(owner)) · Group: \(access(group)) · Everyone else: \(access(others))")
    }
}

/// The type the Info sheet names a file by: the system's for its extension, except where the extension names two
/// things. `.ts`, `.mts` and `.cts` are TypeScript as often as MPEG-2 transport streams, so the node's reading of the
/// bytes decides (`mime`, docs/protocol/protocol.md "File details and links", #7974); without it (an older node) none.
nonisolated enum FileKind {
    static let scriptOrStream: Set<String> = ["ts", "mts", "cts"]

    static func type(name: String, mime: String?) -> UTType? {
        let ext = (name as NSString).pathExtension.lowercased()
        guard !ext.isEmpty else { return nil }
        guard scriptOrStream.contains(ext) else { return UTType(filenameExtension: ext) }
        let media = (mime ?? "").split(separator: ";", maxSplits: 1).first?.trimmingCharacters(in: .whitespaces).lowercased() ?? ""
        if media.hasPrefix("video/") { return .mpeg2TransportStream }
        if media.hasPrefix("text/") { return .sourceCode }
        return nil
    }
}

struct FileInfoSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machine: MachineRef
    let path: String
    let cwd: String
    /// What the listing said, shown until the details are here (and instead of them from an older hub or node).
    var entry: FileEntry?
    /// Go to where a link leads: a folder opens, a file is shown in its folder.
    var goTo: ((_ path: String, _ dir: Bool) -> Void)?
    @State private var info: FileInfo?
    @State private var error: String?
    @State private var loading = true
    @State private var editingMode = false

    private var name: String {
        let n = (path as NSString).lastPathComponent
        return n.isEmpty ? path : n
    }
    private var home: String? { model.machine(machine)?.home }
    private var windows: Bool { model.machine(machine)?.os?.lowercased() == "windows" }
    private var isDir: Bool { info.map { $0.kind == "dir" } ?? entry?.dir ?? false }
    /// Permissions can be changed here (#5831): the hub has `file_chmod`, the computer is not Windows, the folder takes changes.
    private var canChangeMode: Bool {
        let f = model.features(machine.group)
        return (f.isEmpty || f.contains("file_chmod")) && FilesNavigation.writable((path as NSString).deletingLastPathComponent, home: home)
            && info?.symlink != true
    }
    private var hubHasInfo: Bool {
        let f = model.features(machine.group)
        return f.isEmpty || f.contains("file_info")
    }

    var body: some View {
        NavigationStack {
            List {
                header.listRowBackground(Color.clear)
                linkSection
                Section {
                    row(String(localized: "Location"), FilesNavigation.tilde((path as NSString).deletingLastPathComponent, home: home), mono: true)
                    if isDir {
                        if let items = info?.items {
                            row(String(localized: "Contents"), info?.itemsMore == true ? String(localized: "More than \(items) items") : String(localized: "\(items) items"))
                        }
                    } else if let size = info?.size ?? entry?.size {
                        row(String(localized: "Size [File Info]"), size >= 1024 ? String(localized: "\(size.storageText) (\(size.formatted()) bytes)") : String(localized: "\(size.formatted()) bytes"))
                    }
                    if let t = info?.birthtime { row(String(localized: "Created"), t.formatted(date: .abbreviated, time: .shortened)) }
                    if let t = info?.mtime ?? entry?.mtime { row(String(localized: "Modified"), t.formatted(date: .abbreviated, time: .shortened)) }
                    if let t = info?.ctime { row(String(localized: "Attributes Changed"), t.formatted(date: .abbreviated, time: .shortened)) }
                }
                if !windows, let mode = info?.mode {
                    Section("Permissions") {
                        row(String(localized: "Who Can Do What"), FilePermissions.words(mode))
                        if let owner = info?.owner {
                            row(String(localized: "Owner"), info?.group.map { String(localized: "\(owner) (group \($0))") } ?? owner)
                        }
                        row(String(localized: "Permission Code"), "\(FilePermissions.symbolic(mode)) · \(FilePermissions.octal(mode))", mono: true)
                        if canChangeMode {
                            Button { editingMode = true } label: { Label("Change Permissions…", systemImage: Theme.Icon.edit) }
                                .themedRow()
                                .accessibilityIdentifier("files.info.permissions")
                        }
                    }
                }
                Section {
                    Button {
                        UIPasteboard.general.string = path
                        Haptics.tap()
                        model.toast = Toast(String(localized: "Path copied"), style: .success)
                    } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
                    .themedRow()
                } footer: {
                    if let note { Text(note) }
                }
            }
            .themedGroupedPage()
            .navigationTitle(String(localized: "Info [File Info]"))
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1)
                }
            }
            .accessibilityIdentifier("files.info")
        }
        .task(id: path) { await load() }
        .sheet(isPresented: $editingMode) {
            FilePermissionsSheet(machine: machine, path: path, cwd: cwd, mode: info?.mode) { _ in Task { await load() } }
                .presentationDetents([.medium, .large])
        }
    }

    private var header: some View {
        VStack(spacing: Theme.Space.xs) {
            ThemedIcon(isDir ? Theme.Icon.folderFilled : FileViewer.symbol(for: name))
                .font(Theme.Font.title)
                .foregroundStyle(isDir ? Theme.accent : Color.secondary)
            Text(name).font(Theme.Font.heading).multilineTextAlignment(.center).lineLimit(3)
            Text(kind).font(Theme.Font.label).foregroundStyle(.secondary)
            if loading { ProgressView().padding(.top, Theme.Space.xs) }
        }
        .frame(maxWidth: .infinity)
    }

    /// "Folder", "Swift Source", "Link to a folder"…
    private var kind: String {
        let base: String
        if isDir {
            base = String(localized: "Folder")
        } else if let t = FileKind.type(name: name, mime: info?.mime), let d = t.localizedDescription {
            base = d
        } else {
            base = String(localized: "File")
        }
        if info?.linkBroken == true || entry?.linkBroken == true { return String(localized: "Broken Link") }
        if info?.symlink == true || entry?.symlink == true { return String(localized: "Link to \(base)") }
        return base
    }

    @ViewBuilder private var linkSection: some View {
        if info?.symlink == true || entry?.symlink == true, let says = info?.linkTarget ?? entry?.linkTarget {
            let broken = info?.linkBroken ?? entry?.linkBroken ?? false
            Section("Link") {
                row(String(localized: "Link Says"), says, mono: true)
                if broken {
                    Text("What it points to isn’t there").foregroundStyle(Theme.danger).themedRow()
                } else if let real = info?.linkReal {
                    row(String(localized: "Real Location"), FilesNavigation.tilde(real, home: home), mono: true)
                    if let goTo {
                        Button {
                            dismiss()
                            goTo(real, isDir)
                        } label: { Label("Go to Real Location", systemImage: Theme.Icon.openExternal) }
                        .themedRow()
                        .accessibilityIdentifier("files.info.real")
                    }
                }
            }
        }
    }

    private func row(_ label: String, _ value: String, mono: Bool = false) -> some View {
        LabeledContent {
            Text(value)
                .font(mono ? Theme.Font.code : Theme.Font.secondary)
                .multilineTextAlignment(.trailing)
                .lineLimit(3)
                .textSelection(.enabled)
        } label: {
            Text(label).lineLimit(1)
        }
        .themedRow()
    }

    /// Why less is shown than the sheet has room for.
    private var note: String? {
        if let error { return error }
        if !hubHasInfo { return String(localized: "Update the hub to see more details, such as when it was created and who can open it.") }
        if let info, !info.detailed, info.kind != "missing" {
            let name = model.machine(machine)?.title ?? machine.id
            return String(localized: "Update Persona on \(name) to see more details, such as when it was created and who can open it.")
        }
        if info?.kind == "missing" { return String(localized: "It isn’t there anymore, or it can’t be read from your phone.") }
        return nil
    }

    private func load() async {
        defer { loading = false }
        guard hubHasInfo, let api = model.api(machine.group) else { return }
        do {
            info = try await api.fileInfo(machine: machine.id, path: path, cwd: cwd)
            error = nil
        } catch {
            if !error.isCancellation { self.error = error.localizedDescription }
        }
    }
}
