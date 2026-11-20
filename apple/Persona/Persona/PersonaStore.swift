import Foundation
import ImageIO
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif
import PersonaWire

/// The personas on this phone (docs/apple/persona.md「On the phone」): which one is chosen, what the hub offers, and the packages
/// downloaded, one directory per rev under Application Support/Personas/<id>/<rev>. A package is used only once all of
/// it is there (it is downloaded beside and moved in whole), and the chosen one shows from disk at launch, before
/// the hub answers or without it.
///
/// A hub that keeps the choice (GET /api/v1/looks, #2406) is the one truth of it: its choice is shown, a choice made
/// here goes to it (PUT /api/v1/looks/current) and another device's comes as a `look` push (`receive`); the saved id is
/// then this device's copy of the hub's. With an older hub (404) the choice stays this device's, as before.
@MainActor @Observable
final class PersonaStore {
    /// Mochi first, then valid packages the hub has listed (kept across launches).
    private(set) var offered: [Persona] = Persona.builtins
    /// The chosen one as it can be shown now: the built-in until its package is on the phone.
    private(set) var current: Persona = .builtin
    /// Ids being downloaded.
    private(set) var downloading: Set<String> = []
    /// Why the last list or download failed (Settings › Avatar shows it).
    private(set) var error: String?
    /// Bumped when pictures land on disk (a package, a thumbnail): the views that asked for one before ask again.
    private(set) var pictures = 0

    /// `current` changed (AppModel lays the colors over the style).
    @ObservationIgnored var onChange: (() -> Void)?
    @ObservationIgnored let root: URL
    @ObservationIgnored private let defaults: UserDefaults
    /// Only the active visit owns selection state. A URL can become current again while its old requests are returning.
    struct Mark: Equatable {
        fileprivate let generation: Int
        fileprivate let sequence: Int
    }

    /// One accepted upload or delete. Its selection is reconciled once, even when its files need a retry.
    @MainActor final class SelectionReceipt {
        let started: Mark
        fileprivate let id: String
        fileprivate let readsAtAnswer: Int
        fileprivate var reconciled = false

        fileprivate init(_ id: String, started: Mark, readsAtAnswer: Int) {
            self.id = id
            self.started = started
            self.readsAtAnswer = readsAtAnswer
        }
    }

    private struct Tap {
        var persona: Persona
        let mark: Mark
        let api: APIClient?
        let local: Bool
    }

    private typealias Work = Task<Void, Never>
    private struct Visit {
        var keeps: Bool?
        var confirmed: (id: String, heard: Int)?
        var pushes: [String: Int] = [:]
        var desired: Tap?
        var inFlight: (tap: Tap, sent: Mark, work: Work)?
        var queued: Tap?
        var waitingForPreviousVisit: Work?
        var acceptedTap = 0
        var latestAuthoritativeRead = 0
        /// Last selection that can survive a refused optimistic tap, including an older revision still on screen.
        var stable: Persona
    }

    private struct PackageKey: Hashable {
        let id: String
        let rev: String
        init(_ p: Persona) { id = p.id; rev = p.rev }
    }

    nonisolated private struct Listing: Sendable {
        let packages: [PersonaInfo]
        let current: LookCurrent? // nil for an older hub
    }

    /// The pre-hub device preference moves up once across all hubs. A new visit can observe the receipt without
    /// adopting the old hub's response as its own choice.
    private struct Migration {
        let mark: Mark
        let localChange: Int
        let hub: URL
        let answer: Task<Result<LookCurrent, any Error>, Never>
    }

    private enum Event {
        case hubSwitched(URL?)
        case tap(Mark, Persona, APIClient?)
        case tapReady(Mark)
        case push(Mark, chosen: String?, effective: String, APIClient)
        case followConfirmed(Mark, String, APIClient)
        case listRequested(Mark, APIClient, after: Int?)
        case listAnswered(Mark, APIClient, Result<Listing, any Error>)
        case migrationObserved(Mark, Migration, Bool)
        case listChoice(Mark, APIClient, LookCurrent?)
        case listSettled(Mark, APIClient, String?)
        case listFinished(Mark, Int)
        case writeAvailable(Mark)
        case putAnswered(Mark, URL, Result<LookCurrent, any Error>)
        case uploadChosen(SelectionReceipt, APIClient)
        case downloadRequested(Mark, Persona, APIClient)
        case downloadFinished(Mark, Persona, Result<URL, any Error>)
        case thumbnailFinished(Mark, Persona, Data)
    }

    @ObservationIgnored private var generation = 0
    /// The ordering clock, also observed by tests waiting for a push to reach the reducer.
    @ObservationIgnored private(set) var choices = 0
    @ObservationIgnored private(set) var hub: URL?
    @ObservationIgnored private var visit = Visit(stable: .builtin)
    @ObservationIgnored private var localChange = 0
    @ObservationIgnored private var migration: Migration?
    /// Stale replies cannot change a visit, but an already sent PUT can still reach its server.
    @ObservationIgnored private var writers: [URL: (sent: Mark, work: Work)] = [:]
    @ObservationIgnored private var reads = 0
    @ObservationIgnored private var reading: (number: Int, work: Work)?
    @ObservationIgnored private var downloads: [PackageKey: Work] = [:]
    #if DEBUG
    @ObservationIgnored private var launchChoice: String?
    #endif

