import LocalAuthentication
import Security
import SwiftUI
import PersonaWire

/// A Mac's Screen Sharing account and password remembered on this device (#1322, 「在这台手机上记住」), by machine: in
/// the keychain under a service of their own, readable only while the device is unlocked and never leaving it (not in
/// the iCloud keychain, not restored onto another device). From here they go only into the live screen's page
/// (ScreenBridge) when the Mac asks to sign in, never to the hub or a log. One-tap unlock (docs/extensions/computer-use.md
/// 「锁屏、防睡眠、按 App 批准」a-2) reads the same.
nonisolated enum ScreenCredentials {
    struct Saved: Codable, Equatable, Sendable {
        var account: String
        var password: String
    }

    static let service = (Bundle.main.bundleIdentifier ?? "persona").replacingOccurrences(of: ".watchkitapp", with: "") + ".screen-sharing"

    static func key(_ ref: MachineRef) -> String { ref.group.isEmpty ? ref.id : ref.group + "/" + ref.id }

    /// This service's items, or ref's one.
    private static func query(_ ref: MachineRef?) -> [String: Any] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service]
        if let ref { q[kSecAttrAccount as String] = key(ref) }
        return q
    }

    static func load(_ ref: MachineRef) -> Saved? {
        var q = query(ref)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return try? JSONDecoder().decode(Saved.self, from: data)
    }

    @discardableResult
    static func save(_ saved: Saved, for ref: MachineRef) -> Bool {
        guard !saved.account.isEmpty, !saved.password.isEmpty, let data = try? JSONEncoder().encode(saved) else { return false }
        SecItemDelete(query(ref) as CFDictionary)
        var add = query(ref)
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        add[kSecAttrSynchronizable as String] = false
        return SecItemAdd(add as CFDictionary, nil) == errSecSuccess
    }

    static func forget(_ ref: MachineRef) { SecItemDelete(query(ref) as CFDictionary) }

    /// Every Mac's (`-openScreenFresh YES`: the UI tests start with none).
    static func forgetAll() { SecItemDelete(query(nil) as CFDictionary) }
}

/// One-Tap Unlock for a Mac on this device (#1507 a-2): off unless turned on here, and only while its sign-in is kept, which
/// is the Mac account's password the live screen types on its lock screen after Face ID (or the device passcode).
nonisolated enum ScreenUnlock {
    private static let key = "screen.oneTapUnlock"

    static func isOn(_ ref: MachineRef) -> Bool {
        (UserDefaults.standard.stringArray(forKey: key) ?? []).contains(ScreenCredentials.key(ref)) && ScreenCredentials.load(ref) != nil
    }

    static func set(_ on: Bool, for ref: MachineRef) {
        var macs = Set(UserDefaults.standard.stringArray(forKey: key) ?? [])
        if on { macs.insert(ScreenCredentials.key(ref)) } else { macs.remove(ScreenCredentials.key(ref)) }
        UserDefaults.standard.set(macs.sorted(), forKey: key)
    }

    /// Off for every Mac (`-openScreenFresh YES`, with the sign-ins: else one kept from an earlier run comes back on
    /// with the next sign-in).
    static func forgetAll() { UserDefaults.standard.removeObject(forKey: key) }

    /// The owner of this device, by Face ID, Touch ID or the passcode.
    static func authenticate(_ name: String) async -> Bool {
        #if DEBUG
        if UserDefaults.standard.bool(forKey: "showUnlockWithoutAuth") { return true } // UI tests: no Face ID on the simulator
        #endif
        return (try? await LAContext().evaluatePolicy(.deviceOwnerAuthentication, localizedReason: String(localized: "Type the saved password on \(name)'s lock screen"))) ?? false
    }
}

/// Forget Screen Sharing Password, next to VNC while this device remembers the Mac's sign-in: the live screen asks for it again.
struct ScreenForgetButton: View {
    let ref: MachineRef
    @Binding var kept: Bool

    var body: some View {
        Button {
            ScreenCredentials.forget(ref)
            ScreenUnlock.set(false, for: ref)
            kept = false
            Haptics.success()
        } label: {
            Text("Forget Screen Sharing Password").font(Theme.Font.labelStrong).lineLimit(1).minimumScaleFactor(0.8)
        }
        .themedButton()
        .accessibilityIdentifier("screen-forget")
    }
}
