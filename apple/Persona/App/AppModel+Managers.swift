import Foundation
import PersonaWire

// Several managers on one hub (docs/assistant/managers.md "Clients"), as the app reads them outside Settings › Assistants:
// each group's roster, read when its hub says hello and on each of its `managers` pushes. Pages that group a list read
// their own (`ManagerRoster.read`); this one is for what is decided outside such a page: whose copy of an extension a
// push is about, the inbox's groups, a computer whose manager is offline, and the current assistant: the manager picked
// in the sidebar's switcher (`AssistantSwitcher`), kept on this device for each hub. The interface calls a manager an
// assistant; the code and the docs keep "manager".

extension AppModel {
    /// The roster of the hub-level group's hub; nil from a hub before managers, or until it is read.
    var managerRoster: ManagerRoster? { managerRosters[hub?.group ?? ""] }

    /// The name beside the Persona face where a page says whose it is (the sidebar's foot, the Mac's status line, the
    /// voice captions): the current assistant's on a hub with several, as the sidebar's head says it (the face stays the
    /// persona's, picked in Appearance); the persona's with one assistant, as before.
    var assistantShownName: String { assistantManager?.name ?? persona.name }

    /// The same for what is said in conversation `key` (a voice call's captions): another assistant's conversation
    /// opened from elsewhere names its own, as the top bar does.
    func assistantShownName(in key: SessionKey) -> String { otherAssistant(of: key)?.name ?? assistantShownName }

    /// Whose page it is, as the Assistant chat's top bar says it (#9973) and the screenshot banner shows it (#12243):
    /// the named manager on a hub with several, else the current assistant (with one too, as the sidebar's head shows
    /// it), else the persona's name, "Persona" only as the last fallback. `assistantShownName` keeps the persona's
    /// name with one assistant (the composer, calls, voice captions).
    var assistantTitleName: String { assistantManager?.name ?? currentAssistant?.name ?? persona.name }

    /// The same for conversation `key`: another assistant's conversation opened from elsewhere names its own.
    func assistantTitleName(in key: SessionKey?) -> String { key.flatMap { otherAssistant(of: $0) }?.name ?? assistantTitleName }

    /// Reads group's roster again. A hub without one (404) has none; one that can't be read now keeps the last.
    func refreshManagerRoster(_ group: String) async {
        guard let api = api(group) else { return }
        let read = managerRosterReads[group, default: 0] + 1
        managerRosterReads[group] = read
        let answer = await ManagerRoster.ask(api)
        guard read == managerRosterReads[group] else { return }
        let wasPick = assistantPick(in: group)
        let roster: ManagerRoster?
        switch answer {
        case .list(let list):
            roster = ManagerRoster(list)
            // The inbox counts go beside the roster, which carries none: a count that moved leaves the roster as it was,
            // and nothing that reads the roster is drawn again.
            let counts = Dictionary(list.managers.compactMap { m in m.inboxNeedsYou.map { (m.id, $0) } }, uniquingKeysWith: { a, _ in a })
            if inboxNeedsYou[group] ?? [:] != counts { inboxNeedsYou[group] = counts }
            if managerRosterUnsupported.contains(group) { managerRosterUnsupported.remove(group) }
            if rostersKept.contains(group) { rostersKept.remove(group) }
        case .absent:
            roster = nil
            if rostersKept.contains(group) { rostersKept.remove(group) }
            if inboxNeedsYou[group] != nil { inboxNeedsYou[group] = nil }
            if !managerRosterUnsupported.contains(group) { managerRosterUnsupported.insert(group) }
        case .unread:
            roster = managerRosters[group]
        }
        guard managerRosters[group] != roster || assistantPick(in: group) != wasPick else {
            // The same roster and pick, yet what the pages list or ask may have changed: the hub answered a roster the
            // last launch kept, or that it has none (a kept pick stopped counting).
            if group == hub?.group {
                followScope()
                #if os(iOS)
                refreshStarred(force: false)
                #endif
            }
            return
        }
        let was = managerRosters[group].map { shownOwner($0, group: group) }
        let wasOffline = group == hub?.group && assistantManagerOffline != nil
        let defaultMoved = managerRosters[group] != nil && roster != nil && managerRosters[group]?.defaultID != roster?.defaultID
        if defaultMoved {
            // What was held as the default's settings is the one before's, and the new default's may be held by its id.
            if let old = managerRosters[group]?.defaultID, let held = assistantByGroup[group] {
                assistantByManager[Self.managerKey(group, old)] = held
            }
            assistantByGroup[group] = roster.flatMap { assistantByManager[Self.managerKey(group, $0.defaultID)] }
        }
        managerRosters[group] = roster
        // A pick of a manager that left the roster goes with it: back to the default, also should it come back. So does
        // one the hub now keeps out of the switcher (the built-in assistant hidden, #9055). A hub without a roster leaves
        // the pick alone: it names nothing there.
        if let roster, let pick = assistantPicks[group], !roster.canPick(pick) {
            assistantPicks[group] = nil
        }
        saveSnapshot()
        // Another manager's page stays shown: Health, the channels and the health launcher follow the default all the same,
        // so its settings are read once in this run (the default's own page, which reads them, doesn't show), and again
        // when another manager becomes the default.
        if assistantPick(in: group) != nil, defaultMoved || defaultAssistantReads.insert(group).inserted {
            readDefaultAssistant(group, api: api)
        }
        guard group == hub?.group else { return }
        // The manager the Persona page showed left the roster (or the hub has one manager again), the default moved, or the
        // shown one is back online: the page reads its manager again. Only the first and a pick kept from the last launch
        // that names a manager only now that the roster is read change the current assistant. Whether it changed goes by
        // who is shown, not by what a request names (`assistantPick`), which turns nil and back as the default moves
        // onto or off the one shown while that one stays.
        let now = roster.map { shownOwner($0, group: group) }
        let back = wasOffline && assistantManagerOffline == nil
        let switched = was != nil ? was != now : assistantPick(in: group) != wasPick
        if defaultMoved || back || switched {
            assistantManagerChanged(switched: switched)
        } else {
            relistAssistant()
            #if os(iOS)
            refreshStarred(force: false)
            #endif
        }
    }

