import Foundation
import Observation

/// One filtered, frozen list window. HTTP work is serialized; changing the query cancels and fences the old work.
/// Delta upserts outside the window do not become rows. Their overlay only prevents a later frozen page resurrecting
/// old values, and is released when the snapshot is exhausted or the query resets.
/// A `legacy` query on a paging hub is a complete copy instead (`complete`): every page of complete rows read once,
/// then each hint reads only what changed since its revision, and new rows join it (#3759).
@MainActor @Observable final class SessionListModel<Row: Decodable & Equatable & Sendable> {
    typealias Page = SessionListPage<Row>
    typealias Fetch = @MainActor (SessionListQuery, String?, String?) async throws -> Page
    private(set) var rows: [Row] = []
    private(set) var counts: SessionListCounts?
    private(set) var total = 0
    private(set) var hasMore = false
    private(set) var loading = false
    private(set) var loaded = false
    private(set) var paged = false
    private(set) var error: String?
    private(set) var query = SessionListQuery()
    /// The rows are the whole collection with a checkpoint (a legacy query on a paging hub), not a window.
    @ObservationIgnored private(set) var complete = false
    @ObservationIgnored private(set) var revision: String?
    @ObservationIgnored private var snapshotRevision: String?
    @ObservationIgnored private var cursor: String?
    @ObservationIgnored private var overlays: [String: Row] = [:]
    @ObservationIgnored private var removed = Set<String>()
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var recoveries = 0
    @ObservationIgnored private var dirty = false
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var taskID: UUID?
    @ObservationIgnored private let refreshes = CoalescedRefresh()
    @ObservationIgnored private var hintTask: Task<Void, Never>?
    @ObservationIgnored private let waitForHints: @MainActor () async throws -> Void
    /// The list's hint reads, one per window (#9487). Zero-length unless the owner passes one (the hub's live lists do,
    /// while no Agents list is in sight).
    @ObservationIgnored private let hintWindow: HintWindow
    @ObservationIgnored private var id: (Row) -> String
    @ObservationIgnored private var removalID: (String) -> String?
    @ObservationIgnored private var fetch: Fetch
    @ObservationIgnored private var fetchCounts: @MainActor (SessionListQuery) async throws -> SessionListCounts
    @ObservationIgnored var changed: (([Row]) -> Void)?
    /// Rows a delta brought that are not in the window (sessions started after it was read, or further down): they
    /// stay out of `rows`, as the window is frozen, but what looks sessions up by id still needs them (#2003: a
    /// to-do's new agent was missing and shown as Follow Up while it worked).
    @ObservationIgnored var outside: (([Row]) -> Void)?

    init(id: @escaping (Row) -> String, removalID: @escaping (String) -> String?,
         fetch: @escaping Fetch, counts: @escaping @MainActor (SessionListQuery) async throws -> SessionListCounts,
         waitForHints: @escaping @MainActor () async throws -> Void = { try await Task.sleep(for: .milliseconds(150)) },
         hintWindow: HintWindow = HintWindow(length: { .zero })) {
        self.id = id
        self.removalID = removalID
        self.fetch = fetch
        self.fetchCounts = counts
        self.waitForHints = waitForHints
        self.hintWindow = hintWindow
    }

    func select(_ query: SessionListQuery) async {
        if self.query != query { reset(query) }
        await load()
    }

    func cancel() { reset(query) }

    /// The list came into sight: a hint read waiting for its window goes now.
    func releaseHintWindow() { hintWindow.release() }

    private func reset(_ query: SessionListQuery) {
        recoveries = 0
        generation += 1
        refreshes.reset()
        hintTask?.cancel()
        hintTask = nil
        hintWindow.reset()
        task?.cancel()
        task = nil
        taskID = nil
        self.query = query
        rows = []
        counts = nil
        loaded = false
        loading = false
        paged = false
        complete = false
        total = 0
        hasMore = false
        revision = nil
        snapshotRevision = nil
        cursor = nil
        overlays = [:]
        removed = []
        error = nil
        dirty = false
    }

    func load() async {
        guard !loaded || task != nil else { return }
        let gen = generation
        await refreshes.refresh { [weak self] in
            await self?.read(generation: gen, refresh: false)
        }
    }

    /// Manual refresh absorbs the queued hint and shares the serialized read without discarding the paging window.
    func refresh() async {
        let gen = generation
        let hadHint = hintTask != nil
        hintTask?.cancel()
        hintTask = nil
        await refreshes.refresh(afterCurrent: hadHint) { [weak self] in
            await self?.read(generation: gen, refresh: true)
        }
    }

    func reload() async {
        reset(query)
        await load()
    }

    func next() async {
        guard loaded, paged, hasMore, task == nil else { return }
        await run(first: false)
    }

