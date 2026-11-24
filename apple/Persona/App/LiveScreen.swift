import SwiftUI

/// A Mac's live screen, as the core's pages ask for it (#14017): what the screen-share module provides through the
/// registry (`ModuleSlot.capabilities`). Nil when that module is not active (left out of the build, or turned off by
/// the hub): the machine pages then offer no VNC, no One-Tap Unlock and no kept sign-in.
@MainActor protocol LiveScreen: AnyObject {
    /// The Mac's live screen can be shown (the plugin cua or ego is on for it and Screen Sharing answers).
    func canShow(_ ref: MachineRef) -> Bool
    /// VNC for ref: the small window when this device keeps the Mac's sign-in, else the whole screen.
    func openVNC(_ ref: MachineRef, from: SessionKey?)
    /// The whole-screen page; `unlock`: and type the kept password on the lock screen.
    func open(_ ref: MachineRef, unlock: Bool)
    /// The whole-screen page is up (observed: a page that reads it draws again when it closes).
    var isOpen: Bool { get }
    /// The Mac in the small window, nil when none is.
    var smallWindow: MachineRef? { get }
    /// This device keeps the Mac's Screen Sharing sign-in.
    func keepsSignIn(_ ref: MachineRef) -> Bool
    /// One-Tap Unlock is on for the Mac on this device.
    func oneTapUnlock(_ ref: MachineRef) -> Bool
    func setOneTapUnlock(_ on: Bool, for ref: MachineRef)
    /// Face ID (or the passcode) before the kept password is typed; `name`: the Mac's.
    func authenticate(_ name: String) async -> Bool
    /// The button that forgets the kept sign-in; `kept` goes false once it is forgotten.
    func forgetButton(_ ref: MachineRef, kept: Binding<Bool>) -> AnyView
}

extension AppModel {
    /// The live screen when the screen-share module is active.
    var liveScreen: (any LiveScreen)? { Modules.registry.capability((any LiveScreen).self) }
}
