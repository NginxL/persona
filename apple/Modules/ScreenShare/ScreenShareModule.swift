import PersonaModuleAPI
import SwiftUI

/// The `screen-share` module (#14017, part of #13432 and #13430; docs/apple/ios.md "Modules"): a Mac's live screen over
/// VNC or video, its small window, the screen menus, the computer-activity pill, the VNC button under a computer tool,
/// Settings › Live Screen, and the kept sign-ins. The core reaches it only through `Modules.registry`: the views at its
/// places, the settings entry and the `LiveScreen` capability the machine pages ask for.
struct ScreenShareModule: PersonaModule {
    typealias Host = PersonaHostApp

    let descriptor = ModuleDescriptor(id: "screen-share", hubGated: true,
                                      slots: [.launchHooks, .settings, .toolbarItems, .chatAccessories, .chatCardKinds,
                                              .capabilities])

    /// Built into every iPhone and iPad build that compiles the module (apple/modules.yml).
    var isAvailable: Bool { true }

    func register(into registrar: inout ModuleRegistrar<PersonaHostApp>) {
        registrar.provide((any LiveScreen).self, ScreenShareState.shared)
        registrar.onLaunch { ScreenShareState.launch() }
        // A chat's ··· menu and the assistant's top bar: VNC for its Mac, the one operated last, or every Mac (#1343).
        registrar.view(at: .chatMenu) { c in ScreenMenu(machine: c.machine, recent: c.recent, from: c.from) }
        // While the model operates a Mac: with the chat's running work (#1343).
        registrar.view(at: .chatFooter, when: { ComputerActivityPill.shows($0.activity) }) { c in
            ComputerActivityPill(activity: c.activity, from: c.from)
        }
        // VNC under a tool that operates a Mac.
        registrar.view(at: .toolCall, when: { $0.call != nil }) { c in
            if let call = c.call { ScreenEntry(call: call) }
        }
        #if os(iOS)
        registrar.settingsEntry(id: "live-screen",
                                info: SettingsEntry(id: "live-screen", title: String(localized: "Live Screen"), symbol: Theme.Icon.screen,
                                                    group: .device, page: .module("live-screen"),
                                                    aliases: String(localized: "settings.search.live_screen")),
                                row: { AnyView(LiveScreenSettingsRow()) }) {
            LiveScreenSettingsView()
        }
        #endif
    }
}

/// What the live screen holds while the app runs: the whole-screen page asked for and the Mac in the small window.
/// The machine pages read it through `LiveScreen`.
@MainActor @Observable
final class ScreenShareState: LiveScreen {
    static let shared = ScreenShareState()

    /// The live screen asked for (VNC); a window of its own shows it, over whatever is presented (#1604).
    var open: ScreenOpen? { didSet { if open != oldValue { ScreenWindow.shared.show(open) } } }
    /// The Mac whose VNC is in the small window now (picture in picture, #1343).
    var smallWindow: MachineRef?

    var isOpen: Bool { open != nil }

    func canShow(_ ref: MachineRef) -> Bool { AppModel.shared.canShowScreen(ref) }
    func openVNC(_ ref: MachineRef, from: SessionKey?) { AppModel.shared.openVNC(ref, from: from) }
    func open(_ ref: MachineRef, unlock: Bool) { AppModel.shared.openScreen(ref, unlock: unlock) }
    func keepsSignIn(_ ref: MachineRef) -> Bool { ScreenCredentials.load(ref) != nil }
    func oneTapUnlock(_ ref: MachineRef) -> Bool { ScreenUnlock.isOn(ref) }
    func setOneTapUnlock(_ on: Bool, for ref: MachineRef) { ScreenUnlock.set(on, for: ref) }
    func authenticate(_ name: String) async -> Bool { await ScreenUnlock.authenticate(name) }
    func forgetButton(_ ref: MachineRef, kept: Binding<Bool>) -> AnyView { AnyView(ScreenForgetButton(ref: ref, kept: kept)) }

    /// The module's DEBUG launch switches and its runtime-CLI commands (docs/operations/runtime-cli.md).
    static func launch() {
        #if DEBUG
        let d = UserDefaults.standard
        // `-openScreenFresh YES`: no Mac's Screen Sharing sign-in remembered, One-Tap Unlock off (ScreenPasswordTests, #1322).
        if d.bool(forKey: "openScreenFresh") {
            ScreenCredentials.forgetAll()
            ScreenUnlock.forgetAll()
        }
        // `-openScreenDemo YES`: open the live-screen page on a placeholder Mac (no hub needed) for screenshots of the
        // picture switcher and the connection-stats overlay; pair with -showScreenStatsDemo / -showScreenFallbackDemo (#3954).
        if d.bool(forKey: "openScreenDemo") {
            Task { @MainActor in
                try? await Task.sleep(for: .milliseconds(600))
                ScreenShareState.shared.open = ScreenOpen(machine: MachineRef(id: "Studio"))
            }
        }
        #if os(iOS)
        RuntimeCLI.moduleState["vncPiP"] = { Array(VNCPiPLog.lines.suffix(20)) } // the small window's steps (#1839)
        #if canImport(PersonaScreenKit)
        RuntimeCLI.moduleCommands.merge(RuntimeCLI.screenRouteCommands) { core, _ in core }
        #endif
        #endif
        #endif
    }
}