    @ObservationIgnored private let frameCache = NSCache<NSString, PersonaFrames>()
    @ObservationIgnored private let imageCache = NSCache<NSString, UIImage>()

    static let chosenKey = NativeStateMigration.personaKey
    /// Set: the saved choice is a copy of a hub's that keeps the choice, so it is not moved up to another hub. Not set: it
    /// was chosen on this device (an older app, an older hub, no hub), and it moves up once, with `if_unset`, to the
    /// first hub that keeps the choice (docs/extensions/extensions.md "Avatars").
    static let followsHubKey = "persona.followsHub"
    static var defaultRoot: URL {
        URL.applicationSupportDirectory.appending(path: "Personas", directoryHint: .isDirectory)
    }

    /// The user's choice; kept when its package is gone from the hub, so it comes back with it. With a hub that keeps
    /// the choice, the hub's (its `effective`).
    var chosenID: String {
        if visit.desired == nil, current.isBuiltin, let retained = defaults.string(forKey: Self.localChoiceKey) {
            return PersonaSelection.resolve(retained)
        }
        return PersonaSelection.resolve(NativeStateMigration.readPersonaID(from: defaults))
    }

    private var followsHub: Bool {
        get { defaults.bool(forKey: Self.followsHubKey) }
        set {
            guard newValue != followsHub else { return }
            if newValue { defaults.set(true, forKey: Self.followsHubKey) } else { defaults.removeObject(forKey: Self.followsHubKey) }
        }
    }

    @ObservationIgnored private var listed = false

    private var mark: Mark { Mark(generation: generation, sequence: choices) }

    /// Capture before deferring a push or sending an upload/delete. A return to the same URL is a new visit.
    func mark(for api: APIClient) -> Mark? {
        guard !api.config.isDemo else { return nil }
        if hub == nil { apply(.hubSwitched(api.config.url)) }
        return hub == api.config.url ? mark : nil
    }

    func isCurrent(_ request: Mark) -> Bool { request.generation == generation }

    func switchHub(_ api: APIClient?) {
        apply(.hubSwitched(api.flatMap { $0.config.isDemo ? nil : $0.config.url }))
    }

    /// A legacy hub can stop listing a locally chosen pack. Keep that preference separately while Mochi is on screen.
    private static let localChoiceKey = "persona.localChoice"
    private var localID: String? {
        guard !followsHub else { return nil }
        return (defaults.string(forKey: Self.localChoiceKey) ?? NativeStateMigration.readPersonaID(from: defaults)).map(PersonaSelection.resolve)
    }

    /// Writes the launch id, shown manifest and watch choice from the same displayed revision.
    private func show(_ p: Persona, explicitlyChosen: Bool = false) {
        let changed = current != p
        let cached = NativeStateMigration.readPersonaID(from: defaults)
        let saveID = cached != p.id && (changed || explicitlyChosen || cached != nil)
        if saveID {
            NativeStateMigration.writePersonaID(p.id, defaults: defaults)
            WatchSync.shared.push(persona: p.id)
        }
        if changed || saveID { remember(p) }
        guard changed else { return }
        current = p
        onChange?()
    }


    /// The home screen icon the choice puts on (`Persona.appIcon`): a package's is known from the list before the
    /// package is on the phone, so the icon does not go to the default's and back while it downloads. Nil while the
    /// chosen one is not listed yet; once the hub has answered without it, the built-in's on screen.
    var appIcon: AppIconChoice? {
        if let chosen = offered.first(where: { $0.id == chosenID }) { return chosen.appIcon }
        return listed ? current.appIcon : nil
    }

    init(root: URL = PersonaStore.defaultRoot, defaults: UserDefaults = .standard) {
        self.root = root
        self.defaults = defaults
        _ = NativeStateMigration.defaults(defaults)
        frameCache.countLimit = 6
        if let data = try? Data(contentsOf: indexURL), let list = try? JSONDecoder().decode([PersonaInfo].self, from: data) {
            offered = Persona.builtins + list.filter(Self.supportedPackage).map(Persona.init(info:))
        }
        var saved = PersonaSelection.resolve(NativeStateMigration.readPersonaID(from: defaults))
        #if DEBUG
        // `-openPersona <id>`: start with that one (on the phone already, or downloaded when the list comes).
        if let id = defaults.string(forKey: "openPersona"), !id.isEmpty {
            saved = PersonaSelection.resolve(id)
            launchChoice = saved
        }
        #endif
        if let data = try? Data(contentsOf: shownURL), let info = try? JSONDecoder().decode(PersonaInfo.self, from: data),
           info.id == saved, Self.supportedPackage(info), isOnPhone(Persona(info: info)) {
            current = Persona(info: info)
        } else if let p = offered.first(where: { $0.id == saved }), isOnPhone(p) {
            current = p
        }
        visit.stable = current
    }

    nonisolated private static func supportedPackage(_ info: PersonaInfo) -> Bool {
        info.manifest.id == info.id && info.manifest.problem == nil
    }

