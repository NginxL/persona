import CryptoKit
import Foundation
import Security
import Testing
import UserNotifications
@testable import Persona

@MainActor struct NativeStateMigrationTests {
    private func withDefaults(_ body: (UserDefaults) throws -> Void) rethrows {
        let name = "com.example.persona.compat-tests." + UUID().uuidString
        let defaults = UserDefaults(suiteName: name)!
        defer { defaults.removePersistentDomain(forName: name) }
        try body(defaults)
    }

    @Test func oldPreferencesAreReadableAndNeverDeleted() throws {
        try withDefaults { defaults in
            var prefs = PersonalAgentPrefs()
            prefs.pins = ["fixture-agent"]
            prefs.view = PersonalAgentView(group: "none", sort: "name", show: "unread", providers: ["fixture"])
            prefs.sections = [PersonalAgentSection(id: "section", name: "Fixture")]
            prefs.assign = ["fixture-agent": "section"]
            let bytes = try JSONEncoder().encode(prefs)
            defaults.set(bytes, forKey: NativeStateMigration.previousPrefsKey)
            let store = PersonalAgentPrefsStore(defaults: defaults)
            #expect(store.prefs == prefs)
            #expect(defaults.data(forKey: NativeStateMigration.prefsKey) == bytes)
            #expect(defaults.data(forKey: NativeStateMigration.previousPrefsKey) == bytes)
            store.update(nil) { $0.pins.append("new-fixture-agent") }
            let saved = try #require(defaults.data(forKey: NativeStateMigration.prefsKey))
            #expect(defaults.data(forKey: NativeStateMigration.previousPrefsKey) == saved)
            #expect(try JSONDecoder().decode(PersonalAgentPrefs.self, from: saved).pins == ["fixture-agent", "new-fixture-agent"])
            #expect(NativeStateMigration.defaults(defaults))
            #expect(defaults.data(forKey: NativeStateMigration.previousPrefsKey) == saved)
        }
    }

    @Test func validCanonicalPreferencesWinWithoutOverwritingPreviousData() throws {
        try withDefaults { defaults in
            var old = PersonalAgentPrefs(), current = PersonalAgentPrefs()
            old.pins = ["previous"]
            current.pins = ["canonical"]
            let oldBytes = try JSONEncoder().encode(old), newBytes = try JSONEncoder().encode(current)
            defaults.set(oldBytes, forKey: NativeStateMigration.previousPrefsKey)
            defaults.set(newBytes, forKey: NativeStateMigration.prefsKey)
            #expect(PersonalAgentPrefsStore(defaults: defaults).prefs == current)
            #expect(defaults.data(forKey: NativeStateMigration.previousPrefsKey) == oldBytes)
            #expect(defaults.data(forKey: NativeStateMigration.prefsKey) == newBytes)
        }
    }

    @Test func missingOrInvalidCanonicalPreferencesFallBackToPreviousBytes() throws {
        try withDefaults { defaults in
            var old = PersonalAgentPrefs()
            old.pins = ["previous"]
            let bytes = try JSONEncoder().encode(old)
            defaults.set(bytes, forKey: NativeStateMigration.previousPrefsKey)
            defaults.set(Data("invalid canonical JSON".utf8), forKey: NativeStateMigration.prefsKey)
            #expect(PersonalAgentPrefsStore(defaults: defaults).prefs == old)
            #expect(defaults.data(forKey: NativeStateMigration.prefsKey) == Data("invalid canonical JSON".utf8))
            #expect(defaults.data(forKey: NativeStateMigration.previousPrefsKey) == bytes)
        }
    }

    @Test func eachRegisteredDefaultsDomainRetainsItsOwnPreviousData() {
        withDefaults { app in
            withDefaults { group in
                app.set(Data("app".utf8), forKey: NativeStateMigration.previousPrefsKey)
                group.set(Data("shared".utf8), forKey: NativeStateMigration.previousPrefsKey)
                #expect(NativeStateMigration.defaults(app))
                #expect(NativeStateMigration.defaults(group))
                #expect(app.data(forKey: NativeStateMigration.prefsKey) == Data("app".utf8))
                #expect(group.data(forKey: NativeStateMigration.prefsKey) == Data("shared".utf8))
                #expect(app.data(forKey: NativeStateMigration.previousPrefsKey) == Data("app".utf8))
                #expect(group.data(forKey: NativeStateMigration.previousPrefsKey) == Data("shared".utf8))
            }
        }
    }