/// The live screen's part of the app model: which Macs can show theirs, and opening it.
extension AppModel {
    /// The live screen asked for (`ScreenShareState.open`).
    var screenOpen: ScreenOpen? {
        get { ScreenShareState.shared.open }
        set { ScreenShareState.shared.open = newValue }
    }

    /// Macs whose live screen can be shown: the plugin cua or ego is on for them and Screen Sharing answers
    /// (docs/extensions/computer-use.md; ego's first-run setup is done over it, #1507).
    var screenMachines: [Machine] { machines.filter(Self.showsScreen) }
    func canShowScreen(_ ref: MachineRef) -> Bool { machine(ref).map(Self.showsScreen) ?? false }
    /// The Mac's displays as its node last read them, from whichever extension shows its screen (#80).
    func screenDisplays(_ ref: MachineRef) -> [MachineDisplay] {
        guard let m = machine(ref) else { return [] }
        if let c = m.computer, c.enabled, c.screenSharing, !c.displays.isEmpty { return c.displays }
        if let b = m.browser, b.enabled, b.screenSharing { return b.displays }
        return []
    }
    /// Whether this Mac's node advertises the live-screen hardware video channel (#3954), from whichever extension shows
    /// its screen. The Picture / Quality switcher appears only then, so no Mac shows a control it cannot use.
    func screenVideoAdvertised(_ ref: MachineRef) -> Bool { machine(ref).map(Self.advertisesVideo) ?? false }
    /// The video channel as the extension showing this Mac's screen reports it (#3954): its version, codecs and whether
    /// Screen Recording is granted for it; nil when no shown extension advertises it.
    func screenVideo(_ ref: MachineRef) -> MachineScreenVideo? {
        guard let m = machine(ref) else { return nil }
        if let c = m.computer, c.enabled, c.screenSharing, let v = c.screenVideo, v.advertised { return v }
        if let b = m.browser, b.enabled, b.screenSharing, let v = b.screenVideo, v.advertised { return v }
        return nil
    }
    /// Pixel scrolling as the extension showing this Mac's screen reports it (#7775): whether its node reads scroll records
    /// and Persona Input may post them; nil when no shown extension's node reads them.
    func screenInput(_ ref: MachineRef) -> MachineScreenInput? {
        guard let m = machine(ref) else { return nil }
        if let c = m.computer, c.enabled, c.screenSharing, let i = c.screenInput, i.reads { return i }
        if let b = m.browser, b.enabled, b.screenSharing, let i = b.screenInput, i.reads { return i }
        return nil
    }
    /// Phone Screen as the extension showing this Mac's screen reports it (#9314); nil when no shown extension's node has it.
    func phoneScreen(_ ref: MachineRef) -> MachinePhoneScreen? {
        guard let m = machine(ref) else { return nil }
        if let c = m.computer, c.enabled, c.screenSharing, let p = c.phoneScreen, p.offered { return p }
        if let b = m.browser, b.enabled, b.screenSharing, let p = b.phoneScreen, p.offered { return p }
        return nil
    }
    nonisolated static func advertisesVideo(_ m: Machine) -> Bool {
        (m.computer.map { $0.enabled && $0.screenSharing && ($0.screenVideo?.advertised ?? false) } ?? false)
            || (m.browser.map { $0.enabled && $0.screenSharing && ($0.screenVideo?.advertised ?? false) } ?? false)
    }
    nonisolated static func showsScreen(_ m: Machine) -> Bool {
        (m.computer.map { $0.enabled && $0.screenSharing } ?? false) || (m.browser.map { $0.enabled && $0.screenSharing } ?? false)
    }
    /// Brings up ref's VNC on the whole screen; `unlock`: and types its kept password on the lock screen (One-Tap Unlock,
    /// #1507 a-2); `from`: the chat it came from (I'm Done hands back to its agent).
    func openScreen(_ ref: MachineRef, unlock: Bool = false, from: SessionKey? = nil) {
        guard api(ref.group) != nil else { toast = Toast(String(localized: "Not connected to the hub [App Model 93d955]"), style: .error); return }
        if VNCPiP.shared.ref == ref { VNCPiP.shared.handOver() }
        screenOpen = ScreenOpen(machine: ref, unlock: unlock, from: from)
    }

    /// The Mac whose VNC is in the small window now (picture in picture, #1343).
    var vncPiP: MachineRef? {
        get { ScreenShareState.shared.smallWindow }
        set { ScreenShareState.shared.smallWindow = newValue }
    }

    /// VNC for ref, from an entry (a machine's page, a tool, the running work, a chat's ··· menu): straight into the
    /// small window when this device keeps the Mac's sign-in, else the whole screen, where it is signed in to.
    func openVNC(_ ref: MachineRef, from: SessionKey? = nil) {
        guard api(ref.group) != nil else { toast = Toast(String(localized: "Not connected to the hub [App Model 93d955]"), style: .error); return }
        let kept = ScreenCredentials.load(ref) != nil
        #if os(iOS)
        VNCPiPLog.add("点了 VNC：支持画中画 \(VNCPiPLog.yes(VNCPiP.supported))，整页开着 \(VNCPiPLog.yes(screenOpen != nil))，记住了登录 \(VNCPiPLog.yes(kept))" // i18n-ignore
                      + (kept ? "" : "（先进整页登录）")) // i18n-ignore
        #endif
        if VNCPiP.supported, screenOpen == nil, kept {
            VNCPiP.shared.start(ref, from: from)
        } else {
            openScreen(ref, from: from)
        }
    }
}