    private var indexURL: URL { root.appending(path: "index.json") }
    /// The package on screen, kept beside the list (`remember`).
    private var shownURL: URL { root.appending(path: ".shown.json") }

    /// Keeps the package on screen beside the list: a launch shows it from disk while the hub's choice downloads, also
    /// when the list read meanwhile no longer has it or has a new rev of it (`index.json` is that newer list).
    private func remember(_ p: Persona) {
        guard let m = p.manifest else { try? FileManager.default.removeItem(at: shownURL); return }
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try? JSONEncoder().encode(PersonaInfo(id: p.id, rev: p.rev, bytes: 0, manifest: m)).write(to: shownURL, options: .atomic)
    }

    func directory(_ p: Persona) -> URL { root.appending(path: p.id).appending(path: p.rev, directoryHint: .isDirectory) }

    func isOnPhone(_ p: Persona) -> Bool {
        p.isBuiltin || FileManager.default.fileExists(atPath: directory(p).path(percentEncoded: false))
    }

    // MARK: The hub

    func refresh(_ api: APIClient?, since request: Mark? = nil) async { await read(api, since: request, reload: false) }
    func reload(_ api: APIClient?, since request: Mark? = nil) async { await read(api, since: request, reload: true) }

    private func read(_ api: APIClient?, since request: Mark?, reload: Bool) async {
        guard let api, !api.config.isDemo else { return }
        if request == nil, hub != api.config.url { apply(.hubSwitched(api.config.url)) }
        await apply(.listRequested(request ?? mark, api, after: reload ? reads : nil))?.value
    }

    /// Record a Settings tap before its UI action yields or the current hub can change.
    func tap(_ p: Persona, api: APIClient?) -> Task<Void, Never>? {
        if let api, !api.config.isDemo, mark(for: api) == nil { return nil }
        return apply(.tap(mark, p, api))
    }

    func choose(_ p: Persona, api: APIClient?) async { await tap(p, api: api)?.value }

    func packagesChanged(on api: APIClient) {
        guard let request = mark(for: api) else { return }
        apply(.listRequested(request, api, after: reads))
    }

    /// Record the socket's position in the event stream before another main-actor action can enter.
    @discardableResult
    func receive(_ id: String?, effective: String?, api: APIClient?) -> Task<Void, Never>? {
        guard let api, let request = mark(for: api), let shown = effective ?? id, !shown.isEmpty else { return nil }
        return apply(.push(request, chosen: id.map(PersonaSelection.resolve), effective: PersonaSelection.resolve(shown), api))
    }

    func follow(_ id: String?, effective: String?, api: APIClient?) async {
        await receive(id, effective: effective, api: api)?.value
    }

    func receipt(for id: String, since request: Mark) -> SelectionReceipt {
        SelectionReceipt(id, started: request, readsAtAnswer: reads)
    }

    /// The server already chose this look on upload or delete. A retry uses the same reply to finish its download.
    func adopt(_ receipt: SelectionReceipt, api: APIClient) async -> Bool {
        guard isCurrent(receipt.started) else { return false }
        if !offered.contains(where: { $0.id == receipt.id }) {
            await apply(.listRequested(mark, api, after: reads))?.value
        }
        guard isCurrent(receipt.started), let p = offered.first(where: { $0.id == receipt.id }) else { return false }
        await apply(.uploadChosen(receipt, api))?.value
        return isCurrent(receipt.started) && isOnPhone(p)
    }

    func download(_ p: Persona, _ api: APIClient) async {
        guard let request = mark(for: api) else { return }
        await apply(.downloadRequested(request, p, api))?.value
    }

