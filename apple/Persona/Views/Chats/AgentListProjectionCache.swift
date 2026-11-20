import Foundation
import Observation

nonisolated private enum AgentSortAdapter {
    static let projection = AgentSortProjectionAdapter()
}

/// One view's bounded projection cache. Only immutable input crosses to the serial worker.
@MainActor @Observable final class AgentListProjectionCache {
    nonisolated struct Partition: Equatable, Sendable {
        var group: String
        var owner: ObjectIdentifier
        var config: ServerConfig
        /// The assistant whose agents the list shows of this group (`AppModel.assistantScope`): another one is another
        /// list, started over at once like another hub's, with nothing of the one before kept behind a hold.
        var scope: AssistantScope?
    }

    nonisolated struct Key: Equatable, Sendable {
        var revision: UInt64
        var partitions: [Partition]
        var settings: AgentListProjection.Settings
        var workspaceRevision: UInt64 = 0
        /// What waiting children ask, so a new card changes the parent row without waiting on a session revision.
        var asks: [SessionKey: String] = [:]
    }

    static func partitions(_ model: AppModel) -> [Partition] {
        let scope = model.assistantScope
        return model.links.map { Partition(group: $0.group, owner: ObjectIdentifier($0), config: $0.config, scope: $0.group == scope?.group ? scope : nil) }
    }

    /// The body observes publication, not the dictionaries or task bookkeeping mutated during lookup.
    private(set) var publication: UInt64 = 0
    @ObservationIgnored private(set) var publishedRevision: UInt64?
    #if DEBUG
    @ObservationIgnored private(set) var shadowValue: (input: AgentListProjection.Input, projection: AgentListProjection, now: Date?)?
    @ObservationIgnored private var shadowNow: [UInt64: Date?] = [:]
    @ObservationIgnored private var shadowNowRevision = Set<UInt64>()
    #endif
    @ObservationIgnored private var value: AgentListProjection?
    @ObservationIgnored private var publishedKey: Key?
    @ObservationIgnored private var requestedKey: Key?
    /// At most one preparation runs; the newest request waits behind it (older waiting ones are dropped), so a stream
    /// of revisions faster than one derivation still publishes, newest last, instead of cancelling every run.
    @ObservationIgnored private(set) var pending: Task<Void, Never>?
    @ObservationIgnored private var queued: (Key, AgentListProjection.Input)?
    @ObservationIgnored private var ready: (Key, AgentListProjection, AgentListProjection.Input, Date?)?
    @ObservationIgnored private var generation: UInt64 = 0
    @ObservationIgnored private let compute: @Sendable (AgentListProjection.Input, Date?) async throws -> AgentListProjection
    private let immediateProjection: AgentSortProjectionAdapter

    init(compute: (@Sendable (AgentListProjection.Input, Date?) async throws -> AgentListProjection)? = nil,
         projection: AgentSortProjectionAdapter = AgentSortAdapter.projection) {
        let worker = Worker(projection: projection)
        self.immediateProjection = projection
        self.compute = compute ?? { input, now in try await worker.build(input, now: now) }
    }

    private func capturedNow(_ key: Key) -> Date? {
        #if DEBUG
        if shadowNowRevision.count > 16, let oldest = shadowNowRevision.min() {
            shadowNowRevision.remove(oldest)
            shadowNow.removeValue(forKey: oldest)
        }
        if !shadowNowRevision.contains(key.revision) {
            shadowNowRevision.insert(key.revision)
            shadowNow[key.revision] = AgentSearchPlan.compile(query: key.settings.query, settings: key.settings).parsed.sinceHours == nil ? nil : Date()
        }
        return shadowNow[key.revision] ?? nil
        #else
        return nil
        #endif
    }

    private func recordShadow(_ input: AgentListProjection.Input, _ projection: AgentListProjection, now: Date?) {
        #if DEBUG
        shadowValue = (input, projection, now)
        #endif
    }