    /// The default manager's Persona settings for group while another assistant is current (`assistantByGroup`), unless
    /// the default moved again or the default became current meanwhile.
    private func readDefaultAssistant(_ group: String, api: APIClient) {
        let expected = managerRosters[group]?.defaultID
        Task {
            guard let st = try? await api.manager(nil).assistant(), managerRosters[group]?.defaultID == expected,
                  assistantPick(in: group) != nil else { return }
            assistantByGroup[group] = st
        }
    }

    /// The manager group's Persona page shows by `roster`.
    private func shownOwner(_ roster: ManagerRoster, group: String) -> String {
        roster.isMulti ? roster.owner(assistantPicks[group]) : roster.defaultID
    }

    /// The current assistant, when another than the default: the switcher's pick while the roster has it installed. nil
    /// for the default, and always with one manager.
    var assistantManagerID: String? { assistantPick(in: hub?.group ?? "") }

    /// The current assistant as the roster has it, for the sidebar's header and its list: the picked manager, else the
    /// default. nil from a hub without a roster, or until it is read.
    var currentAssistant: HubManager? {
        guard let group = hub?.group, let roster = managerRosters[group] else { return nil }
        let id = roster.owner(assistantPicks[group])
        return roster.installed.first { $0.id == id }
    }

    /// Whether the shared sidebar header shows the assistant switcher. This display gate includes a single installed
    /// assistant; no roster or no installed current assistant keeps the old Persona header. Behavior gates (filtering,
    /// badges, extra requests, and manager request parameters) remain controlled by `ManagerRoster.isMulti`.
    var showsAssistantSwitcher: Bool { managerRoster != nil && currentAssistant != nil }

    /// `assistantManagerID` for group's Persona page.
    func assistantPick(in group: String) -> String? {
        guard let pick = assistantPicks[group], !managerRosterUnsupported.contains(group) else { return nil }
        guard let roster = managerRosters[group] else { return pick }
        guard roster.isMulti else { return nil }
        return roster.named(pick)
    }

    /// The manager the Persona page shows, as the roster has it, on a hub with several managers; nil with one.
    var assistantManager: HubManager? {
        guard let roster = managerRoster, roster.isMulti else { return nil }
        let id = roster.owner(assistantManagerID)
        return roster.installed.first { $0.id == id }
    }

    /// The assistant whose conversation `key` is, when that is another than the current one: a conversation opened from
    /// a notification, a link or the inbox, which doesn't switch. By its listing, else by what it was opened for
    /// (`assistantItemManagers`). nil for the current assistant's, the health space's, and with one manager.
    func otherAssistant(of key: SessionKey) -> HubManager? {
        guard let roster = managerRosters[key.group], roster.isMulti else { return nil }
        let manager: String?
        if let s = session(key) {
            guard s.assistant, !s.inSpace else { return nil }
            manager = s.manager
        } else {
            guard let m = assistantItemManagers[key] else { return nil }
            manager = m
        }
        let id = roster.owner(manager)
        guard id != roster.owner(assistantPick(in: key.group)) else { return nil }
        return roster.installed.first { $0.id == id }
    }