    /// All changes to the visit, observable properties and launch cache are synchronous. A caller can await the
    /// returned work without holding up another input. Effects send results back with the mark they captured.
    @discardableResult
    private func apply(_ event: Event) -> Work? {
        switch event {
        case .hubSwitched(let url):
            generation += 1
            choices += 1
            hub = url
            visit = Visit(stable: current)
            reading?.work.cancel()
            reading = nil
            for work in downloads.values { work.cancel() }
            downloads = [:]
            downloading = []
            listed = false
            error = nil
            guard let migration else { return nil }
            let request = mark
            return Task {
                let answer = await migration.answer.value
                apply(.migrationObserved(request, migration, (try? answer.get()) != nil))
            }

        case .tap(let source, let p, let api):
            guard isCurrent(source), offered.contains(p) else { return nil }
            let client = api.flatMap { $0.config.isDemo ? nil : $0 }
            guard client != nil || isOnPhone(p) else {
                error = String(localized: "“\(p.name)” isn’t on this phone yet. Connect to the hub, then choose it.")
                return nil
            }
            choices += 1
            let request = mark
            visit.desired = Tap(persona: p, mark: request, api: client, local: api?.config.isDemo != true)
            visit.queued = nil
            error = nil
            if let client, visit.keeps == nil {
                let checking = apply(.listRequested(request, client, after: nil))
                return Task { await checking?.value; await apply(.tapReady(request))?.value }
            }
            return apply(.tapReady(request))

        case .tapReady(let request):
            guard isCurrent(request), var tap = visit.desired, tap.mark == request else { return nil }
            // A list may have replaced the revision while the tap was waiting for a file or the hub's capabilities.
            guard let p = offered.first(where: { $0.id == tap.persona.id }) else {
                visit.desired = nil
                project()
                return nil
            }
            tap.persona = p
            if !isOnPhone(p), let api = tap.api {
                return apply(.downloadRequested(request, p, api))
            }
            if let api = tap.api, visit.keeps ?? followsHub {
                visit.desired = tap
                if !followsHub, defaults.string(forKey: Self.localChoiceKey) == nil {
                    defaults.set(localID ?? Persona.builtinID, forKey: Self.localChoiceKey)
                }
                project(explicitlyChosen: true)
                if let move = migration, move.hub == api.config.url {
                    return Task {
                        let accepted = (try? await move.answer.value.get()) != nil
                        apply(.migrationObserved(request, move, accepted))
                        await apply(.tapReady(request))?.value
                    }
                }
                if let active = visit.inFlight {
                    if active.tap.mark == request { return active.work }
                    visit.queued = tap
                    return active.work
                }
                if let older = writers[api.config.url] {
                    visit.queued = tap
                    if let waiting = visit.waitingForPreviousVisit { return waiting }
                    let waiting = Task {
                        await older.work.value
                        await apply(.writeAvailable(request))?.value
                    }
                    visit.waitingForPreviousVisit = waiting
                    return waiting
                }
                return startPut(tap, api)
            }
            if tap.local {
                localChange = request.sequence
                followsHub = false
                defaults.removeObject(forKey: Self.localChoiceKey)
            }
            visit.confirmed = nil
            visit.desired = nil
            visit.stable = p
            project(explicitlyChosen: true)

        case .push(let request, let chosen, let id, let api):
            guard isCurrent(request) else { return nil }
            let pushed = chosen ?? id
            let olderTap = visit.desired.map { $0.persona.id != pushed && $0.persona.id != id } == true &&
                (visit.inFlight?.tap.persona.id == pushed || visit.confirmed?.id == id)
            choices += 1
            visit.pushes[id] = choices
            visit.pushes[pushed] = choices
            confirm(id)
            visit.keeps = true
            if !olderTap {
                visit.desired = nil
                visit.queued = nil
            }
            let received = mark
            return Task { await apply(.followConfirmed(received, id, api))?.value }

        case .followConfirmed(let received, let id, let api):
            // The choice was recorded synchronously. Derive the display with any intervening tap still in place.
            guard isCurrent(received), visit.confirmed?.heard == received.sequence else { return nil }
            project()
            guard let p = offered.first(where: { $0.id == id }) else {
                return apply(.listRequested(mark, api, after: reads))
            }
            return apply(.downloadRequested(mark, p, api))

        case .listRequested(let request, let api, let after):
            guard isCurrent(request) else { return nil }
            if let after, reads > after { return reading?.work }
            if let reading {
                guard after != nil else { return reading.work }
                return Task {
                    await reading.work.value
                    await apply(.listRequested(request, api, after: after))?.value
                }
            }
            reads += 1
            let number = reads, asked = mark
            let task = Task {
                let answer = await Self.readList(api)
                await apply(.listAnswered(asked, api, answer))?.value
                if case .success(let list) = answer, isCurrent(asked) {
                    for info in list.packages {
                        let p = Persona(info: info)
                        guard isCurrent(asked) else { break }
                        if !isOnPhone(p), !hasThumbnail(p),
                           let file = p.manifest?.avatarFile(points: Self.thumbnailPoints, scale: 3),
                           let data = try? await api.personaFile(p.id, file) {
                            apply(.thumbnailFinished(asked, p, data))
                        }
                    }
                }
                apply(.listFinished(asked, number))
            }
            reading = (number, task)
            return task

        case .listAnswered(let request, let api, let answer):
            guard isCurrent(request) else { return nil }
            switch answer {
            case .failure(let failure):
                if let server = failure as? APIError, server.status == 404 {
                    error = String(localized: "This hub can't host avatars yet. Update the hub and check again.")
                } else if !failure.isCancellation {
                    error = String(localized: "Couldn't load the avatar list: \(failure.localizedDescription)")
                }
            case .success(let list):
                error = nil
                listed = true
                if list.current != nil, let reading { visit.latestAuthoritativeRead = reading.number }
                if list.current != nil || (visit.confirmed?.heard ?? 0) <= request.sequence {
                    visit.keeps = list.current != nil
                    if list.current == nil { visit.confirmed = nil }
                }
                offered = Persona.builtins + list.packages.map(Persona.init(info:))
                try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
                try? JSONEncoder().encode(list.packages).write(to: indexURL, options: .atomic)
                return apply(.listChoice(request, api, list.current))
            }

        case .listChoice(let request, let api, let kept):
            guard isCurrent(request) else { return nil }
            return settle(kept, api: api, since: request)

        case .migrationObserved(let request, let move, let accepted):
            guard isCurrent(request), migration?.mark == move.mark else { return nil }
            migration = nil
            if accepted, localChange == move.localChange { followsHub = true }

        case .listSettled(let request, let api, let id):
            guard isCurrent(request) else { return nil }
            if let id, (visit.confirmed?.heard ?? 0) <= request.sequence, localChange <= request.sequence {
                choices += 1
                confirm(id)
            } else if id == nil, visit.confirmed == nil, visit.desired == nil, localChange <= request.sequence {
                let local = localID ?? chosenID
                if let p = offered.first(where: { $0.id == local }) {
                    if isOnPhone(p) { visit.stable = p; project() }
                    else { return apply(.downloadRequested(mark, p, api)) }
                } else {
                    defaults.set(local, forKey: Self.localChoiceKey)
                    visit.stable = .builtin
                    project()
                }
                return nil
            }
            project()
            if let id = visit.confirmed?.id, let p = offered.first(where: { $0.id == id }) {
                return apply(.downloadRequested(mark, p, api))
            }

        case .listFinished(let request, let number):
            guard isCurrent(request), reading?.number == number else { return nil }
            reading = nil
            prune(keeping: Set(offered.map { "\($0.id)/\($0.rev)" }))

        case .writeAvailable(let request):
            guard isCurrent(request) else { return nil }
            visit.waitingForPreviousVisit = nil
            let queued = visit.queued
            visit.queued = nil
            if let queued { return apply(.tapReady(queued.mark)) }

        case .putAnswered(let request, let url, let answer):
            if writers[url]?.sent == request { writers[url] = nil }
            guard isCurrent(request), let active = visit.inFlight, active.sent == request else { return nil }
            visit.inFlight = nil
            let tap = active.tap
            let ownsIntent = visit.desired?.mark == tap.mark
            if ownsIntent { visit.desired = nil }
            let echoed = (visit.pushes[tap.persona.id] ?? 0) > request.sequence
            switch answer {
            case .success(let kept):
                visit.keeps = true
                visit.acceptedTap = max(visit.acceptedTap, tap.mark.sequence)
                if !echoed, tap.mark.sequence >= localChange {
                    choices += 1
                    confirm(PersonaSelection.resolve(kept.effective ?? kept.id ?? tap.persona.id))
                }
            case .failure(let failure):
                if !failure.isCancellation, !echoed, tap.mark.sequence >= localChange,
                   ownsIntent || visit.desired.map({ !isOnPhone($0.persona) }) == true {
                    error = String(localized: "Couldn’t switch to “\(tap.persona.name)” on the hub: \(failure.localizedDescription)")
                }
            }
            project()
            let queued = visit.queued
            visit.queued = nil
            if let queued, queued.mark == visit.desired?.mark, let api = queued.api { return startPut(queued, api) }

        case .uploadChosen(let receipt, let api):
            let request = receipt.started, id = receipt.id
            guard isCurrent(request), let p = offered.first(where: { $0.id == id }) else { return nil }
            if !receipt.reconciled {
                receipt.reconciled = true
                let replaced = (visit.pushes[id] ?? 0) > request.sequence && visit.confirmed?.id != id
                // A modern list requested after the server replied supplies the current choice even without a push.
                if visit.latestAuthoritativeRead <= receipt.readsAtAnswer, !replaced,
                   visit.acceptedTap <= request.sequence, localChange <= request.sequence {
                    choices += 1
                    if visit.keeps == true { confirm(id) }
                    else { visit.confirmed = (id, choices) }
                    project()
                }
            }
            return apply(.downloadRequested(mark, p, api))

        case .downloadRequested(let request, let p, let api):
            guard isCurrent(request), p.manifest != nil, !isOnPhone(p) else { return nil }
            let key = PackageKey(p)
            if let running = downloads[key] { return running }
            let folder = directory(p).deletingLastPathComponent()
            let part = folder.appending(path: ".\(p.rev).\(UUID().uuidString).part", directoryHint: .isDirectory)
            let task = Task {
                let answer = await Self.fetch(p, api: api, part: part)
                await apply(.downloadFinished(request, p, answer))?.value
            }
            downloads[key] = task
            downloading.insert(p.id)
            return task

        case .downloadFinished(let request, let p, let result):
            // A stale fetch owns only its unique staging directory, never a current visit's destination.
            defer { if case .success(let part) = result { try? FileManager.default.removeItem(at: part) } }
            guard isCurrent(request) else { return nil }
            downloads[PackageKey(p)] = nil
            if !downloads.keys.contains(where: { $0.id == p.id }) { downloading.remove(p.id) }
            let waiting = visit.desired.flatMap { $0.persona.id == p.id ? $0.mark : nil }
            guard offered.first(where: { $0.id == p.id })?.rev == p.rev else {
                return waiting.flatMap { apply(.tapReady($0)) }
            }
            do {
                let part = try result.get()
                if !isOnPhone(p) { try FileManager.default.moveItem(at: part, to: directory(p)) }
                pictures += 1
                if let waiting { return apply(.tapReady(waiting)) }
                if visit.confirmed == nil, visit.desired == nil, (localID ?? chosenID) == p.id { visit.stable = p }
                project()
            } catch let failure {
                if visit.desired?.persona.id == p.id { visit.desired = nil }
                project()
                let relevant = waiting != nil || visit.confirmed?.id == p.id || choices == request.sequence
                if relevant, !failure.isCancellation { error = String(localized: "Couldn’t download “\(p.name)”: \(failure.localizedDescription)") }
            }

        case .thumbnailFinished(let request, let p, let data):
            guard isCurrent(request), offered.contains(p) else { return nil }
            try? FileManager.default.createDirectory(at: thumbnails, withIntermediateDirectories: true)
            try? data.write(to: thumbnail(p), options: .atomic)
            pictures += 1
        }
        return nil
    }

