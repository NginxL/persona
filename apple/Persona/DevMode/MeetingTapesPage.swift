#if os(iOS)
import SwiftUI
import UniformTypeIdentifiers

/// Replay a Meeting Tape (Settings › About › Developing Persona, #10589; docs/operations/developing-persona.md
/// "Replaying a meeting tape"): the speed and the sound, then the tapes: those on this phone, those on the owner's
/// computers (`~/.persona/meeting-tapes`, brought here on a tap) and Choose from Files. A tap starts the tape in the
/// meeting room (`MeetingReplay`), which this page presents as the Recordings page does (`recordingLiveSheet`).
/// Who it serves: people developing and testing Persona; nothing of it shows while Developing Persona is off.
struct MeetingTapesPage: View {
    @Environment(AppModel.self) private var model
    @State private var library = MeetingTapeLibrary.shared
    @State private var speed: Double = 1
    @State private var sound = true
    @State private var choosing = false
    @State private var problem: String?
    /// The tape being opened on a tap (read, or fetched from a computer first).
    @State private var opening: String?
    /// The tape Delete asks about: one from Files can't be had again.
    @State private var deleting: MeetingTapeLibrary.Tape?

    var body: some View {
        SettingsForm {
            Section {
                Picker(selection: $speed) {
                    ForEach(MeetingReplay.speeds, id: \.self) { Text(Self.speedLabel($0)).tag($0) }
                } label: {
                    Text("Speed")
                }
                .pickerStyle(.segmented)
                .accessibilityIdentifier("meeting-tapes-speed")
                Toggle(isOn: $sound) { Text("Play Sound").lineLimit(1) }
                    .accessibilityIdentifier("meeting-tapes-sound")
            } footer: {
                Text("Silent at 4×.")
            }
            Section {
                if library.loaded, library.tapes.isEmpty {
                    Text("No tapes on this iPhone yet").foregroundStyle(.secondary)
                        .accessibilityIdentifier("meeting-tapes-none")
                }
                ForEach(library.tapes) { tape in
                    Button { start(tape) } label: { tapeRow(tape) }
                        .disabled(opening != nil)
                        .accessibilityIdentifier("meeting-tape")
                        .swipeActions {
                            Button(role: .destructive) { deleting = tape } label: {
                                Label(String(localized: "Delete"), systemImage: Theme.Icon.delete)
                            }
                        }
                }
            } header: {
                Text("On This iPhone")
            }
            computers
            Section {
                Button { choosing = true } label: {
                    Label { Text("Choose from Files…").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.folder) }
                }
                .accessibilityIdentifier("meeting-tapes-files")
            } footer: {
                Text("A replay plays on this iPhone only: nothing goes to your hub, nothing is transcribed and no minutes are written. Tapes stay on this iPhone, out of backups, and are deleted when Developing Persona is turned off.")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Replay a Meeting Tape"))
        .fileImporter(isPresented: $choosing, allowedContentTypes: [.folder, .json, .audio], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result, !urls.isEmpty else { return }
            Task { await importPicked(urls) }
        }
        // The room opens over this page when a tape starts, and its page after the meeting closes back to it.
        .recordingLiveSheet()
        .task {
            await library.refresh()
            if let api = model.api { await library.refreshComputers(model.machines, api: api) }
        }
        .alert(problem ?? "", isPresented: Binding(get: { problem != nil }, set: { if !$0 { problem = nil } })) {
            Button(String(localized: "OK"), role: .cancel) {}
        }
        .confirmationDialog(String(localized: "Delete “\(deleting?.summary.title ?? "")”?"),
                            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            titleVisibility: .visible, presenting: deleting) { tape in
            Button(String(localized: "Delete"), role: .destructive) { Task { await library.delete(tape) } }
            Button(String(localized: "Cancel"), role: .cancel) {}
        }
    }

    // MARK: Rows

    private func tapeRow(_ tape: MeetingTapeLibrary.Tape) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.recording).foregroundStyle(Theme.accent)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(tape.summary.title).foregroundStyle(Color.primary).lineLimit(1)
                Text(Self.details(tape.summary)).font(Theme.Font.caption).foregroundStyle(Color.secondary).lineLimit(2)
            }
            Spacer(minLength: Theme.Space.s)
            if opening == tape.id { ProgressView() }
        }
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }

    /// The tapes on the owner's computers, a section each; one line saying where to put them when none has any.
    @ViewBuilder private var computers: some View {
        let groups = Dictionary(grouping: library.remote, by: \.machine)
        let order = library.remote.map(\.machine).reduce(into: [String]()) { if !$0.contains($1) { $0.append($1) } }
        if library.listed, order.isEmpty {
            Section {
                Text("To see a computer's tapes here, put each tape's folder in \(MeetingTapeLibrary.computerFolder) on it.")
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
                    .accessibilityIdentifier("meeting-tapes-computers-none")
            } header: {
                Text("On Your Computers")
            }
        }
        ForEach(order, id: \.self) { machine in
            Section {
                ForEach(groups[machine] ?? []) { remote in
                    Button { fetchAndStart(remote) } label: { remoteRow(remote) }
                        .disabled(opening != nil || library.fetching != nil)
                        .accessibilityIdentifier("meeting-tape-remote")
                }
            } header: {
                Text("On \(groups[machine]?.first?.machineName ?? machine) [Meeting Tapes]")
            }
        }
    }

    private func remoteRow(_ remote: MeetingTapeLibrary.Remote) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.machine).foregroundStyle(Color.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(remote.name).foregroundStyle(Color.primary).lineLimit(1)
                if library.fetching == remote.id {
                    if let part = library.fetched {
                        ProgressView(value: part).accessibilityIdentifier("meeting-tape-progress")
                    } else {
                        Text("Getting it from \(remote.machineName)…").font(Theme.Font.caption).foregroundStyle(Color.secondary)
                    }
                }
            }
            Spacer(minLength: Theme.Space.s)
            if library.fetching != remote.id {
                ThemedIcon(Theme.Icon.save).foregroundStyle(Theme.accent)
            }
        }
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }

    static func speedLabel(_ speed: Double) -> String { "\(Int(speed))×" }

    /// "3:46 · 10 lines · map changed 2 times · with sound".
    static func details(_ s: MeetingTape.Summary) -> String {
        let length = Duration.seconds(Int(s.length.rounded())).formatted(.time(pattern: s.length >= 3600 ? .hourMinuteSecond : .minuteSecond))
        var parts = [length, String(localized: "\(s.lines) lines [Meeting Tape]")]
        if s.topics > 0 { parts.append(String(localized: "map changed \(s.topics) times")) }
        if s.audio != nil { parts.append(String(localized: "with sound")) }
        return parts.joined(separator: " · ")
    }

    // MARK: Actions

    private func start(_ tape: MeetingTapeLibrary.Tape) {
        guard MeetingReplay.canStart else {
            problem = String(localized: "Stop the recording first, then replay the tape.")
            return
        }
        opening = tape.id
        Task {
            defer { opening = nil }
            guard let read = await MeetingTapeLibrary.load(tape) else {
                problem = String(localized: "Couldn't read that tape.")
                return
            }
            if !MeetingReplay.shared.play(read, audioFile: tape.audio, speed: speed, sound: sound) {
                problem = String(localized: "Stop the recording first, then replay the tape.")
            }
        }
    }

    private func fetchAndStart(_ remote: MeetingTapeLibrary.Remote) {
        guard let api = model.api else { return }
        Task {
            do {
                let tape = try await library.fetch(remote, api: api)
                start(tape)
            } catch {
                problem = String(localized: "Couldn't get the tape: \(error.localizedDescription)")
            }
        }
    }

    private func importPicked(_ urls: [URL]) async {
        do {
            let tape = try await library.importPicked(urls)
            start(tape)
        } catch {
            problem = error.localizedDescription
        }
    }
}
#endif