    /// That manager while it is offline: its page shows what was read of it in this run, and nothing is sent to it.
    var assistantManagerOffline: HubManager? { assistantManager.flatMap { $0.online ? nil : $0 } }

    /// Whether a thing stamped `manager` is the Persona page's manager's (everything is, with one manager).
    func assistantShows(_ manager: String?) -> Bool { sameManager(manager, assistantManagerID) }

    /// Whether things stamped `a` and `b` are one manager's (everything is, with one manager).
    func sameManager(_ a: String?, _ b: String?) -> Bool {
        guard let roster = managerRoster, roster.isMulti else { return true }
        return roster.owner(a) == roster.owner(b)
    }

    /// The Persona page's manager for its lists; nil with one manager.
    var assistantShown: AssistantLists.Shown? {
        guard let roster = managerRoster, roster.isMulti else { return nil }
        return AssistantLists.Shown(roster: roster, manager: roster.owner(assistantManagerID))
    }

    /// Manager `id` is the current assistant from now on, on this device (nil: the hub's default again, whichever that
    /// becomes): the Persona page's settings, conversations and readiness are that manager's. The pick is the manager
    /// itself, so it stays when another one becomes the default. Nothing for one the roster hasn't installed, or keeps
    /// out of the switcher.
    func selectAssistantManager(_ id: String?) {
        guard let group = hub?.group, let roster = managerRosters[group] else { return }
        if let id, !roster.canPick(id) { return }
        let was = assistantManagerID
        assistantPicks[group] = id
        if assistantManagerID != was { assistantManagerChanged() }
    }

    /// Settings, on the page `settingsPath` names, where the caller has no way of its own to it.
    func showSettings() {
        selectedTab = .settings
        if tabRouter != nil { shellSettings = true }
    }

    /// Whether `api` was made for the Persona page as it is now (its group and manager): what it read lands.
    func showsPage(of api: APIClient) -> Bool { api.group == hub?.group && api.shownManager == assistantManagerID }

    #if DEBUG
    /// `-openAssistantManager <id>`: the Persona page on that manager once the hub's roster is in (screenshots, UI tests).
    func openAssistantManagerWhenListed(_ id: String) {
        selectedTab = .assistant
        Task {
            for _ in 0..<40 where managerRoster?.isMulti != true { try? await Task.sleep(for: .milliseconds(250)) }
            selectAssistantManager(id)
        }
    }
    #endif

    /// `assistantByManager`'s key.
    static func managerKey(_ group: String, _ manager: String) -> String { group + "\u{0}" + manager }

    /// This device's prefs as a page just saved them, for what reads them here (the Mac's banners, the Live
    /// Activities); a prefs read already on its way doesn't put the old ones back.
    func savedNotifyPrefs(_ prefs: NotifyPrefs) {
        notifyPrefsWrites += 1
        LiveActivityController.shared.settings(prefs)
    }

    /// Whose copy a hub-level push is about, as the pages name managers (`ManagerRoster.named`): nil for the default
    /// manager's, else its id. A push without `manager` is the hub's own assistant's, the default until the roster says
    /// otherwise.
    func pushedBy(_ manager: String?) -> String? { ManagerRoster.pushed(by: manager, defaultID: managerRoster?.defaultID) }

    /// The manager a request about an inbox item stamped `manager` names (its details, Done, Mute, a reply): none for the
    /// default's (docs/assistant/managers.md "Grouping"). Until a roster is read, its stamp: the default may have moved,
    /// and naming the default is harmless. The Rules and Muted lists name their stamp too.
    func itemManager(_ manager: String?) -> String? { managerRoster.map { $0.naming(manager) } ?? manager }

    /// The manager the requests of a Persona conversation name (its feed, a reply draft's Send and Don't handle, a plan's
    /// Start or Don't Start, a briefing question's Handled): the conversation's own, none for the default's. Some of those
    /// routes name no conversation, so the hub would take them to the default manager whichever assistant the conversation
    /// is. By its listing, else by what it was opened for (`assistantItemManagers`), else the current assistant, on whose
    /// page it shows.
    func feedManager(of conversation: String, group: String) -> String? {
        feedManager(listed: sessionsNow.first { $0.assistant && $0.group == group && $0.sessionID == conversation },
                    opened: assistantItemManagers.first { $0.key.group == group && $0.key.sessionID == conversation }?.value, group: group)
    }

