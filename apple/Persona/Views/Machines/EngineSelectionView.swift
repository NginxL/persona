import SwiftUI

/// Pairing uses the same picker as Settings, with an always-available way to continue without saving.
struct PairingEngineView: View {
    var ref: MachineRef

    var body: some View {
        NavigationStack {
            EngineSelectionView(ref: ref, pairing: true)
        }
    }
}

struct EngineSelectionView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    var ref: MachineRef
    var pairing = false
    @State private var selection: EngineSelectionModel?
    @State private var botmux: BotmuxSwitchModel?
    private var machineID: String { ref.id }

    var body: some View {
        let machine = app.machine(ref)
        Form {
            if let machine, let selection {
                #if os(iOS) || os(visionOS)
                if pairing {
                    Section {
                        Text("Which engine does this computer use?").font(Theme.Font.title)
                        Text(machine.name).font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                }
                #endif
                Section {
                    ForEach(EngineSelectionModel.options(machine)) { found in
                        let engine = MachineEngine(rawValue: found.id)
                        Button { selection.choose(engine) } label: {
                            HStack(spacing: Theme.Space.m) {
                                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                                    Text(found.name).font(Theme.Font.heading).foregroundStyle(.primary).lineLimit(1)
                                    if let detail = engine.detail {
                                        Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                    // Nothing to detect for no engine: it is there whenever the hub takes host sets.
                                    if engine != .noEngine {
                                        Text(detection(engine, machine)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                    if let note = found.note, !note.isEmpty {
                                        Text(note).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                                            .minimumScaleFactor(0.8)
                                    }
                                }
                                Spacer(minLength: Theme.Space.s)
                                ThemedIcon(selection.selected == engine ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                                    .font(Theme.Font.heading)
                                    .accessibilityHidden(true)
                                    .foregroundStyle(selection.selected == engine ? Theme.accent : Color.secondary)
                            }
                            .padding(.vertical, Theme.Space.s)
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("engine.option.\(engine.rawValue)")
                        .accessibilityAddTraits(selection.selected == engine ? .isSelected : [])
                        .disabled(selection.saving || !EngineSelectionModel.canSelect(engine, on: machine))
                    }
                } header: {
                    Text("Engine")
                } footer: {
                    if selection.selected == .herdrBoth {
                        Text(machine.online ? String(localized: "Shows sessions from both herdr and Herdr App. Sessions you start from your phone still open in herdr. You can change this anytime in Settings › Machines.") : String(localized: "The machine is offline; your choice takes effect once it connects. Sessions from both engines are shown."))
                    } else if selection.selected == .noEngine {
                        Text(machine.online ? String(localized: "Shows the desktop apps' sessions. Starting or continuing a session from your phone is off. You can change this anytime in Settings › Machines.") : String(localized: "The machine is offline; your choice takes effect once it connects. The desktop apps' sessions are shown, and starting or continuing a session from your phone is off."))
                    } else {
                        Text(machine.online ? String(localized: "Only sessions in the selected engine are shown. You can change this anytime in Settings › Machines.") : String(localized: "The machine is offline; your choice takes effect once it connects. Only sessions in the selected engine are shown."))
                    }
                }
                if let error = selection.error {
                    Section {
                        Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger)
                        if pairing { Text("Choose an engine to continue. Check your network and tap Confirm again.").font(Theme.Font.secondary) }
                    }
                }
                #if os(iOS) || os(visionOS)
                Section {
                    Button { save(selection) } label: {
                        HStack {
                            Text(pairing ? String(localized: "Confirm and Continue") : String(localized: "Save")).lineLimit(1)
                            Spacer()
                            if selection.saving { ProgressView() }
                        }
                    }
                    .disabled(selection.saving || !EngineSelectionModel.canSelect(selection.selected, on: machine))
                    .accessibilityIdentifier("engine.save")
                }
                #endif
                if !pairing, let botmux, let on = botmux.on {
                    // Saved as soon as it is flipped, not with Save: it is not part of the engine choice (#2575).
                    Section {
                        Toggle(isOn: Binding(get: { on }, set: { next in Task { await botmux.set(next) } })) {
                            Text("Also Show botmux Sessions").font(Theme.Font.body).lineLimit(1)
                        }
                        .disabled(botmux.saving)
                        .accessibilityIdentifier("engine.botmux")
                        if let error = botmux.error {
                            Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger)
                        }
                    } footer: {
                        Text("When botmux is installed on this computer, its sessions are listed along with the selected engine's. Turn this off to stop listing them and stop running botmux.")
                    }
                }
            } else {
                Section {
                    Text("Can't read this machine right now")
                    if pairing { Button { finish() } label: { Text("Continue Anyway").lineLimit(1) } }
                }
            }
        }
        .themedPage()
        .detailTitle(String(localized: "Choose Engine"))
        .navigationBarTitleDisplayMode(.inline)
        .task(id: ref) {
            guard let machine else { return }
            selection = EngineSelectionModel(machine: machine, pairing: pairing, deps: .init(api: { [ref] in app.api(ref.group) }, updated: { app.applyMachine($0) }))
            guard !pairing else { return }
            let api = { [ref] in app.api(ref.group) }
            let model = BotmuxSwitchModel(deps: .init(
                load: { [ref] in
                    guard let api = api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                    return try await api.botmux(machine: ref.id)
                },
                save: { [ref] on in
                    guard let api = api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                    return try await api.setBotmux(machine: ref.id, on: on)
                }))
            botmux = model
            await model.load()
        }
        .onChange(of: machine) { _, updated in
            if let updated { selection?.synchronize(updated, pairing: pairing) }
        }
    }

    private func detection(_ engine: MachineEngine, _ machine: Machine) -> String {
        guard machine.online else { return String(localized: "Offline, not detected yet") }
        guard let found = machine.engines.first(where: { $0.id == engine.rawValue }) else { return String(localized: "Not detected yet") }
        let status = found.available ? String(localized: "Available [Engine Selection View 10dd90]") : String(localized: "Not found")
        return found.available && !(found.version ?? "").isEmpty ? "\(status) · \(found.version!)" : status
    }

    private func save(_ selection: EngineSelectionModel) {
        Task { if await selection.save(machineID: machineID) { finish() } }
    }

    private func finish() {
        if pairing { app.pairingEngineMachine = nil } else { dismiss() }
    }
}
