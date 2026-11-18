#if os(iOS)
import Foundation
import Observation
import OSLog

/// The meeting tapes on this phone, to replay (#10589; docs/apple/recording.md "A meeting's tape and its replay",
/// docs/operations/developing-persona.md "Replaying a meeting tape"). A tape is a folder holding `tape.json` and the
/// audio it names. They come from the owner's computers (a folder per tape in `~/.persona/meeting-tapes`, listed and
/// downloaded through the hub, which relays the bytes and keeps nothing) or from the Files app, and are kept in
/// Application Support/meeting-tapes, out of backups: a tape holds other people's words. Turning Developing Persona off
/// deletes them all (`deleteAll`).
@MainActor @Observable final class MeetingTapeLibrary {
    static let shared = MeetingTapeLibrary()

    /// Where a computer keeps its tapes for the phones to list.
    nonisolated static let computerFolder = "~/.persona/meeting-tapes"
    /// The tape's file in a tape's folder.
    nonisolated static let tapeName = "tape.json"
    /// The largest tape and audio taken: a 3-hour meeting's tape is a few MB, its 16 kHz audio about 350 MB.
    nonisolated static let largestTape: Int64 = 32 << 20
    nonisolated static let largestAudio: Int64 = 1 << 30

    /// A tape on this phone.
    nonisolated struct Tape: Identifiable, Equatable, Sendable {
        /// Its folder's name.
        var id: String
        var folder: URL
        var summary: MeetingTape.Summary
        /// Its audio file, when it has one here.
        var audio: URL?
        var bytes: Int64
        var added: Date
    }

    /// A tape on one of the owner's computers, not on this phone yet.
    nonisolated struct Remote: Identifiable, Equatable, Sendable {
        var machine: String
        var machineName: String
        /// The tape's folder there (absolute, as the computer listed it) and its name.
        var path: String
        var name: String
        var id: String { "\(machine)/\(path)" }
    }

    private(set) var tapes: [Tape] = []
    private(set) var loaded = false
    /// The computers' tapes, by computer as the machines list orders them; `listed` once they were asked.
    private(set) var remote: [Remote] = []
    private(set) var listed = false
    /// The tape being fetched from a computer, and how far, 0...1 (nil while the size isn't known).
    private(set) var fetching: String?
    private(set) var fetched: Double?

    @ObservationIgnored private let root: URL
    nonisolated private static let log = Logger(subsystem: "persona", category: "recording")

    init(root: URL = MeetingTapeLibrary.folder) {
        self.root = root
    }

    /// Application Support/meeting-tapes.
    nonisolated static var folder: URL { URL.applicationSupportDirectory.appending(path: "meeting-tapes") }

    // MARK: On this phone

    /// Reads the tapes here (off the main actor).
    func refresh() async {
        let root = root
        tapes = await Task.detached(priority: .userInitiated) { Self.read(root) }.value
        loaded = true
    }

    /// The tape `tape` holds, read off the main actor; nil when it can't be read.
    nonisolated static func load(_ tape: Tape) async -> MeetingTape? {
        await Task.detached(priority: .userInitiated) {
            try? MeetingTape.decode(Data(contentsOf: tape.folder.appending(path: tapeName)))
        }.value
    }

    func delete(_ tape: Tape) async {
        let folder = tape.folder
        await Task.detached(priority: .userInitiated) { try? FileManager.default.removeItem(at: folder) }.value
        tapes.removeAll { $0.id == tape.id }
    }

    /// Developing Persona turned off: every tape goes.
    func deleteAll() {
        tapes = []
        let root = root
        Task.detached(priority: .utility) { try? FileManager.default.removeItem(at: root) }
    }

    /// Whether any tape is here (the switch asks before turning off).
    var hasTapes: Bool { !tapes.isEmpty }

    nonisolated static func read(_ root: URL) -> [Tape] {
        let fm = FileManager.default
        let folders = (try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: [.isDirectoryKey, .creationDateKey])) ?? []
        return folders.compactMap { folder -> Tape? in
            let file = folder.appending(path: tapeName)
            guard let data = try? Data(contentsOf: file), let tape = try? MeetingTape.decode(data) else { return nil }
            let audio = tape.audio.map { folder.appending(path: $0) }.flatMap { fm.fileExists(atPath: $0.path) ? $0 : nil }
            let audioBytes = audio.flatMap { try? $0.resourceValues(forKeys: [.fileSizeKey]).fileSize } ?? 0
            let added = (try? folder.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? .distantPast
            return Tape(id: folder.lastPathComponent, folder: folder, summary: tape.summary, audio: audio,
                        bytes: Int64(data.count + audioBytes), added: added)
        }
        .sorted { $0.added > $1.added }
    }

