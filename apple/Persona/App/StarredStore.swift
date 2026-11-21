#if os(iOS)
import SwiftUI

/// One current assistant's compact index. Snapshot text is loaded only while the secondary page is open.
@MainActor @Observable final class StarredStore {
    static let shared = StarredStore(deps: .live)

    enum LoadState: Equatable { case idle, loading, ready, unsupported, failed }
    enum Mark: Equatable { case saved, saving }
    enum Original: Equatable { case verified, unavailable, failed, retired }

    private(set) var group: String?
    private(set) var count: Int?
    private(set) var canWrite = false
    private(set) var state = LoadState.idle
    private(set) var snapshots: [StarredSnapshot] = []
    private var refs: [StarredSource: String] = [:]
    private var pending: Set<StarredSource> = []

    @ObservationIgnored private let deps: AssistantDeps
    @ObservationIgnored private let enqueueUndo: (@escaping @MainActor () async -> Void) -> Void
    @ObservationIgnored private var offered = false
    @ObservationIgnored private var manager: String?
    @ObservationIgnored private var defaultManager: String?
    @ObservationIgnored private var authority: APIClient?
    @ObservationIgnored private(set) var scope = 0
    @ObservationIgnored private var indexRead = 0
    @ObservationIgnored private var pageRead = 0
    @ObservationIgnored private var readers = 0

    init(deps: AssistantDeps, enqueueUndo: @escaping (@escaping @MainActor () async -> Void) -> Void = { work in
        Task { await work() }
    }) {
        self.deps = deps
        self.enqueueUndo = enqueueUndo
    }

    @discardableResult
    func configure(group: String?, offered: Bool, manager: String? = nil, defaultManager: String? = nil, reset: Bool = false) -> Bool {
        let next = currentClient(manager: manager)
        let sameAuthority = next?.sameSettingsScope(as: authority) ?? (authority == nil)
        let changed = reset || group != self.group || manager != self.manager || offered != self.offered || !sameAuthority
        if changed {
            scope += 1
            count = nil
            canWrite = false
            snapshots = []
            refs = [:]
            pending = []
            state = offered ? .idle : .unsupported
        }
        self.group = group
        self.offered = offered
        self.manager = manager
        self.defaultManager = defaultManager
        authority = next
        return changed
    }

    func invalidate(manager: String?) {
        if (manager ?? defaultManager) == self.manager { refresh() }
    }

    /// A request whose owner is known stays with that manager even if the hub default moves before it arrives.
    private var api: APIClient? {
        guard let current = currentClient(manager: manager), current.sameSettingsScope(as: authority) else { return nil }
        return current.pinStarredAuthority()
    }

    private func currentClient(manager: String?) -> APIClient? {
        guard let api = deps.api() else { return nil }
        return manager.map { api.manager($0) } ?? api
    }

    private func current(_ owner: Int, _ client: APIClient) -> Bool {
        owner == scope && client.sameSettingsScope(as: currentClient(manager: manager))
    }

    func refresh() {
        guard offered else { return }
        Task { await loadIndex() }
        if readers > 0 { Task { await loadPage() } }
    }

    func startReading() {
        readers += 1
        Task { await loadPage() }
    }

    func stopReading() {
        readers = max(0, readers - 1)
        guard readers == 0 else { return }
        pageRead += 1
        snapshots = []
        state = offered ? .idle : .unsupported
    }

    func loadIndex() async {
        guard offered, let api else { return }
        indexRead += 1
        let read = indexRead, owner = scope
        do {
            let index = try await api.starredIndex()
            guard current(owner, api), read == indexRead else { return }
            count = index.refs.count
            canWrite = index.can_write
            refs = index.refs.reduce(into: [:]) { if $1.source.isVerified { $0[$1.source] = $1.id } }
        } catch {
            guard current(owner, api), read == indexRead else { return }
            if TodosLogic.isOldHub(error) {
                clearUnavailable()
            }
        }
    }

    func loadPage() async {
        guard offered, let api else { state = .unsupported; return }
        pageRead += 1
        let read = pageRead, owner = scope
        if snapshots.isEmpty { state = .loading }
        do {
            let result = try await api.starredList()
            guard current(owner, api), read == pageRead else { return }
            snapshots = result.stars.filter { $0.id != nil }
            state = .ready
        } catch {
            guard current(owner, api), read == pageRead else { return }
            if TodosLogic.isOldHub(error) { clearUnavailable() } else { state = .failed }
        }
    }

