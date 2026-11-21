import Foundation
import UIKit

/// The guided setup's state and actions (#8199): what the hub says of the extension, the computers and this iPhone,
/// turned into steps by IPhoneSetupFlow, and the calls each step makes. The page refreshes it every few seconds while a
/// step waits on something outside the app (the cable, Trust, a restart, a job).
@Observable final class IPhoneSetupModel {
    struct Deps {
        /// Every iPhone the computers reach (`GET /api/v1/iphone`); nil from a hub without the extension.
        var list: () async throws -> IPhoneList?
        /// The computers the app knows (the default group's), with what each said last.
        var machines: () -> [Machine]
        /// One computer's iPhone status, asked now.
        var machineIPhone: (_ machine: String) async throws -> MachineIPhone
        var setExtension: (_ on: Bool) async throws -> Void
        var setComputer: (_ machine: String, _ on: Bool) async throws -> MachineIPhone
        /// pair | install | prepare | cancel on a computer.
        var job: (_ machine: String, _ action: String, _ device: String?) async throws -> MachineIPhone
        var consent: (_ device: String, _ allowed: Bool) async throws -> IPhoneSeen
        var tryIt: (_ device: String) async throws -> IPhoneTryResult
        /// The extension's "Ask on the phone before every action".
        var askEvery: () async throws -> Bool
        var setAskEvery: (_ on: Bool) async throws -> Void
        /// This iPhone's model identifier and iOS version.
        var model: String
        var ios: String
        /// The iPhone picked as this one, and the computer picked, kept on this device.
        var picked: () -> String?
        var pick: (String?) -> Void
        var chosen: () -> String?
        var choose: (String?) -> Void

        static let chosenKey = "iphoneSetup.computer"

        static var live: Deps {
            let consent = IPhoneConsentModel.Deps.live
            return Deps(list: consent.list, machines: {
                AppModel.shared.machines.filter { $0.hubGroup == nil }
            }, machineIPhone: { id in
                try await Self.api().iphone(machine: id)
            }, setExtension: { on in
                _ = try await Self.api().setExtension("iphone", enabled: on)
            }, setComputer: { id, on in
                try await Self.api().setIPhone(machine: id, enabled: on)
            }, job: { id, action, device in
                try await Self.api().iphoneJob(machine: id, action, device: device)
            }, consent: consent.consent, tryIt: { device in
                try await Self.api().tryIPhone(device: device)
            }, askEvery: {
                let detail = try await Self.api().plugin("iphone")
                return detail.config["confirm"]?.bool ?? false
            }, setAskEvery: { on in
                let api = try Self.api()
                var config = try await api.plugin("iphone").config
                config["confirm"] = .bool(on)
                _ = try await api.setExtension("iphone", config: config)
            }, model: consent.model, ios: consent.ios, picked: consent.picked, pick: consent.pick,
            chosen: { UserDefaults.standard.string(forKey: chosenKey) },
            choose: { id in UserDefaults.standard.set(id, forKey: chosenKey) })
        }

        private static func api() throws -> APIClient {
            guard let api = AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
            return api
        }
    }

    /// What a button is doing now, so it shows progress at once and isn't tapped twice.
    enum Busy: Equatable { case turningOn, choosing(String), pairing, preparing, allowing, trying, asking }

    private(set) var list: IPhoneList?
    /// Each computer's iPhone status, asked since the page opened (newer than the machine list's).
    private(set) var fresh: [String: MachineIPhone] = [:]
    private(set) var loaded = false
    private(set) var busy: Busy?
    /// What the last action that failed said, in the hub's words.
    private(set) var error: String?
    private(set) var tried: IPhoneTryResult?
    /// Try It's screenshot, decoded off the main thread.
    private(set) var triedImage: UIImage?
    private(set) var askEvery = false
    private(set) var chosen: String?
    private(set) var picked: String?
    /// The iPhone a pairing was started for from here: never started twice for the same one by itself.
    @ObservationIgnored private var pairStarted: Set<String> = []
    /// Ignore reads that overlap a new Prepare, and the job each retry replaced on its computer.
    @ObservationIgnored private var prepareRevision = 0
    @ObservationIgnored private var replacedPrepare: [String: Date] = [:]
    @ObservationIgnored private let deps: Deps

    /// Reads nothing: the picks kept on this device are read on the first load (docs/apple/performance.md P3).
    init(deps: Deps = .live) {
        self.deps = deps
    }

    // MARK: What the setup knows

    var computers: [IPhoneSetupComputer] {
        deps.machines()
            .filter { $0.os == "darwin" || $0.os == "linux" }
            .map { m in
                IPhoneSetupComputer(id: m.id, name: m.name, mac: m.os == "darwin", online: m.online, iphone: fresh[m.id] ?? m.iphone)
            }
            .sorted { ($0.unusable == nil ? 0 : 1, $0.name) < ($1.unusable == nil ? 0 : 1, $1.name) }
    }

    var match: IPhoneIdentity.Match {
        IPhoneIdentity.match(list?.devices ?? [], model: deps.model, ios: deps.ios, picked: picked)
    }

    var input: IPhoneSetupInput {
        var i = IPhoneSetupInput(extensionOn: list?.on == true, computers: computers, chosen: chosen,
                                 iosMajor: IPhoneSetupFlow.major(deps.ios))
        if case .this(let me, _) = match { i.me = me }
        return i
    }

    var current: IPhoneSetupStep? { IPhoneSetupFlow.current(input) }
    var computer: IPhoneSetupComputer? { IPhoneSetupFlow.computer(input) }
    var device: IPhoneDevice? { IPhoneSetupFlow.device(input) }
    var iosMajor: Int { IPhoneSetupFlow.major(deps.ios) }
    var model: String { deps.model }
    var ios: String { deps.ios }

