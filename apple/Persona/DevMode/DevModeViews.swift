import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// Settings › About's Developing Persona group (#9460, docs/operations/developing-persona.md): the switch, and while
/// it is on, how much is recorded and the way to export it. Turning it off with something recorded asks first and
/// offers to export.
struct DevModeSection: View {
    @Environment(AppModel.self) private var model
    @State private var confirmOff = false
    private let dev = DevMode.shared

    var body: some View {
        Section {
            // A closure, not `set: switched`: Xcode 26.0's IRGen overflows on that isolated method value (docs/lessons/ios.md).
            Toggle(isOn: Binding(get: { dev.isOn }, set: { switched($0) })) {
                Text("Developing Persona").lineLimit(1)
            }
            .accessibilityIdentifier("dev-mode-switch")
            if dev.isOn {
                LabeledContent(String(localized: "Recorded [Developing Persona]"), value: recorded)
                    .accessibilityIdentifier("dev-mode-recorded")
                NavigationLink(value: SettingsPage.devExport) {
                    Text("Export Diagnostics…").lineLimit(1)
                }
                .accessibilityIdentifier("dev-mode-export")
                #if os(iOS)
                // An on-device experiment for Phone › Recents (#9887): the kind of handle a call to Persona carries.
                Picker(selection: Binding(get: { dev.callHandle }, set: { dev.setCallHandle($0) })) {
                    Text("Generic [Call handle]").tag(CallHandleKind.generic)
                    Text("Email [Call handle]").tag(CallHandleKind.email)
                } label: {
                    Text("Call Handle").lineLimit(1)
                }
                .accessibilityIdentifier("dev-mode-call-handle")
                #endif
            }
        } footer: {
            Text("When this is on, this device records logs, errors and energy use so you can export them for an Agent to look into. The data stays on this device; turning this off deletes it.")
        }
        .task(id: dev.isOn) { await dev.refresh() }
        .alert(String(localized: "Turn off and delete the \(Self.bytes(dev.size ?? 0)) recorded?"), isPresented: $confirmOff) {
            Button(String(localized: "Export First…")) { model.settingsPath.append(SettingsPage.devExport) }
            Button(String(localized: "Turn Off and Delete"), role: .destructive) { dev.setOn(false) }
            Button(String(localized: "Cancel"), role: .cancel) {}
        }
    }

    private func switched(_ on: Bool) {
        if on || (dev.size ?? 0) == 0 { dev.setOn(on) } else { confirmOff = true }
    }

    private var recorded: String {
        let since = dev.since?.formatted(.dateTime.month(.abbreviated).day().hour().minute()) ?? "–"
        guard let size = dev.size else { return String(localized: "Since \(since)") }
        return String(localized: "Since \(since) · \(Self.bytes(size))")
    }

    static func bytes(_ n: Int64) -> String { n.formatted(.byteCount(style: .file)) }
}

/// Export Diagnostics (Settings › About › Developing Persona): what the package holds, which of it to leave out, and
/// where it goes: Send to Agent (`DevSendSheet`, a chat's box with the zip and a note) or the system's Share sheet.
struct DevExportPage: View {
    @Environment(AppModel.self) private var model
    @State private var picked = Set(DevPackage.Category.allCases)
    /// Which button is building the package.
    @State private var busy: Way?
    #if os(iOS)
    @State private var sharing: DevExportFile?
    @State private var sending: DevExportFile?
    /// The chat Send To picked and the package for it; it goes there once the sheet has gone.
    @State private var target: (ForwardTarget, URL)?
    #endif

    private enum Way { case send, share }
    private let dev = DevMode.shared