    /// The same for a page that holds its conversation's session (`listed`; nil while it is not listed) and the manager
    /// it was opened for. With one installed manager, or no roster, none is named (docs/assistant/managers.md "The
    /// current assistant"): there is only the hub's own to ask, as before.
    func feedManager(listed: Session?, opened: String?, group: String) -> String? {
        guard let roster = managerRosters[group], roster.isMulti else { return nil }
        if let listed { return roster.naming(listed.manager) }
        if let opened { return roster.naming(opened) }
        return assistantPick(in: group)
    }

    /// The manager of `machine` while a hub with several managers has that manager offline: the computer comes back
    /// with it, and Reconnect, which goes through it, can't work meanwhile. nil otherwise.
    func offlineManager(of machine: Machine?, group: String) -> HubManager? {
        guard let machine, !machine.online else { return nil }
        return managerRosters[group]?.offlineManager(of: machine.manager)
    }

    /// The manager of conversation `session` while a hub with several managers has it offline: nothing goes to it until
    /// it is back, wherever the conversation was opened. nil otherwise.
    func offlineManager(of session: Session?) -> HubManager? {
        guard let session else { return nil }
        return managerRosters[session.group]?.offlineManager(of: session.manager)
    }

    /// The manager the links opened among things stamped `manager` (a chat's session, an inbox item) are read through
    /// (`ManagerRoster.reading`), in group `group` (nil: the hub-level one). The native readers read through the hub-level
    /// group's hub, so another group's name none.
    func linkManager(_ manager: String?, group: String? = nil) -> String? {
        guard group == nil || group == (hub?.group ?? "") else { return nil }
        return ManagerRoster.reading(manager, roster: managerRoster)
    }
}

/// Where each hub's current assistant is kept on this device (`AppModel.assistantPicks`: group → manager id): the
/// app's defaults, so it outlives the caches; a test's model keeps its own file in its state folder, read through
/// `StateCache` like the model's other state-folder files (#4695). The write stays synchronous: a test reads it back
/// at once, and `StateCache.save` writes later.
nonisolated enum AssistantPickStore {
    static let key = "assistant.manager"
    /// The file's name in a test's state folder, one `StateCache` keeps as it is.
    private static let fileName = "assistant-manager"

    private static func file(_ folder: URL) -> URL { folder.appending(path: fileName + ".json") }

    static func load(folder: URL?) -> [String: String] {
        if let folder {
            return StateCache.load(fileName, as: [String: String].self, in: folder) ?? [:]
        }
        return UserDefaults.standard.dictionary(forKey: key)?.compactMapValues { $0 as? String } ?? [:]
    }

    static func save(_ picks: [String: String], folder: URL?) {
        if let folder {
            try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            try? JSONEncoder().encode(picks).write(to: file(folder), options: .atomic)
        } else if picks.isEmpty {
            UserDefaults.standard.removeObject(forKey: key)
        } else {
            UserDefaults.standard.set(picks, forKey: key)
        }
    }
}

extension ManagerRoster {
    /// `AppModel.pushedBy` with the default manager's id, nil until a roster is read.
    static func pushed(by manager: String?, defaultID: String?) -> String? {
        let id = manager ?? assistant
        return id == (defaultID ?? assistant) ? nil : id
    }

    /// The manager for a storage key. Unlike a raw push, storage's nil already means the default assistant, not the
    /// hub's own assistant, so name that default before applying the push rule.
    static func stored(_ manager: String?, defaultID: String?) -> String? {
        pushed(by: manager ?? defaultID, defaultID: defaultID)
    }

    /// The installed manager a thing stamped `manager` belongs to, when it is offline on a hub with several managers;
    /// nil otherwise (with one manager the pages show none of it).
    func offlineManager(of manager: String?) -> HubManager? {
        guard isMulti else { return nil }
        let id = owner(manager)
        return installed.first { $0.id == id && !$0.online }
    }

    /// The manager a link opened among things stamped `manager` is read through (docs/assistant/managers.md "Clients"): as
    /// `named`, nil for the default's and for one `roster` hasn't installed; before a roster is read, any but the hub's own
    /// assistant.
    static func reading(_ manager: String?, roster: ManagerRoster?) -> String? {
        if let roster { return roster.named(manager) }
        guard let manager, !manager.isEmpty, manager != assistant else { return nil }
        return manager
    }
}