    private func confirm(_ id: String) {
        visit.confirmed = (id, choices)
        followsHub = true
        defaults.removeObject(forKey: Self.localChoiceKey)
    }

    private func project(explicitlyChosen: Bool = false) {
        if let id = visit.confirmed?.id, let p = offered.first(where: { $0.id == id }), isOnPhone(p) { visit.stable = p }
        var shown = visit.stable
        if let desired = visit.desired {
            if let p = offered.first(where: { $0.id == desired.persona.id }), isOnPhone(p) { shown = p }
            else if let active = visit.inFlight { shown = active.tap.persona }
        }
        show(shown, explicitlyChosen: explicitlyChosen)
    }

    private func startPut(_ tap: Tap, _ api: APIClient) -> Work {
        let sent = mark
        let work = Task {
            let answer: Result<LookCurrent, any Error>
            do {
                guard isCurrent(sent) else { throw CancellationError() }
                answer = .success(try await api.chooseLook(tap.persona.id))
            }
            catch { answer = .failure(error) }
            await apply(.putAnswered(sent, api.config.url, answer))?.value
        }
        writers[api.config.url] = (sent, work)
        visit.inFlight = (tap, sent, work)
        return work
    }

    private func settle(_ kept: LookCurrent?, api: APIClient, since request: Mark) -> Work? {
        let effective = kept.map { PersonaSelection.resolve($0.effective ?? $0.id) }
        #if DEBUG
        if let id = launchChoice {
            launchChoice = nil
            if let p = offered.first(where: { $0.id == id }), visit.desired == nil, choices == request.sequence {
                if let effective { confirm(effective) }
                return apply(.tap(mark, p, api))
            }
        }
        #endif
        guard let kept, kept.id == nil, (visit.confirmed?.heard ?? 0) <= request.sequence,
              localChange <= request.sequence, let mine = localID, mine != Persona.builtinID else {
            return apply(.listSettled(request, api, effective))
        }
        if let writer = writers[api.config.url] {
            return Task {
                await writer.work.value
                await apply(.listChoice(request, api, kept))?.value
            }
        }
        let move: Migration
        if let current = migration {
            move = current
        } else {
            let answer = Task<Result<LookCurrent, any Error>, Never> {
                do { return .success(try await api.chooseLook(mine, ifUnset: true)) }
                catch { return .failure(error) }
            }
            move = Migration(mark: request, localChange: localChange, hub: api.config.url, answer: answer)
            migration = move
        }
        return Task {
            let answer = try? await move.answer.value.get()
            apply(.migrationObserved(request, move, answer != nil))
            guard isCurrent(request) else { return }
            if move.mark.generation != request.generation {
                await apply(.listChoice(request, api, kept))?.value
            } else {
                await apply(.listSettled(request, api, answer.map { PersonaSelection.resolve($0.effective ?? $0.id) }))?.value
            }
        }
    }