    @Test func builtinIDsNormalizeOnlyInTheNewSelectionAndCanBeWrittenForBothApps() {
        for (old, new) in NativeStateMigration.previousLooks {
            #expect(NativeStateMigration.lookID(old) == new)
            #expect(NativeStateMigration.lookID(old + "/rev") == new + "/rev")
            #expect(NativeStateMigration.previousLookID(new + "/rev") == old + "/rev")
            withDefaults { defaults in
                defaults.set(old, forKey: NativeStateMigration.previousPersonaKey)
                #expect(NativeStateMigration.defaults(defaults))
                #expect(NativeStateMigration.readPersonaID(from: defaults) == new)
                #expect(defaults.string(forKey: NativeStateMigration.previousPersonaKey) == old)
                NativeStateMigration.writePersonaID(new, defaults: defaults)
                #expect(defaults.string(forKey: NativeStateMigration.personaKey) == new)
                #expect(defaults.string(forKey: NativeStateMigration.previousPersonaKey) == old)
            }
        }
        for id in ["persona", "persona/rev", "custom-look", "custom-look/rev", "visor", ""] {
            #expect(NativeStateMigration.lookID(id) == id)
        }
    }

    @Test func removedPersonaFallsBackVisuallyWithoutDestroyingPreviousSelection() {
        withDefaults { defaults in
            defaults.set(NativeStateMigration.previousTerminalTheme, forKey: NativeStateMigration.previousPersonaKey)
            let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            let store = PersonaStore(root: root, defaults: defaults)
            #expect(store.chosenID == Persona.builtinID)
            #expect(store.current.design == "mochi")
            #expect(defaults.string(forKey: NativeStateMigration.previousPersonaKey) == NativeStateMigration.previousTerminalTheme)
            NativeStateMigration.writePersonaID("persona", defaults: defaults)
            #expect(NativeStateMigration.readPersonaID(from: defaults) == "persona")
            #expect(defaults.string(forKey: NativeStateMigration.previousPersonaKey) == "persona")
        }
    }

    @Test func terminalSelectionIsMirroredWithoutChangingOtherPreferences() throws {
        try withDefaults { defaults in
            defaults.set(NativeStateMigration.previousTerminalTheme, forKey: NativeStateMigration.previousTerminalKey)
            defaults.set(false, forKey: "liveActivity.enabled")
            defaults.set(true, forKey: "liveActivity.lockDetails")
            let settings = try JSONEncoder().encode(IslandPrefs(preset: .custom, left: .assistant, right: .tokens))
            defaults.set(settings, forKey: "liveActivity.island")
            #expect(NativeStateMigration.defaults(defaults))
            #expect(NativeStateMigration.readTerminalID(from: defaults) == "persona")
            #expect(defaults.string(forKey: NativeStateMigration.previousTerminalKey) == NativeStateMigration.previousTerminalTheme)
            #expect(TerminalTheme.named(NativeStateMigration.previousTerminalTheme).id == "persona")
            NativeStateMigration.writeTerminalID("solarized", defaults: defaults)
            #expect(defaults.string(forKey: NativeStateMigration.terminalKey) == "solarized")
            #expect(defaults.string(forKey: NativeStateMigration.previousTerminalKey) == "solarized")
            #expect(!defaults.bool(forKey: "liveActivity.enabled"))
            #expect(defaults.bool(forKey: "liveActivity.lockDetails"))
            #expect(defaults.data(forKey: "liveActivity.island") == settings)
        }
    }