    /// Preserve the short hint-merging window, then single-flight with one dirty follow-up. Later hints do not
    /// move the pending deadline. A list with a `hintWindow` reads for hints at most once a window: a hint less than a
    /// window after the last hint read waits for the window's end, and the hints until then share that read (#9487).
    /// A hint for the revision the list already holds (the one every connect sends, a burst's repeat) brings nothing
    /// and starts no read. Manual refreshes, pages and query changes never wait for the window.
    func invalidate(revision hinted: String? = nil) {
        if let hinted, loaded, paged || complete, hinted == revision { return }
        guard hintTask == nil else { return }
        let gen = generation
        hintTask = Task { [weak self] in
            guard let self else { return }
            do {
                try await self.waitForHints()
                try await self.hintWindow.enter()
            } catch { return }
            guard self.generation == gen, !Task.isCancelled else { return }
            self.hintTask = nil
            self.refreshes.invalidate { [weak self] in
                await self?.read(generation: gen, refresh: true)
            }
        }
    }

    private func read(generation gen: Int, refresh: Bool) async {
        // Loading another frozen page is serialized with the refresh, but is not itself an invalidation.
        while let task {
            await task.value
            guard generation == gen, !Task.isCancelled else { return }
            // Let the operation's owner finish cleanup and any 409 recovery before joining its next work.
            // Clearing its task ID here can make that owner skip the recovery tail.
            await Task.yield()
        }
        guard generation == gen, !Task.isCancelled else { return }
        if !loaded { await run(first: true) }
        else if refresh {
            if paged || complete { await synchronizeRead() }
            else { await run(first: true) } // Legacy hubs still return their complete list.
        }
    }

    private func run(first: Bool) async {
        let gen = generation
        let readID = UUID()
        dirty = false
        loading = true
        let work = Task { [weak self] in
            guard let self else { return }
            do {
                var page: Page
                if first { page = try await self.fetch(self.query, nil, nil) }
                else {
                    do { page = try await self.fetch(self.query, self.cursor, nil) }
                    catch let expired as APIError where expired.status == 409 {
                        // The hub keeps a frozen window 5 minutes (fewer when newer ones push it out), so reaching
                        // the end of a list open that long always lands here. Starting over from the first page cut
                        // the list back to 50 rows and the scroll view put the reader near the top; keep the rows and
                        // read a fresh window as deep as they have scrolled, plus a page.
                        try await self.rebaseline(depth: self.rows.count + 1, generation: gen)
                        return
                    }
                }
                try Task.checkCancellation()
                guard self.generation == gen else { return }
                let complete = first && self.query.legacy && page.isPaged
                if complete {
                    // Every frozen page of the same revision, so the copy is complete before `since` follows it.
                    var cursors = Set<String>()
                    while page.hasMore == true {
                        guard let next = page.nextCursor else { throw APIError(status: 0, message: "Missing list cursor") }
                        guard cursors.insert(next).inserted else { throw APIError(status: 0, message: "Repeated list cursor") }
                        let more = try await self.fetch(self.query, next, nil)
                        try Task.checkCancellation()
                        guard self.generation == gen else { return }
                        guard more.revision == page.revision else { throw APIError(status: 409, message: "List snapshot changed") }
                        page.sessions += more.sessions
                        page.hasMore = more.hasMore
                        page.nextCursor = more.nextCursor
                    }
                }
                if first {
                    self.rows = self.unique(page.sessions)
                    self.complete = complete
                    self.paged = page.isPaged && !complete
                    if !self.paged { self.counts = nil }
                    self.snapshotRevision = page.revision
                    self.revision = page.revision
                } else {
                    guard page.revision == self.snapshotRevision else { throw APIError(status: 409, message: "List snapshot changed") }
                    let ids = Set(self.rows.map(self.id))
                    let added = page.sessions.compactMap { row -> Row? in
                        let key = self.id(row)
                        guard !ids.contains(key), !self.removed.contains(key) else { return nil }
                        return self.overlays[key] ?? row
                    }
                    self.rows += self.unique(added)
                }
                if first { self.total = complete ? self.rows.count : page.total ?? self.rows.count }
                self.cursor = page.nextCursor
                self.hasMore = page.hasMore == true && page.nextCursor != nil
                self.loaded = true
                self.error = nil
                self.changed?(self.rows)
                // Render the first page before waiting for aggregates. A counts revision is never a row baseline.
                if self.paged {
                    let counts = try await self.fetchCounts(self.query)
                    try Task.checkCancellation()
                    guard self.generation == gen else { return }
                    self.counts = counts
                }
                if !self.hasMore { self.overlays = [:]; self.removed = [] }
                self.recoveries = 0 // read through: a later expiry (the next hub restart) may rebuild the baseline again
            } catch {
                if self.generation == gen, !Task.isCancelled { self.error = error.localizedDescription }
                if self.generation == gen, (error as? APIError)?.status == 409, self.recoveries < 1 {
                    self.recoveries += 1
                    self.loaded = false
                    self.cursor = nil
                    self.revision = nil
                    self.snapshotRevision = nil
                    self.overlays = [:]
                    self.removed = []
                    self.dirty = true
                }
            }
        }
        task = work
        taskID = readID
        await work.value
        guard generation == gen, taskID == readID else { return }
        task = nil
        taskID = nil
        loading = false
        if dirty {
            dirty = false
            if !loaded { await run(first: true) }
            else if paged || complete { await synchronizeRead() }
        }
    }