    /// Initial/explicit/urgent changes retain immediate presentation; ordinary feed revisions prepare off-main.
    /// An unchanged retained-tab body neither captures nor rebuilds the inventory.
    /// `valid`: the key's partitions and settings are still the live ones. `canPublish`: nothing holds the list now.
    func resolve(_ key: Key, immediate: Bool,
                 input: () -> AgentListProjection.Input,
                 valid: @escaping @MainActor (Key) -> Bool,
                 canPublish: @escaping @MainActor (Key) -> Bool,
                 willPublish: @escaping @MainActor () -> Void) -> AgentListProjection {
        let _ = publication
        if publishedKey == key, let value { return value }
        if !immediate, let (readyKey, next, captured, now) = ready, newer(readyKey), valid(readyKey), canPublish(readyKey) {
            ready = nil
            willPublish()
            publishedKey = readyKey
            value = next
            publishedRevision = readyKey.revision
            recordShadow(captured, next, now: now)
            if readyKey == key { return next }
        }
        if value == nil || immediate || publishedKey?.partitions != key.partitions || publishedKey?.settings != key.settings ||
            publishedKey?.workspaceRevision != key.workspaceRevision {
            invalidate()
            let captured = input()
            let next = immediateProjection.build(captured, now: capturedNow(key))
            publishedKey = key
            requestedKey = key
            value = next
            recordShadow(captured, next, now: capturedNow(key))
            publishedRevision = key.revision
            return next
        }
        if requestedKey != key {
            requestedKey = key
            if pending == nil {
                start(key, input(), valid: valid, canPublish: canPublish, willPublish: willPublish)
            } else {
                queued = (key, input())
            }
        }
        return value!
    }

    private func newer(_ key: Key) -> Bool { key.revision > publishedKey?.revision ?? 0 }

    private func start(_ key: Key, _ captured: AgentListProjection.Input,
                       valid: @escaping @MainActor (Key) -> Bool,
                       canPublish: @escaping @MainActor (Key) -> Bool,
                       willPublish: @escaping @MainActor () -> Void) {
        let generation = generation
        let compute = compute
        pending = Task { [weak self] in
            let prepared: AgentListProjection?
            do { prepared = try await compute(captured, self?.capturedNow(key)) }
            catch { prepared = nil }
            guard !Task.isCancelled, let self, self.generation == generation else { return }
            self.pending = nil
            // The default worker only throws on cancellation. A failed custom worker must not strand an old snapshot:
            // recover from the same immutable input, behind the same live fences.
            let next = prepared ?? immediateProjection.build(captured, now: capturedNow(key))
            // An older completed revision still lands if nothing newer did: the list converges while revisions stream.
            if self.newer(key), valid(key) {
                if canPublish(key) {
                    self.ready = nil
                    willPublish()
                    self.publishedKey = key
                    self.value = next
                    self.publishedRevision = key.revision
                    self.recordShadow(captured, next, now: capturedNow(key))
                    self.publication &+= 1
                } else if self.ready.map({ key.revision > $0.0.revision }) ?? true {
                    self.ready = (key, next, captured, capturedNow(key))
                }
            }
            if let (nextKey, nextInput) = self.queued {
                self.queued = nil
                self.start(nextKey, nextInput, valid: valid, canPublish: canPublish, willPublish: willPublish)
            }
        }
    }

    /// A hold release asks the body to reconsider a completed value with its current live key/reader state.
    func resume() {
        if ready != nil { publication &+= 1 }
    }

    func invalidate() {
        generation &+= 1
        pending?.cancel()
        pending = nil
        queued = nil
        ready = nil
        requestedKey = nil
        publishedKey = nil
        publishedRevision = nil
        #if DEBUG
        shadowValue = nil
        shadowNow = [:]
        shadowNowRevision = []
        #endif
        value = nil
    }

    private actor Worker {
        let projection: AgentSortProjectionAdapter
        init(projection: AgentSortProjectionAdapter) { self.projection = projection }
        func build(_ input: AgentListProjection.Input, now: Date?) throws -> AgentListProjection {
            try Task.checkCancellation()
            let result = projection.build(input, now: now)
            try Task.checkCancellation()
            return result
        }
    }
}