    var body: some View {
        SettingsForm {
            Section {
                HStack(spacing: Theme.Space.m) {
                    IconTile(Theme.Icon.fileArchive, tint: Theme.accent)
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(window).font(Theme.Font.heading)
                        Text(details).font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("dev-export-summary")
            }
            Section {
                row(.logs, String(localized: "Logs"), String(localized: "App logs, screens, request errors"))
                row(.errors, String(localized: "Errors and Crashes"), String(localized: "Crashes, hangs, uncaught exceptions"))
                row(.energy, String(localized: "Energy and Performance"), String(localized: "MetricKit daily reports, heat, battery, requests and connections each minute"))
                HStack(spacing: Theme.Space.m) {
                    ThemedIcon(Theme.Icon.locked).foregroundStyle(.secondary)
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Device and App Version")
                        Text("Model, system, app version (always included)").font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                .accessibilityElement(children: .combine)
            } header: {
                Text("Includes")
            } footer: {
                Text("Never included: message text, knowledge base content, keys, tokens, passwords, cookies, pairing links.")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Export Diagnostics"))
        .safeAreaInset(edge: .bottom) { bottomBar }
        .task { await dev.refresh() }
        #if os(iOS)
        .sheet(item: $sharing) { file in
            FileShareSheet(url: file.url) { sharing = nil }
        }
        .sheet(item: $sending, onDismiss: sent) { file in
            DevSendSheet(zip: file.url, size: file.size) { target = ($0, file.url) }
        }
        #endif
    }

    private func row(_ category: DevPackage.Category, _ title: String, _ detail: String) -> some View {
        Button {
            if picked.contains(category) { picked.remove(category) } else { picked.insert(category) }
        } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(picked.contains(category) ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                    .foregroundStyle(picked.contains(category) ? Theme.accent : .secondary)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).foregroundStyle(Color.primary) // not the button's tint
                    Text(detail).font(Theme.Font.caption).foregroundStyle(Color.secondary)
                }
            }
        }
        .accessibilityAddTraits(picked.contains(category) ? .isSelected : [])
        .accessibilityIdentifier("dev-export-\(category.rawValue)")
    }

    private var bottomBar: some View {
        VStack(spacing: Theme.Space.s) {
            #if os(iOS)
            button(.send, String(localized: "Send to Agent…"), icon: Theme.Icon.sendMail, id: "dev-export-send")
                .themedButton(prominent: true)
            button(.share, String(localized: "Share File…"), icon: Theme.Icon.share, id: "dev-export-share")
                .themedButton()
            #else
            button(.share, String(localized: "Share File…"), icon: Theme.Icon.share, id: "dev-export-share")
                .themedButton(prominent: true)
            #endif
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .background(Theme.groupedPage)
    }

    private func button(_ way: Way, _ title: String, icon: String, id: String) -> some View {
        Button { export(way) } label: {
            Group {
                if busy == way {
                    ProgressView()
                } else {
                    Label { Text(verbatim: title).buttonLabelLine() } icon: { ThemedIcon(icon) }
                }
            }
            .frame(maxWidth: .infinity)
        }
        .disabled(busy != nil)
        .accessibilityIdentifier(id)
    }

    private var window: String {
        let since = dev.since?.formatted(.dateTime.month(.abbreviated).day().hour().minute()) ?? "–"
        return String(localized: "\(since) – Now")
    }

    private var details: String {
        let size = dev.size.map(DevModeSection.bytes) ?? "–"
        #if canImport(UIKit)
        let system = "\(UIDevice.current.systemName) \(UIDevice.current.systemVersion)"
        #else
        let system = ProcessInfo.processInfo.operatingSystemVersionString
        #endif
        return String(localized: "About \(size) · zip · \(system)")
    }

    private func export(_ way: Way) {
        busy = way
        let picked = picked
        Task {
            defer { busy = nil }
            do {
                let url = try await dev.exportDiagnostics(picked)
                #if os(iOS)
                let file = DevExportFile(url: url, size: (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init))
                if way == .send { sending = file } else { sharing = file }
                #endif
            } catch {
                model.toast = Toast(String(localized: "Couldn’t export: \(error.localizedDescription)"), style: .error)
            }
        }
    }

    #if os(iOS)
    /// Send To went: the package and the note go to the chat it picked (nothing when it was cancelled).
    private func sent() {
        guard let (to, zip) = target else { return }
        target = nil
        let since = dev.since?.formatted(.dateTime.month(.abbreviated).day().hour().minute()) ?? "–"
        model.attachDiagnostics(zip, note: DevMode.note(since: since), to: to, group: model.hub?.group ?? "")
    }
    #endif
}

#if os(iOS)
private struct DevExportFile: Identifiable {
    let url: URL
    var size: Int64?
    var id: URL { url }
}
#endif

extension View {
    /// Tells Developing Persona this screen is in front while it is shown (#9460); free while the switch is off.
    func devScreen(_ name: String) -> some View { modifier(DevScreen(name: name)) }
}

private struct DevScreen: ViewModifier {
    let name: String
    @State private var token: UUID?

    func body(content: Content) -> some View {
        content
            .onAppear { token = DevMode.shared.enter(name) }
            .onDisappear { if let token { DevMode.shared.leave(token) }; token = nil }
    }
}