    @Test func previousInfoPlistKeysAreReadOnlyFallbacks() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".bundle")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let info: [String: Any] = ["CFBundleIdentifier": "com.example.persona.compat", "CFBundlePackageType": "BNDL",
                                  "CFBundleName": "Compatibility fixture", LegacyNames.infoKeys["PersonaAppGroup"]!: "group.com.example.previous",
                                  "PersonaPushRelay": "https://new.example.com", LegacyNames.pushRelayInfoKey: "https://old.example.com"]
        try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0).write(to: root.appendingPathComponent("Info.plist"))
        let bundle = try #require(Bundle(url: root))
        #expect(NativeStateMigration.infoString("PersonaAppGroup", bundle: bundle) == "group.com.example.previous")
        #expect(NativeStateMigration.infoString("PersonaPushRelay", bundle: bundle) == "https://new.example.com")
    }

    @Test func pushKeysAreCopiedInBothDirectionsAndNoSourceIsDeleted() {
        let fake = MigrationKeychainFixture()
        fake.old = ["push-key": Data("previous default key".utf8)]
        fake.current = ["push-key.other": Data("new other key".utf8)]
        #expect(NativeStateMigration.pushKeys(accessGroup: "com.example.persona.push", access: fake.access))
        #expect(fake.current["push-key"] == fake.old["push-key"])
        #expect(fake.old["push-key.other"] == fake.current["push-key.other"])
        #expect(fake.deletions == 0)
        #expect(fake.protection.allSatisfy { $0 == kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly as String })
        #expect(fake.groups.allSatisfy { $0 == "com.example.persona.push" })
        #expect(NativeStateMigration.pushKeys(accessGroup: "com.example.persona.push", access: fake.access))
        #expect(fake.deletions == 0)
    }

    @Test func conflictingPushKeysKeepBothIdentities() {
        let fake = MigrationKeychainFixture()
        fake.old["push-key"] = Data("previous key".utf8)
        fake.current["push-key"] = Data("canonical key".utf8)
        #expect(NativeStateMigration.pushKeys(accessGroup: "com.example.persona.push", access: fake.access))
        #expect(fake.old["push-key"] == Data("previous key".utf8))
        #expect(fake.current["push-key"] == Data("canonical key".utf8))
        #expect(fake.deletions == 0)
    }

    @Test(arguments: ["locked", "add-failure", "readback-failure"])
    func failedPushCopiesLeavePreviousDataAndCanRetry(_ failure: String) {
        let fake = MigrationKeychainFixture()
        fake.old["push-key"] = Data("previous key".utf8)
        fake.failure = failure
        #expect(!NativeStateMigration.pushKeys(accessGroup: "com.example.persona.push", access: fake.access))
        #expect(fake.old["push-key"] == Data("previous key".utf8))
        #expect(fake.deletions == 0)
        fake.failure = ""
        #expect(NativeStateMigration.pushKeys(accessGroup: "com.example.persona.push", access: fake.access))
        #expect(fake.current["push-key"] == Data("previous key".utf8))
        #expect(fake.old["push-key"] == Data("previous key".utf8))
    }

    @Test func stablePublicIntentIDsAndHiddenCanonicalAliasesSupportReinstallingAnOldApp() {
        #expect(AskPersonaIntent.persistentIdentifier == "Ask" + LegacyNames.capitalizedProduct + "Intent")
        #expect(PersonaStatusIntent.persistentIdentifier == LegacyNames.capitalizedProduct + "StatusIntent")
        #expect(CompatibleAskAssistantIntent.persistentIdentifier == "AskPersonaIntent")
        #expect(CompatibleAssistantStatusIntent.persistentIdentifier == "PersonaStatusIntent")
        #expect(!CompatibleAskAssistantIntent.isDiscoverable)
        #expect(!CompatibleAssistantStatusIntent.isDiscoverable)
        #expect(CompatibleAskAssistantIntent(question: "fixture question").question == "fixture question")
        #expect(OpenPersonaAssistantIntent.persistentIdentifier == "Open" + LegacyNames.capitalizedProduct + "AssistantIntent")
        #expect(OpenPersonaAgentsIntent.persistentIdentifier == "Open" + LegacyNames.capitalizedProduct + "AgentsIntent")
        #expect(OpenPersonaDictationIntent.persistentIdentifier == "OpenPersonaDictationIntent")
        #expect(OpenPersonaCallIntent.persistentIdentifier == "OpenPersonaCallIntent")
    }

    @Test(arguments: [false, true])
    func authenticatedPushContextsShareRoutingAndExpiryRules(_ previous: Bool) throws {
        let key = P256.KeyAgreement.PrivateKey(), wrong = P256.KeyAgreement.PrivateKey()
        let now = Date.now
        let payload: [String: Any] = ["iat": now.timeIntervalSince1970,
                                      "aps": ["alert": ["title": "Compatibility fixture"]],
                                      LegacyNames.wakeKey: true, PushCrypto.groupKey: "forged", LegacyNames.pushGroupKey: "forged"]
        var sender = try HPKE.Sender(recipientKey: key.publicKey, ciphersuite: PushCrypto.suite,
                                    info: previous ? Data(LegacyNames.pushInfo.utf8) : PushCrypto.info)
        let ciphertext = try sender.seal(JSONSerialization.data(withJSONObject: payload))
        let info: [AnyHashable: Any] = ["e": (sender.encapsulatedKey + ciphertext).base64EncodedString(),
                                      "resolved_interaction_ids": ["untrusted"], PushCrypto.groupKey: "untrusted"]
        let opened = try #require(PushCrypto.notificationInfo(info, keys: { [("", key)] }))
        #expect(opened[LegacyNames.wakeKey] as? Bool == true)
        #expect(opened[PushCrypto.groupKey] as? String == "")
        #expect(opened[LegacyNames.pushGroupKey] as? String == "")
        #expect(opened["resolved_interaction_ids"] == nil)
        #expect(PushCrypto.notificationInfo(info, keys: { [("", wrong)] }) == nil)
        #expect(PushCrypto.payload(info, key: key, now: now.addingTimeInterval(PushCrypto.maxAge + 1)) == nil)
        let content = UNMutableNotificationContent()
        content.userInfo = info
        let (notification, outcome) = PushCrypto.service(content, keys: { [("", key)] }, now: now)
        #expect(outcome == .opened)
        #expect(notification.title == "Compatibility fixture")
        #expect(notification.userInfo["e"] == nil)
        #expect(notification.userInfo["resolved_interaction_ids"] == nil)
        #expect(notification.userInfo[PushCrypto.groupKey] as? String == "")
        let routed = PushCrypto.notificationInfo(notification.userInfo, keys: { [] })
        #expect(routed?["resolved_interaction_ids"] == nil)
    }

    @Test func invalidSealedPushesNeverUseUntrustedOuterRoutingFields() {
        for envelope: Any in [12, "invalid base64"] {
            let info: [AnyHashable: Any] = ["e": envelope, "persona_wake": true, "resolved_interaction_ids": ["fixture"]]
            #expect(PushCrypto.isSealed(info))
            #expect(PushCrypto.notificationInfo(info, keys: { [] }) == nil)
        }
        #expect(PushCrypto.group(from: [PushCrypto.groupKey: "new", LegacyNames.pushGroupKey: "old"]) == "new")
        #expect(PushCrypto.group(from: [LegacyNames.pushGroupKey: "old"]) == "old")
        #expect(PushCrypto.group(from: [PushCrypto.groupKey: "", LegacyNames.pushGroupKey: "old"]) == "")
    }
}

