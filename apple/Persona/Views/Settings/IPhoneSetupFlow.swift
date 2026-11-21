import Foundation

/// The guided setup's steps (#8199, docs/extensions/iphone.md "Guided setup"): what the user does, one screen each, in order.
nonisolated enum IPhoneSetupStep: Int, CaseIterable, Identifiable, Sendable {
    case turnOn, computer, pair, developerMode, allow

    var id: Int { rawValue }
}

/// A step's state, from what the hub reports (never from what was tapped): done, working on it, waiting for the user,
/// or not reached yet.
nonisolated enum IPhoneSetupState: Equatable, Sendable {
    case done, working, needsYou, later
}

/// A computer as the setup offers it: a Mac, or another computer it says can't be used yet.
nonisolated struct IPhoneSetupComputer: Identifiable, Hashable, Sendable {
    /// Why a computer can't be picked: offline, or not a Mac (the developer disk image comes with Xcode, on a Mac only).
    enum Unusable: Hashable, Sendable { case offline, notMac }

    var id: String
    var name: String
    var mac: Bool
    var online: Bool
    /// What the computer says of iPhones; nil while the extension is off.
    var iphone: MachineIPhone?

    var unusable: Unusable? {
        if !mac { return .notMac }
        return online ? nil : .offline
    }
    var turnedOn: Bool { iphone?.enabled == true }
    var helperInstalled: Bool { iphone?.installed == true }
    /// The computer said it has no developer disk image (no Xcode); nil when it didn't say.
    var noXcode: Bool { iphone?.ddiPresent == false }
    func job(_ kind: String, for device: String? = nil) -> ComputerJob? {
        guard let job = iphone?.job, job.kind == kind, device == nil || job.device == device else { return nil }
        return job
    }
}

/// What the setup knows now.
nonisolated struct IPhoneSetupInput: Sendable {
    var extensionOn = false
    /// The computers, Macs first.
    var computers: [IPhoneSetupComputer] = []
    /// The computer the user picked, if any.
    var chosen: String?
    /// This iPhone as the computers list it (on a cable or paired), if they do.
    var me: IPhoneSeen?
    /// This iPhone's iOS major version: below 27 the assistant can look and open apps, not tap or type.
    var iosMajor = 27
}

/// Where the Developer Mode step stands.
nonisolated enum IPhoneSetupDevPhase: Equatable, Sendable {
    /// Developer Mode is off: the user turns it on, the iPhone restarts.
    case turnOn
    /// Connecting to the iPhone (after its restart).
    case connecting
    /// On, but the developer disk image isn't there: Prepare (or the computer does it again by itself).
    case prepare(auto: Bool)
    /// The computer is preparing it.
    case preparing(step: String?)
    /// The last Prepare for this iPhone failed, with the computer's reason when available.
    case prepareFailed(reason: String?, developerModeOn: Bool)
    /// Something else stops it, in the computer's words.
    case problem(String)
    case done
}

/// Where the pairing step stands.
nonisolated enum IPhoneSetupPairPhase: Equatable, Sendable {
    /// No iPhone on the computer's cable yet.
    case waitingForCable
    /// On the cable: pairing starts by itself.
    case onCable(IPhoneDevice)
    /// The pairing job runs; `code` says what it waits for at the phone (trust_pending, locked).
    case pairing(code: String?)
    /// The last pairing failed, with the computer's words.
    case failed(String)
    case done
}

/// The guided setup as pure computation, from IPhoneSetupInput (IPhoneSetupModel feeds it, IPhoneSetupTests check it).
nonisolated enum IPhoneSetupFlow {
    /// The computer the setup works through: the one picked, else one already turned on with the bridge.
    static func computer(_ i: IPhoneSetupInput) -> IPhoneSetupComputer? {
        if let id = i.chosen, let c = i.computers.first(where: { $0.id == id }), c.unusable == nil { return c }
        return i.computers.first { $0.unusable == nil && $0.turnedOn && $0.helperInstalled }
    }

    /// This iPhone as that computer sees it.
    static func device(_ i: IPhoneSetupInput) -> IPhoneDevice? {
        guard let me = i.me, let c = computer(i) else { return nil }
        return c.iphone?.devices.first { $0.id == me.id }
    }

    static func pairPhase(_ i: IPhoneSetupInput) -> IPhoneSetupPairPhase {
        guard let c = computer(i) else { return .waitingForCable }
        if let d = device(i), d.paired { return .done }
        if let job = c.job("pair") {
            if job.running { return .pairing(code: job.code) }
            if job.state == "failed" { return .failed(job.error ?? "") }
        }
        // On the cable and not paired: this iPhone when it was identified, else the only one there.
        let cable = c.iphone?.devices.filter { $0.usb && !$0.paired } ?? []
        if let me = i.me, let d = cable.first(where: { $0.id == me.id }) { return .onCable(d) }
        if i.me == nil, cable.count == 1 { return .onCable(cable[0]) }
        return .waitingForCable
    }

    static func devPhase(_ i: IPhoneSetupInput) -> IPhoneSetupDevPhase {
        guard let d = device(i), d.paired else { return .turnOn }
        let job = computer(i)?.job("prepare", for: d.id)
        if let job, job.running { return .preparing(step: job.step) }
        let developerModeOn = d.developerMode != false && d.code != "developer_mode_off"
        if !d.ready, let job, job.state == "failed" {
            return .prepareFailed(reason: job.error, developerModeOn: developerModeOn)
        }
        if !developerModeOn { return .turnOn }
        if d.code == "ddi_not_mounted" { return .prepare(auto: d.autoPrepare) }
        if d.connected || d.ready { return .done }
        if d.code == nil || d.code == "connecting" { return .connecting }
        return .problem(d.note ?? "")
    }

    static func state(_ step: IPhoneSetupStep, _ i: IPhoneSetupInput) -> IPhoneSetupState {
        switch step {
        case .turnOn:
            return i.extensionOn ? .done : .needsYou
        case .computer:
            guard let c = computer(i) else { return .needsYou }
            if c.job("install")?.running == true { return .working }
            return c.turnedOn && c.helperInstalled ? .done : .needsYou
        case .pair:
            switch pairPhase(i) {
            case .done: return .done
            case .pairing, .onCable: return .working
            case .waitingForCable, .failed: return .needsYou
            }
        case .developerMode:
            switch devPhase(i) {
            case .done: return .done
            case .connecting, .preparing, .prepare(auto: true): return .working
            case .turnOn, .prepare(auto: false), .prepareFailed, .problem: return .needsYou
            }
        case .allow:
            return i.me?.consented == true ? .done : .needsYou
        }
    }

    /// The first step not done; nil when the setup is done.
    static func current(_ i: IPhoneSetupInput) -> IPhoneSetupStep? {
        IPhoneSetupStep.allCases.first { state($0, i) != .done }
    }

    /// Each step as the overview shows it: before the current one and done, done; the current one as it is; the
    /// rest, later.
    static func overview(_ i: IPhoneSetupInput) -> [(IPhoneSetupStep, IPhoneSetupState)] {
        let now = current(i)
        return IPhoneSetupStep.allCases.map { step in
            let s = state(step, i)
            guard let now, s != .done else { return (step, s) }
            return (step, step.rawValue < now.rawValue ? s : (step == now ? s : .later))
        }
    }

    /// How many steps are done.
    static func doneCount(_ i: IPhoneSetupInput) -> Int {
        IPhoneSetupStep.allCases.count(where: { state($0, i) == .done })
    }

    /// The iOS major version in "27.2".
    static func major(_ version: String) -> Int { Int(version.split(separator: ".").first ?? "") ?? 0 }
}