    @concurrent nonisolated private static func readList(_ api: APIClient) async -> Result<Listing, any Error> {
        do {
            if let looks = try await api.looks() {
                return .success(Listing(packages: looks.packages.filter(supportedPackage), current: looks.current))
            }
            return .success(Listing(packages: try await api.personas().filter(supportedPackage), current: nil))
        } catch { return .failure(error) }
    }

    /// An effect owns a private staging directory; only the reducer can publish the complete revision.
    @concurrent nonisolated private static func fetch(_ p: Persona, api: APIClient, part: URL) async -> Result<URL, any Error> {
        do {
            for file in p.manifest?.files ?? [] {
                let data = try await api.personaFile(p.id, file)
                let dest = part.appending(path: file)
                try FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
                try data.write(to: dest, options: .atomic)
            }
            if let missing = p.manifest?.files.first(where: { !FileManager.default.fileExists(atPath: part.appending(path: $0).path(percentEncoded: false)) }) {
                throw APIError(status: 0, message: String(localized: "Missing \(missing)"))
            }
            return .success(part)
        } catch {
            try? FileManager.default.removeItem(at: part)
            return .failure(error)
        }
    }


    /// Settings' cards show faces 64 pt wide.
    static let thumbnailPoints: CGFloat = 64
    private var thumbnails: URL { root.appending(path: ".thumbnails", directoryHint: .isDirectory) }
    private func thumbnail(_ p: Persona) -> URL { thumbnails.appending(path: "\(p.id)-\(p.rev).png") }
    private func hasThumbnail(_ p: Persona) -> Bool { FileManager.default.fileExists(atPath: thumbnail(p).path(percentEncoded: false)) }