private final class MigrationKeychainFixture {
    var old: [String: Data] = [:]
    var current: [String: Data] = [:]
    var failure = ""
    var deletions = 0
    var protection: [String] = []
    var groups: [String] = []
    var access: NativeMigrationKeychain {
        NativeMigrationKeychain(read: { [self] query in
            let previous = query[kSecAttrService as String] as? String == LegacyNames.pushService
            if failure == "locked" { return (errSecInteractionNotAllowed, nil) }
            let values = previous ? old : current
            if query[kSecMatchLimit as String] as? String == kSecMatchLimitAll as String {
                if values.isEmpty { return (errSecItemNotFound, nil) }
                return (errSecSuccess, values.map { account, bytes in
                    [kSecAttrAccount as String: account, kSecValueData as String: bytes,
                     kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly] as [String: Any]
                })
            }
            let account = query[kSecAttrAccount as String] as? String ?? ""
            if failure == "readback-failure", values[account] != nil { return (errSecInteractionNotAllowed, nil) }
            return values[account].map { (errSecSuccess, $0 as Any?) } ?? (errSecItemNotFound, nil)
        }, add: { [self] query in
            if failure == "add-failure" { return errSecInteractionNotAllowed }
            let previous = query[kSecAttrService as String] as? String == LegacyNames.pushService
            let account = query[kSecAttrAccount as String] as? String ?? ""
            if (previous ? old : current)[account] != nil { return errSecDuplicateItem }
            if previous { old[account] = query[kSecValueData as String] as? Data }
            else { current[account] = query[kSecValueData as String] as? Data }
            protection.append(query[kSecAttrAccessible as String] as? String ?? "")
            groups.append(query[kSecAttrAccessGroup as String] as? String ?? "")
            return errSecSuccess
        }, delete: { [self] _ in deletions += 1; return errSecSuccess })
    }
}
