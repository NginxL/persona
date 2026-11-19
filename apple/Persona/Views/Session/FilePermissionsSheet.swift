import SwiftUI

// Changing a file's or folder's permissions (#5831): read, write and run for the owner, the group and everyone else, as
// switches; Save asks first and says what changes. Not offered for a Windows computer, nor where the computer takes no
// changes (outside the home). docs/protocol/protocol.md "Changing files" (`files/chmod`).

/// What changes between two modes, in words: "Everyone else: Read Only → Read & Run".
nonisolated enum FileModeChange {
    static func words(from old: Int, to new: Int) -> String {
        let names = [String(localized: "Owner"), String(localized: "Group [Permissions]"), String(localized: "Everyone else")]
        var parts: [String] = []
        for (i, shift) in [6, 3, 0].enumerated() where (old >> shift) & 7 != (new >> shift) & 7 {
            parts.append("\(names[i]): \(FilePermissions.access((old >> shift) & 7)) → \(FilePermissions.access((new >> shift) & 7))")
        }
        return parts.joined(separator: " · ")
    }
}

extension APIClient {
    private struct ChmodRequest: Encodable { var path, cwd: String; var mode: Int }
    private struct ChmodResponse: Decodable { var mode: Int }
    /// Sets path's permission bits (0–0777); returns them as they are now.
    func changeMode(machine: String, path: String, cwd: String, mode: Int) async throws -> Int {
        try await post("/api/v1/machines/\(machine)/files/chmod", body: ChmodRequest(path: path, cwd: cwd, mode: mode), as: ChmodResponse.self).mode
    }
}

struct FilePermissionsSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machine: MachineRef
    let path: String
    let cwd: String
    /// The bits now, when known (Info has them); else read here.
    var mode: Int?
    var changed: (Int) -> Void = { _ in }
    @State private var old: Int?
    @State private var bits = 0
    @State private var owner: String?
    @State private var group: String?
    @State private var asking = false
    @State private var saving = false

    private var name: String { (path as NSString).lastPathComponent }

    var body: some View {
        NavigationStack {
            Form {
                if old != nil {
                    Section {
                        Grid(alignment: .leading, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.m) {
                            GridRow {
                                Text(verbatim: "")
                                ForEach(Array(columns.enumerated()), id: \.offset) { _, c in
                                    Text(c).font(Theme.Font.label).foregroundStyle(.secondary).gridColumnAlignment(.center)
                                }
                            }
                            ForEach(Array(classes.enumerated()), id: \.offset) { row, who in
                                GridRow {
                                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                        Text(who.title).font(Theme.Font.secondary).lineLimit(1)
                                        if let detail = who.detail { Text(detail).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1) }
                                    }
                                    ForEach(0..<3, id: \.self) { col in
                                        Toggle(isOn: binding(row: row, col: col)) { Text(verbatim: "") }
                                            .labelsHidden()
                                            .accessibilityLabel(String(localized: "\(who.title): \(columns[col])"))
                                            .accessibilityIdentifier("files.perm.\(row).\(col)")
                                    }
                                }
                            }
                        }
                        .padding(.vertical, Theme.Space.xs)
                    } footer: {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(verbatim: "\(FilePermissions.symbolic(bits)) · \(FilePermissions.octal(bits))").font(Theme.Font.code)
                            Text("Run lets a script run; for a folder, it lets people open it.")
                        }
                    }
                    if let old, old != bits {
                        Section {
                            LabeledContent("Before") {
                                Text("\(FilePermissions.symbolic(old)) · \(FilePermissions.octal(old))").font(Theme.Font.code)
                            }
                        }
                    }
                } else {
                    HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
                }
            }
            .themedGroupedPage()
            .navigationTitle(String(localized: "Permissions"))
            .navigationSubtitleWhenSupported(name)
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                SheetClose { dismiss() }
                SheetConfirm(id: "files.perm.save", name: String(localized: "Save"), disabled: old == nil || old == bits || saving) {
                    asking = true
                }
            }
            .alert(String(localized: "Change the permissions of “\(name)”?"), isPresented: $asking) {
                Button("Change [Permissions]") { Task { await save() } }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text(verbatim: "\(FileModeChange.words(from: old ?? bits, to: bits))\n\(FilePermissions.symbolic(old ?? bits)) (\(FilePermissions.octal(old ?? bits))) → \(FilePermissions.symbolic(bits)) (\(FilePermissions.octal(bits)))")
            }
        }
        .task { await load() }
    }

    private var columns: [String] { [String(localized: "Read [Permissions]"), String(localized: "Write [Permissions]"), String(localized: "Run [Permissions]")] }

    private var classes: [(title: String, detail: String?)] {
        [(String(localized: "Owner"), owner), (String(localized: "Group [Permissions]"), group), (String(localized: "Everyone else"), nil)]
    }

    /// The switch for class row (owner, group, others) and column (read, write, run).
    private func binding(row: Int, col: Int) -> Binding<Bool> {
        let bit = 1 << ((2 - row) * 3 + (2 - col))
        return Binding(get: { bits & bit != 0 }, set: { on in bits = on ? bits | bit : bits & ~bit })
    }

    private func load() async {
        if let mode, old == nil {
            old = mode & 0o777
            bits = mode & 0o777
        }
        guard let api = model.api(machine.group), let info = try? await api.fileInfo(machine: machine.id, path: path, cwd: cwd) else { return }
        owner = info.owner
        group = info.group
        if old == nil, let m = info.mode {
            old = m & 0o777
            bits = m & 0o777
        }
    }

    private func save() async {
        guard let api = model.api(machine.group) else { return }
        saving = true
        defer { saving = false }
        do {
            let now = try await api.changeMode(machine: machine.id, path: path, cwd: cwd, mode: bits)
            Haptics.success()
            model.toast = Toast(String(localized: "Permissions changed"), style: .success)
            changed(now)
            dismiss()
        } catch {
            if !error.isCancellation { model.toast = Toast(FolderEdits.message(error), style: .error) }
        }
    }
}