    func synchronize() async {
        guard paged || complete else { return }
        await refresh()
    }

    private func synchronizeRead() async {
        guard paged || complete, let since = revision, task == nil else { return }
        let gen = generation
        let readID = UUID()
        dirty = false
        let work = Task { [weak self] in
            guard let self else { return }
            do {
                var next: String?
                var upserts: [String: Row] = [:]
                var removals = Set<String>()
                var target: String?
                var total = self.total
                repeat {
                    let page = try await self.fetch(self.query, next, since)
                    try Task.checkCancellation()
                    guard self.generation == gen else { return }
                    if let target, target != page.revision { throw APIError(status: 409, message: "List delta changed") }
                    target = page.revision
                    for row in page.sessions { upserts[self.id(row)] = row }
                    for token in page.remove ?? [] {
                        guard let key = self.removalID(token) else { throw APIError(status: 0, message: "Invalid list removal") }
                        removals.insert(key)
                    }
                    total = page.total ?? total
                    next = page.hasMore == true ? page.nextCursor : nil
                    if page.hasMore == true && next == nil { throw APIError(status: 0, message: "Missing list cursor") }
                } while next != nil
                // A complete copy counts its own rows; a window's counts come from the hub.
                var counts: SessionListCounts?
                if !self.complete { counts = try await self.fetchCounts(self.query) }
                try Task.checkCancellation()
                guard self.generation == gen else { return }
                let window = Set(self.rows.map(self.id))
                var rows = self.rows.compactMap { row -> Row? in
                    let key = self.id(row)
                    return removals.contains(key) ? nil : upserts[key] ?? row
                }
                let beyond = upserts.filter { !window.contains($0.key) && !removals.contains($0.key) }
                    .sorted { $0.key < $1.key }.map { $0.value }
                if self.complete { rows += beyond } // a new session belongs to the whole collection
                if rows != self.rows { self.rows = rows; self.changed?(rows) }
                if !self.complete, !beyond.isEmpty { self.outside?(beyond) }
                if self.hasMore {
                    for key in removals { self.overlays[key] = nil; self.removed.insert(key) }
                    for (key, row) in upserts where !removals.contains(key) { self.overlays[key] = row; self.removed.remove(key) }
                }
                self.total = self.complete ? rows.count : total
                if !self.complete { self.counts = counts }
                self.revision = target // only the last successful delta page advances the checkpoint
                self.error = nil
                self.recoveries = 0
            } catch {
                guard self.generation == gen, !Task.isCancelled else { return }
                self.error = error.localizedDescription
                if (error as? APIError)?.status == 409, self.recoveries < 1 {
                    self.recoveries += 1
                    self.loaded = false
                    self.overlays = [:]
                    self.removed = []
                    self.dirty = true
                }
            }
        }
        task = work
        taskID = readID
        await work.value
        guard generation == gen, taskID == readID else { return }
        task = nil
        taskID = nil
        if dirty {
            dirty = false
            if !loaded { await run(first: true) } else { await synchronizeRead() }
        }
    }

    /// A fresh frozen window at least `depth` rows deep (or all there is), swapped in at once: the rows on screen stay
    /// until then.
    private func rebaseline(depth: Int, generation gen: Int) async throws {
        let first = try await fetch(query, nil, nil)
        try Task.checkCancellation()
        guard generation == gen else { return }
        var fresh = unique(first.sessions)
        var page = first
        while fresh.count < depth, page.hasMore == true, let next = page.nextCursor {
            page = try await fetch(query, next, nil)
            try Task.checkCancellation()
            guard generation == gen else { return }
            // Not a 409: the recovery below would start over from the first page and shrink the list after all.
            guard page.revision == first.revision else { throw APIError(status: 0, message: "List snapshot changed") }
            let seen = Set(fresh.map(id))
            fresh += unique(page.sessions.filter { !seen.contains(id($0)) })
        }
        rows = fresh
        paged = first.isPaged
        snapshotRevision = first.revision
        revision = first.revision
        total = first.total ?? fresh.count
        cursor = page.nextCursor
        hasMore = page.hasMore == true && page.nextCursor != nil
        overlays = [:]
        removed = []
        loaded = true
        error = nil
        recoveries = 0
        changed?(rows)
        if paged {
            let counts = try await fetchCounts(query)
            try Task.checkCancellation()
            guard generation == gen else { return }
            self.counts = counts
        }
    }

    private func unique(_ rows: [Row]) -> [Row] {
        var seen = Set<String>()
        return rows.filter { seen.insert(id($0)).inserted }
    }
}

extension SessionListModel where Row == Session {
    /// Apply pending local receipts to this window without changing its cursor, revision, row order or counts.
    func keepSeen(where marked: (SessionKey) -> Bool) {
        var visible = rows
        var changed = false
        for i in visible.indices where visible[i].needsSeen() && marked(visible[i].key) {
            visible[i].markLocallySeen()
            changed = true
        }
        if changed { rows = visible }
    }
}