    /// Whether the page should ask again every few seconds: a step waits on the cable, the phone or a job.
    var polls: Bool {
        guard loaded, let step = current else { return false }
        return IPhoneSetupFlow.state(step, input) == .working || step == .pair || step == .developerMode
    }

    // MARK: Loading

    /// What the hub says now; with `computers`, each usable computer's status asked afresh too (the chosen one only
    /// once one is chosen).
    func load(computers ask: Bool = true) async {
        if !loaded {
            chosen = chosen ?? deps.chosen()
            picked = picked ?? deps.picked()
        }
        do {
            list = try await deps.list()
        } catch {
            self.error = error.localizedDescription
        }
        if ask, list?.on == true {
            let targets = chosen.map { [$0] } ?? self.computers.filter { $0.unusable == nil }.map(\.id)
            for id in targets {
                guard busy != .preparing else { continue }
                let revision = prepareRevision
                if let p = try? await deps.machineIPhone(id), revision == prepareRevision {
                    recordStatus(p, on: id)
                }
            }
        }
        if case .this(let me, _) = match, me.consented, !loaded {
            askEvery = (try? await deps.askEvery()) ?? false
        }
        loaded = true
        await autoPair()
    }

    /// Pairing starts by itself once this iPhone is on the chosen computer's cable (once per iPhone).
    private func autoPair() async {
        guard current == .pair, let c = computer, case .onCable(let d) = IPhoneSetupFlow.pairPhase(input),
              !pairStarted.contains(d.id) else { return }
        pairStarted.insert(d.id)
        await pair(d.id, on: c.id)
    }

    // MARK: Steps

    func turnOn() async {
        await run(.turningOn) {
            try await self.deps.setExtension(true)
            await self.load()
        }
    }

    /// Use this computer: its switch on, and the bridge installed when it isn't there.
    func choose(_ id: String) async {
        await run(.choosing(id)) {
            self.chosen = id
            self.deps.choose(id)
            var p = try await self.deps.setComputer(id, true)
            if !p.installed {
                p = try await self.deps.job(id, "install", nil)
            }
            self.fresh[id] = p
            await self.load()
        }
    }

    func pair(_ device: String, on machine: String) async {
        await run(.pairing) {
            let p = try await self.deps.job(machine, "pair", device)
            self.fresh[machine] = p
            let id = p.job?.device ?? device // the iPhone that paired from here is this one
            self.picked = id
            self.deps.pick(id)
            await self.load(computers: false)
        }
    }

    /// Pair again after a failure.
    func retryPair() async {
        guard let c = computer else { return }
        let onCable = c.iphone?.devices.first { $0.usb && !$0.paired }
        guard let d = onCable ?? device else { return }
        await pair(d.id, on: c.id)
    }

    func cancelPair() async {
        guard let c = computer else { return }
        await run(.pairing) { self.fresh[c.id] = try await self.deps.job(c.id, "cancel", nil) }
    }

    func prepare() async {
        guard busy == nil, let c = computer, let d = device else { return }
        let previous = c.job("prepare", for: d.id)
        guard previous?.running != true else { return }
        prepareRevision += 1
        if let previous, var status = c.iphone {
            replacedPrepare[c.id] = previous.started
            status.job = nil
            fresh[c.id] = status
        }
        await run(.preparing) {
            let p = try await self.deps.job(c.id, "prepare", d.id)
            self.recordStatus(p, on: c.id)
        }
    }

    private func recordStatus(_ p: MachineIPhone, on id: String) {
        if let job = p.job, job.kind == "prepare", let started = job.started {
            if started == replacedPrepare[id] { return }
            // Both dates come from the same computer. A late read must not roll back the newest attempt.
            if let current = fresh[id]?.job, current.kind == "prepare", let currentStart = current.started,
               started < currentStart || (started == currentStart && job.running && !current.running) { return }
        }
        fresh[id] = p
    }

    /// This iPhone's own consent: only for the iPhone it identified as itself.
    func setAllowed(_ allowed: Bool) async {
        guard case .this(let me, _) = match else { return }
        await run(.allowing) {
            _ = try await self.deps.consent(me.id, allowed)
            await self.load(computers: false)
        }
    }

    /// "This One": the user says which of the alike iPhones this is.
    func pick(_ id: String) {
        picked = id
        deps.pick(id)
    }

    func tryIt() async {
        guard let d = device ?? input.me?.device else { return }
        tried = nil
        triedImage = nil
        await run(.trying) {
            let result = try await self.deps.tryIt(d.id)
            let data = result.screenshot.flatMap { Data(base64Encoded: $0.data) }
            // Decoded and shrunk to the row's thumbnail off the main thread, once (performance.md P8).
            self.triedImage = await Task.detached(priority: .userInitiated) {
                data.flatMap(UIImage.init(data:))?.preparingThumbnail(of: CGSize(width: Theme.Size.avatarXL * 2, height: Theme.Size.avatarXL * 4))
            }.value
            self.tried = result
        }
    }

    func setAskEvery(_ on: Bool) async {
        let was = askEvery
        askEvery = on
        await run(.asking) {
            do {
                try await self.deps.setAskEvery(on)
            } catch {
                self.askEvery = was
                throw error
            }
        }
    }

    private func run(_ what: Busy, _ body: @escaping () async throws -> Void) async {
        busy = what
        defer { busy = nil }
        do {
            try await body()
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}