    /// Keeps `json` (a tape's) and `audio` as a new tape here: a folder of its own, out of backups. Throws when `json`
    /// is no tape.
    nonisolated static func keep(json: URL, audio: URL?, in root: URL, name: String) throws -> URL {
        let fm = FileManager.default
        let size = (try? json.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? 0
        guard size <= largestTape else { throw ImportError.tooLarge }
        let data = try Data(contentsOf: json)
        var tape = try MeetingTape.decode(data)
        if let audio {
            let audioSize = (try? audio.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? 0
            guard audioSize <= largestAudio else { throw ImportError.tooLarge }
        }
        try fm.createDirectory(at: root, withIntermediateDirectories: true)
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        var excluded = root
        try? excluded.setResourceValues(values)
        let folder = root.appending(path: "\(Self.slug(name))-\(UUID().uuidString.prefix(8).lowercased())")
        try fm.createDirectory(at: folder, withIntermediateDirectories: false)
        do {
            if let audio {
                // Kept under the tape's own name for it; a tape that named none names the file it came with.
                let named = tape.audio.map { ($0 as NSString).lastPathComponent }.flatMap { $0.isEmpty ? nil : $0 } ?? audio.lastPathComponent
                try fm.copyItem(at: audio, to: folder.appending(path: named))
                if tape.audio != named {
                    tape.audio = named
                    try tape.encoded().write(to: folder.appending(path: tapeName), options: .atomic)
                    return folder
                }
            }
            try data.write(to: folder.appending(path: tapeName), options: .atomic)
            return folder
        } catch {
            try? fm.removeItem(at: folder)
            throw error
        }
    }

    private nonisolated static func slug(_ name: String) -> String {
        let kept = name.lowercased().unicodeScalars.map { CharacterSet.alphanumerics.contains($0) && $0.isASCII ? Character($0) : "-" }
        let text = String(kept).split(separator: "-").joined(separator: "-")
        return text.isEmpty ? "tape" : String(text.prefix(40))
    }

    nonisolated enum ImportError: LocalizedError, Equatable {
        case noTape, tooLarge
        var errorDescription: String? {
            switch self {
            case .noTape: String(localized: "That isn't a meeting tape. Choose its folder, or its tape.json and audio together.")
            case .tooLarge: String(localized: "That tape is too large.")
            }
        }
    }

    // MARK: From Files

    /// Takes a tape from what was chosen in Files: its folder, or its `tape.json` with its audio (picked together).
    func importPicked(_ urls: [URL]) async throws -> Tape {
        let root = root
        let folder = try await Task.detached(priority: .userInitiated) { try Self.importing(urls, into: root) }.value
        await refresh()
        guard let tape = tapes.first(where: { $0.folder.lastPathComponent == folder.lastPathComponent }) else { throw ImportError.noTape }
        return tape
    }

    nonisolated static func importing(_ urls: [URL], into root: URL) throws -> URL {
        let scoped = urls.filter { $0.startAccessingSecurityScopedResource() }
        defer { scoped.forEach { $0.stopAccessingSecurityScopedResource() } }
        let fm = FileManager.default
        var files: [URL] = []
        var folderName: String?
        for url in urls {
            if (try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true {
                files += (try? fm.contentsOfDirectory(at: url, includingPropertiesForKeys: nil)) ?? []
                folderName = url.lastPathComponent
            } else {
                files.append(url)
            }
        }
        guard let json = files.first(where: { $0.lastPathComponent == tapeName }) ?? files.first(where: { $0.pathExtension.lowercased() == "json" })
        else { throw ImportError.noTape }
        let tape: MeetingTape
        do { tape = try MeetingTape.decode(Data(contentsOf: json)) } catch { throw ImportError.noTape }
        // Named after its folder, else its file, else its title.
        let base = json.deletingPathExtension().lastPathComponent
        let name = folderName ?? (base == "tape" ? tape.title : base)
        let others = files.filter { $0 != json && $0.pathExtension.lowercased() != "json" }
        let audio = tape.audio.flatMap { named in others.first { $0.lastPathComponent == (named as NSString).lastPathComponent } }
            ?? (others.count == 1 ? others.first : nil)
        return try keep(json: json, audio: audio, in: root, name: name)
    }

    // MARK: From the owner's computers

    /// Lists the tapes in `computerFolder` on each online computer; a computer without the folder lists none.
    func refreshComputers(_ machines: [Machine], api: APIClient) async {
        var found: [Remote] = []
        await withTaskGroup(of: (Int, [Remote]).self) { group in
            for (i, machine) in machines.enumerated() where machine.online {
                group.addTask {
                    guard let listing = try? await api.listDir(machine: machine.id, path: Self.computerFolder, cwd: "~") else { return (i, []) }
                    let tapes = listing.entries.filter { $0.dir }.sorted { ($0.mtime ?? .distantPast) > ($1.mtime ?? .distantPast) }
                    return (i, tapes.map { Remote(machine: machine.id, machineName: machine.name, path: Self.join(listing.path, $0.name), name: $0.name) })
                }
            }
            var byMachine: [(Int, [Remote])] = []
            for await answer in group { byMachine.append(answer) }
            found = byMachine.sorted { $0.0 < $1.0 }.flatMap(\.1)
        }
        remote = found
        listed = true
    }

    /// Brings `remote` to this phone: its `tape.json`, then the audio it names, with progress; the new tape.
    func fetch(_ remote: Remote, api: APIClient) async throws -> Tape {
        fetching = remote.id
        fetched = nil
        defer { fetching = nil; fetched = nil }
        let listing = try await api.listDir(machine: remote.machine, path: remote.path, cwd: "~")
        guard let json = listing.entries.first(where: { !$0.dir && $0.name == Self.tapeName }) else { throw ImportError.noTape }
        guard json.size <= Self.largestTape else { throw ImportError.tooLarge }
        let staging = FileManager.default.temporaryDirectory.appending(path: "meeting-tape-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: staging, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: staging) }
        let jsonFile = staging.appending(path: Self.tapeName)
        try await api.download("/api/v1/machines/\(remote.machine)/file/download",
                               query: ["path": Self.join(listing.path, json.name), "cwd": "~"], to: jsonFile) { _, _ in }
        let named = try await Task.detached(priority: .userInitiated) { try MeetingTape.decode(Data(contentsOf: jsonFile)).audio }.value
        var audioFile: URL?
        if let named, let entry = listing.entries.first(where: { !$0.dir && $0.name == (named as NSString).lastPathComponent }) {
            guard entry.size <= Self.largestAudio else { throw ImportError.tooLarge }
            let file = staging.appending(path: entry.name)
            let total = Double(max(1, entry.size + json.size))
            let done = Double(json.size)
            try await api.download("/api/v1/machines/\(remote.machine)/file/download",
                                   query: ["path": Self.join(listing.path, entry.name), "cwd": "~"], to: file) { got, _ in
                let part = min(1, (done + Double(got)) / total)
                Task { @MainActor in MeetingTapeLibrary.shared.progress(remote.id, part) }
            }
            audioFile = file
        }
        let root = root, name = remote.name, audio = audioFile
        let folder = try await Task.detached(priority: .userInitiated) { try Self.keep(json: jsonFile, audio: audio, in: root, name: name) }.value
        await refresh()
        guard let tape = tapes.first(where: { $0.folder.lastPathComponent == folder.lastPathComponent }) else { throw ImportError.noTape }
        Self.log.notice("tapes: fetched one from a computer")
        return tape
    }

    private func progress(_ id: String, _ part: Double) {
        guard fetching == id else { return }
        fetched = part
    }

    private nonisolated static func join(_ dir: String, _ name: String) -> String {
        dir.hasSuffix("/") ? dir + name : dir + "/" + name
    }
}

#if DEBUG
extension MeetingTapeLibrary {
    /// `-openMeetingTapes sample|empty` (#10589): Developing Persona on and its tapes page open (`SettingsView`), with
    /// the made-up tape (`MeetingTapeSample`) on this phone, or none.
    static func installSampleIfAsked() {
        guard let value = UserDefaults.standard.string(forKey: "openMeetingTapes") else { return }
        DevMode.shared.setOn(true)
        let root = folder
        let sample = value == "sample" ? try? MeetingTapeSample.tape.encoded() : nil
        Task {
            await Task.detached(priority: .userInitiated) {
                try? FileManager.default.removeItem(at: root)
                guard let sample else { return }
                let staging = FileManager.default.temporaryDirectory.appending(path: "meeting-tape-sample")
                try? FileManager.default.createDirectory(at: staging, withIntermediateDirectories: true)
                let json = staging.appending(path: tapeName)
                try? sample.write(to: json)
                _ = try? keep(json: json, audio: nil, in: root, name: "sample")
            }.value
            await shared.refresh()
        }
    }
}
#endif
#endif