    private func clearUnavailable() {
        indexRead += 1
        pageRead += 1
        count = nil
        canWrite = false
        refs = [:]
        snapshots = []
        state = .unsupported
    }

    func mark(_ source: StarredSource, group: String) -> Mark? {
        guard group == self.group, currentClient(manager: manager)?.sameSettingsScope(as: authority) == true else { return nil }
        if pending.contains(source) { return .saving }
        return refs[source] == nil ? nil : .saved
    }

    func isPending(_ source: StarredSource) -> Bool { pending.contains(source) }
    func referenceID(_ source: StarredSource) -> String? { refs[source] }

    func add(_ draft: StarredSnapshot, loadText: (() async throws -> String)? = nil) async {
        let source = draft.source
        guard canWrite, source.isVerified || draft.id != nil, !pending.contains(source), let api else { return }
        let owner = scope
        pending.insert(source)
        defer { if owner == scope { pending.remove(source) } }
        do {
            var input = draft
            if let loadText { input.text = try await loadText() }
            guard current(owner, api) else { return }
            let saved = try await api.addStar(input)
            guard current(owner, api), let id = saved.id else { return }
            indexRead += 1
            pageRead += 1
            if saved.source.isVerified, refs.updateValue(id, forKey: saved.source) == nil { count = count.map { $0 + 1 } }
            if readers > 0, !snapshots.contains(where: { $0.id == id }) { snapshots.insert(saved, at: 0) }
            refresh()
        } catch {
            if current(owner, api) { deps.report(error) }
        }
    }

    func remove(_ source: StarredSource, id: String? = nil) async {
        guard canWrite, !pending.contains(source), let id = id ?? refs[source], let api else { return }
        let owner = scope
        pending.insert(source)
        defer { if owner == scope { pending.remove(source) } }
        do {
            let removed = try await api.removeStar(id)
            guard current(owner, api) else { return }
            indexRead += 1
            pageRead += 1
            if refs[source] == id { refs[source] = nil; count = count.map { max(0, $0 - 1) } }
            snapshots.removeAll { $0.id == id }
            if let removed {
                deps.toast(Toast(String(localized: "Message unstarred"), style: .success, action: .init(title: String(localized: "Undo")) { [weak self] in
                    guard let self, current(owner, api) else { return }
                    enqueueUndo { [weak self] in
                        guard let self, current(owner, api) else { return }
                        await add(removed)
                    }
                }))
            }
            refresh()
        } catch {
            if current(owner, api) { deps.report(error) }
        }
    }

    func verify(_ source: StarredSource, key: SessionKey) async -> Original {
        guard key.group == group, let client = api, let routes = deps.api() else { return .retired }
        let owner = scope
        let result = await Self.readOriginal(source, key: key, api: routes)
        return current(owner, client) ? result : .retired
    }

    static func readOriginal(_ source: StarredSource, key: SessionKey, api: APIClient) async -> Original {
        guard source.isVerified, source.identifies(key) else { return .unavailable }
        do {
            let item = try await api.pinStarredAuthority().fullItem(key, id: source.item)
            return source.matches(item) ? .verified : .unavailable
        } catch {
            guard let error = error as? APIError else { return .failed }
            return error.status == 404 || (error.status == 410 && error.code == "conversation_deleted") ? .unavailable : .failed
        }
    }
}

extension AppModel {
    func refreshStarred(reset: Bool = false, force: Bool = true) {
        let roster = rosterAsked ? managerRoster : nil
        let store = StarredStore.shared
        let knowOwner = rosterAsked || !hubFeatures.contains("sessions_manager")
        let changed = store.configure(group: hub?.group, offered: knowOwner && !ProcessInfo.processInfo.isiOSAppOnMac &&
            hubFeatures.contains(APIClient.starredMessagesFeature), manager: roster?.owner(assistantManagerID) ?? assistantManagerID,
            defaultManager: roster?.defaultID, reset: reset)
        if changed || force { store.refresh() }
    }
}
#endif