    /// Removes the revs that are neither offered nor on screen; never while a download runs, never a download's .part.
    private func prune(keeping keep: Set<String>) {
        let fm = FileManager.default
        guard downloading.isEmpty, let ids = try? fm.contentsOfDirectory(atPath: root.path(percentEncoded: false)) else { return }
        for t in (try? fm.contentsOfDirectory(atPath: thumbnails.path(percentEncoded: false))) ?? []
        where !keep.contains(where: { t == $0.replacingOccurrences(of: "/", with: "-") + ".png" }) {
            try? fm.removeItem(at: thumbnails.appending(path: t))
        }
        for id in ids where !id.hasPrefix(".") && id != "index.json" {
            let dir = root.appending(path: id)
            for rev in (try? fm.contentsOfDirectory(atPath: dir.path(percentEncoded: false))) ?? [] where !rev.hasPrefix(".") {
                let key = "\(id)/\(rev)"
                if !keep.contains(key), key != "\(current.id)/\(current.rev)", key != "\(visit.stable.id)/\(visit.stable.rev)" {
                    try? fm.removeItem(at: dir.appending(path: rev))
                }
            }
            if ((try? fm.contentsOfDirectory(atPath: dir.path(percentEncoded: false))) ?? []).isEmpty { try? fm.removeItem(at: dir) }
        }
    }

    // MARK: Pictures

    /// The face `points` wide, from the package, or its thumbnail while the package is not on the phone (nil for the
    /// built-in, or when there is neither).
    func avatar(_ p: Persona, points: CGFloat, scale: CGFloat) -> UIImage? {
        _ = pictures
        guard let file = p.manifest?.avatarFile(points: points, scale: scale) else { return nil }
        let url = isOnPhone(p) ? directory(p).appending(path: file) : thumbnail(p)
        let key = url.path(percentEncoded: false) as NSString
        if let img = imageCache.object(forKey: key) { return img }
        guard let raw = UIImage(contentsOfFile: url.path(percentEncoded: false)) else { return nil }
        let img = raw.preparingForDisplay() ?? raw
        imageCache.setObject(img, forKey: key)
        return img
    }

    /// The state's frames, decoded once (a few dozen 144-pixel images) and kept for the states last shown.
    func frames(_ p: Persona, _ state: PersonaState) -> PersonaFrames? {
        _ = pictures
        guard let spec = p.manifest?.dotState(state), isOnPhone(p) else { return nil }
        let url = directory(p).appending(path: spec.file)
        let key = url.path(percentEncoded: false) as NSString
        if let f = frameCache.object(forKey: key) { return f }
        guard let f = PersonaFrames(url: url, fps: spec.fps) else { return nil }
        frameCache.setObject(f, forKey: key)
        return f
    }
}

#if os(iOS)
extension PersonaStore {
    /// A package's dot frames for `states`, read and decoded off the main actor, each file once (the call's pages keep
    /// them: `CallAssistant`); a state whose file is missing is left out. Nothing for a built-in or a package not on the
    /// phone.
    func decodedFrames(_ p: Persona, _ states: [PersonaState]) async -> [PersonaState: PersonaFrames] {
        guard let manifest = p.manifest else { return [:] }
        let folder = directory(p)
        let wanted = states.compactMap { state in
            manifest.dotState(state).map { CallFramesFile(state: state, url: folder.appending(path: $0.file), fps: $0.fps) }
        }
        return await Task.detached(priority: .userInitiated) {
            guard FileManager.default.fileExists(atPath: folder.path(percentEncoded: false)) else { return [:] }
            var byFile: [URL: PersonaFrames] = [:]
            var out: [PersonaState: PersonaFrames] = [:]
            for file in wanted {
                guard let frames = byFile[file.url] ?? PersonaFrames(url: file.url, fps: file.fps) else { continue }
                byFile[file.url] = frames
                out[file.state] = frames
            }
            return out
        }.value
    }

    /// A package's face `points` wide at `scale` (its thumbnail while the package is not on the phone), read and
    /// prepared for display off the main actor, as `avatar` would on the main one. Nil for a built-in or when there is
    /// neither.
    func decodedFace(_ p: Persona, points: CGFloat, scale: CGFloat) async -> UIImage? {
        guard let file = p.manifest?.avatarFile(points: points, scale: scale) else { return nil }
        let folder = directory(p), thumbnail = thumbnail(p)
        return await Task.detached(priority: .userInitiated) {
            let onPhone = FileManager.default.fileExists(atPath: folder.path(percentEncoded: false))
            let url = onPhone ? folder.appending(path: file) : thumbnail
            guard let raw = UIImage(contentsOfFile: url.path(percentEncoded: false)) else { return nil }
            return raw.preparingForDisplay() ?? raw
        }.value
    }
}

/// One state's frames file, for `decodedFrames`.
private nonisolated struct CallFramesFile: Sendable {
    var state: PersonaState
    var url: URL
    var fps: Double?
}
#endif

/// A dot state's frames and the time one loop takes.
final class PersonaFrames: Sendable {
    let images: [UIImage]
    let duration: TimeInterval

