import SwiftUI

/// Phone Control on a computer's page while the hub's extension iphone is on (docs/extensions/iphone.md): the switch that
/// lets the assistant operate the iPhones paired with this computer, and each of them as the computer sees it now
/// (connected, locked, whether that iPhone allowed it). An iPhone plugged in but not paired yet can be paired from here;
/// an iPhone is operated only once it allowed it in its own app, and the section says where. The node is asked again
/// when the page opens, and every few seconds while a pairing runs.
struct IPhoneControlSection: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    let ref: MachineRef
    var iphone: MachineIPhone
    var online: Bool
    @State private var fresh: MachineIPhone?
    @State private var saving = false
    @State private var asking = false
    @State private var checking = false

    private var shown: MachineIPhone { fresh ?? iphone }

    /// Asked again every few seconds while a pairing runs (it waits for Trust on the iPhone).
    private var watching: Bool { online && shown.enabled && shown.job?.running == true }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Phone Control").font(Theme.Font.heading).padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Toggle(isOn: Binding(get: { shown.enabled }, set: { on in Task { await set(on) } })) {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Allow the Assistant to Operate iPhones Through This Computer").font(Theme.Font.body)
                        Text("Sees the screen, taps, swipes and types on iPhones paired with this computer").font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
                .disabled(saving || !online)
                .accessibilityIdentifier("iphone-switch")
                if shown.enabled {
                    Divider()
                    state
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .card()
        }
        .task(id: online && shown.enabled) {
            if online && shown.enabled { await refresh() }
        }
        // Only while the page is in front: not behind another tab or with the app in the background.
        .tabActiveTask(id: watching && phase == .active) {
            guard phase == .active else { return }
            while watching, !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                await refresh()
            }
        }
    }

    // MARK: State

    @ViewBuilder private var state: some View {
        let p = shown
        if !online {
            line(String(localized: "Machine is offline. Available once it's connected."), ok: false)
        } else {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let note = p.note, !note.isEmpty { line(note, ok: false) }
                if let job = p.job, job.running {
                    HStack(spacing: Theme.Space.s) {
                        ProgressView().controlSize(.small)
                        Text(job.step ?? String(localized: "Pairing… Tap Trust on the iPhone."))
                            .font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("iphone-job")
                    action(String(localized: "Cancel"), "cancel", device: nil, identifier: "iphone-cancel")
                } else if let job = p.job, let error = IPhoneControlText.jobFailure(job) {
                    line(error, ok: false)
                }
                if p.devices.isEmpty {
                    if p.note == nil { line(String(localized: "No iPhone paired with this computer can be reached"), ok: false) }
                    Text("Connect the iPhone to this computer with a cable, then come back to pair it.")
                        .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                } else {
                    ForEach(p.devices) { d in deviceRow(d, pairing: p.job?.running == true) }
                    if p.devices.contains(where: { $0.paired && !$0.allowed }) {
                        Text("An iPhone is operated only after it allows it: in Persona on that iPhone, open Settings › Connectors › This iPhone.")
                            .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                recheckButton
            }
        }
    }

    private func deviceRow(_ d: IPhoneDevice, pairing: Bool) -> some View {
        let (text, ok) = IPhoneControlText.state(d)
        return HStack(alignment: .center, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.phone).foregroundStyle(ok ? Theme.positive : Color.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(d.name.isEmpty ? String(localized: "iPhone") : d.name).font(Theme.Font.secondaryMedium).lineLimit(1)
                Text(text).font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("iphone-device-\(d.id)") // on the text, not the row: a row's id would replace Pair's
            Spacer(minLength: Theme.Space.s)
            if !d.paired && d.usb && !pairing {
                action(String(localized: "Pair [iPhone Control]"), "pair", device: d.id, prominent: true, identifier: "iphone-pair-\(d.id)")
            }
        }
    }

    private func line(_ text: String, ok: Bool) -> some View {
        Label {
            Text(text).font(Theme.Font.secondary).foregroundStyle(ok ? Color.primary : Color.secondary)
                .fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(ok ? Theme.Icon.success : Theme.Icon.error).foregroundStyle(ok ? Theme.positive : Theme.caution)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("iphone-status")
    }

    private func action(_ title: String, _ what: String, device: String?, prominent: Bool = false, identifier: String) -> some View {
        Button { Task { await run(what, device: device) } } label: {
            Text(title).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .themedButton(prominent: prominent)
        .disabled(asking)
        .accessibilityIdentifier(identifier)
    }

    private var recheckButton: some View {
        Button { Task { await recheck() } } label: {
            if checking { ProgressView().controlSize(.small) } else { Text("Check Again [Browser Section 953cf0]").font(Theme.Font.labelStrong).lineLimit(1) }
        }
        .themedButton()
        .disabled(checking)
        .accessibilityIdentifier("iphone-recheck")
    }

    // MARK: Calls

    private func refresh() async {
        guard let api = model.api(ref.group) else { return }
        if let p = try? await api.iphone(machine: ref.id) { fresh = p }
    }

    private func recheck() async {
        checking = true
        defer { checking = false }
        await refresh()
    }

    private func set(_ on: Bool) async {
        guard let api = model.api(ref.group) else { return }
        saving = true
        defer { saving = false }
        do {
            fresh = try await api.setIPhone(machine: ref.id, enabled: on)
            Haptics.success()
        } catch {
            if let m = error.shown { model.toast = Toast(on ? String(localized: "Couldn't turn on: \(m)") : String(localized: "Couldn't turn off: \(m)"), style: .error) }
        }
    }

    private func run(_ what: String, device: String?) async {
        guard let api = model.api(ref.group) else { return }
        asking = true
        defer { asking = false }
        do {
            fresh = try await api.iphoneJob(machine: ref.id, what, device: device)
        } catch {
            if let m = error.shown { model.toast = Toast(m, style: .error) }
        }
    }
}

/// How an iPhone reads in the sections that list it: one line of its state, and whether it is ready for the assistant.
nonisolated enum IPhoneControlText {
    static func jobFailure(_ job: ComputerJob) -> String? {
        guard job.state == "failed" else { return nil }
        if job.kind == "prepare" { return prepareFailure(job.error) }
        guard let error = job.error else { return nil }
        return job.kind == "pair" ? String(localized: "Couldn't pair: \(error)") : error
    }

    static func prepareFailure(_ reason: String?) -> String {
        if let reason, !reason.isEmpty { return String(localized: "Couldn't get this iPhone ready: \(reason)") }
        return String(localized: "Couldn't get this iPhone ready.")
    }

    static func state(_ d: IPhoneDevice) -> (String, ok: Bool) {
        let version = d.ios.map { String(localized: "iOS \($0)") }
        let what: String
        var ok = false
        if !d.paired {
            what = d.note ?? String(localized: "Connected with a cable, not paired yet")
        } else if !d.connected {
            what = d.note ?? String(localized: "Not connected")
        } else if d.locked {
            what = String(localized: "Locked: unlock it to let the assistant operate it")
        } else if !d.ready {
            what = d.note ?? String(localized: "Not ready yet")
        } else if !d.allowed {
            what = String(localized: "Waiting for this iPhone to allow it")
        } else {
            what = String(localized: "Ready · the assistant can operate it")
            ok = true
        }
        return ([version, what].compactMap { $0 }.joined(separator: " · "), ok)
    }
}