    /// From an APNG (ImageIO composes each frame whole) or a still PNG; `fps` when the file has no frame delays.
    nonisolated init?(url: URL, fps: Double?) {
        guard let src = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        let opts = [kCGImageSourceShouldCacheImmediately: true] as CFDictionary
        var images: [UIImage] = []
        var total = 0.0
        for i in 0..<CGImageSourceGetCount(src) {
            guard let cg = CGImageSourceCreateImageAtIndex(src, i, opts) else { continue }
            #if os(iOS) || os(visionOS)
            images.append(UIImage(cgImage: cg))
            #else
            images.append(NSImage(cgImage: cg, size: NSSize(width: cg.width, height: cg.height))) // AppKit's own init: callable off the main actor
            #endif
            let props = CGImageSourceCopyPropertiesAtIndex(src, i, nil) as? [CFString: Any]
            let png = props?[kCGImagePropertyPNGDictionary] as? [CFString: Any]
            let delay = (png?[kCGImagePropertyAPNGUnclampedDelayTime] as? Double) ?? (png?[kCGImagePropertyAPNGDelayTime] as? Double) ?? 0
            total += delay > 0.005 ? delay : 1 / max(1, fps ?? 8)
        }
        guard !images.isEmpty else { return nil }
        self.images = images
        duration = total
    }
}

/// GET /api/v1/looks's `current`, and PUT /api/v1/looks/current's answer.
nonisolated struct LookCurrent: Decodable, Sendable, Equatable {
    /// The one chosen; nil: none was ever chosen on this hub.
    var id: String?
    /// The one to show: the chosen one, or the default while it is not there (its package gone, its extension off).
    var effective: String?
}

/// GET /api/v1/looks: every look the hub offers and the one chosen (docs/protocol/protocol.md; only what the phone uses).
nonisolated struct LookList: Decodable, Sendable {
    struct Look: Decodable, Sendable {
        var id: String
        var rev: String?
        var bytes: Int64?
        /// Nil for the looks the apps draw themselves (`source: builtin`).
        var manifest: PersonaManifest?

        enum CodingKeys: String, CodingKey { case id, rev, bytes, manifest }

        /// A manifest this build cannot read leaves that look out (as an unchecked package is), not the whole list.
        init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            rev = try? c.decodeIfPresent(String.self, forKey: .rev)
            bytes = try? c.decodeIfPresent(Int64.self, forKey: .bytes)
            manifest = try? c.decodeIfPresent(PersonaManifest.self, forKey: .manifest)
        }
    }

    var looks: [Look]
    var current: LookCurrent

    /// The packages among them, as GET /api/v1/personas lists them (the built-in looks are drawn here).
    var packages: [PersonaInfo] {
        looks.compactMap { l in
            guard let m = l.manifest, let rev = l.rev else { return nil }
            return PersonaInfo(id: l.id, rev: rev, bytes: l.bytes ?? 0, manifest: m)
        }
    }
}

nonisolated extension APIClient {
    /// GET /api/v1/looks: the looks and the choice the hub keeps for every device (#3200). Nil from an older hub (404):
    /// it lists its packages at /api/v1/personas, and the choice stays the device's.
    func looks() async throws -> LookList? {
        do {
            return try JSONDecoder().decode(LookList.self, from: try await request("GET", "/api/v1/looks"))
        } catch let e as APIError where e.status == 404 {
            return nil
        }
    }

    /// PUT /api/v1/looks/current: chooses the look for every device (404 when the hub does not list it). `ifUnset`: only
    /// when the hub has no choice yet; otherwise the answer is its choice, unchanged.
    func chooseLook(_ id: String, ifUnset: Bool = false) async throws -> LookCurrent {
        struct Body: Encodable {
            var id: String
            var ifUnset: Bool?
            enum CodingKeys: String, CodingKey {
                case id
                case ifUnset = "if_unset"
            }
        }
        let data = try await request("PUT", "/api/v1/looks/current", body: Body(id: id, ifUnset: ifUnset ? true : nil))
        return (try? JSONDecoder().decode(LookCurrent.self, from: data)) ?? LookCurrent(id: id, effective: id)
    }

    /// GET /api/v1/personas: the packages on the hub that check out (docs/protocol/protocol.md「REST (App → hub)」).
    func personas() async throws -> [PersonaInfo] {
        struct List: Decodable { var personas: [PersonaInfo] }
        return try JSONDecoder().decode(List.self, from: try await request("GET", "/api/v1/personas")).personas
    }

    /// A file of a package (`PersonaManifest.files`), past the system's URL cache: the store keeps it on disk.
    func personaFile(_ id: String, _ file: String) async throws -> Data {
        let path = "/api/v1/personas/\(id)/\(file)"
        if transport != nil || config.isDemo { return try await request("GET", path) }
        var req = URLRequest(url: url(path), timeoutInterval: 60)
        config.authorize(&req)
        req.setValue(LanguageStore.shared.launched, forHTTPHeaderField: "Accept-Language")
        let (data, resp) = try await Self.uncachedSession.data(for: req)
        try CloudflareAccessError.check(resp, body: data, access: config.access)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            throw APIError(status: status, message: APIError.message(in: data) ?? HTTPURLResponse.localizedString(forStatusCode: status))
        }
        return data
    }
}
