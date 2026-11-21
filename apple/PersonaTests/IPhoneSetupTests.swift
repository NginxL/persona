import Foundation
import PersonaKit
import Testing
@testable import Persona
import PersonaWire

/// The iPhone's guided setup (#8199): each step's state from what the hub says, the order the user goes through them,
/// what the Developer Mode and pairing steps wait for, and what the model calls (pairing starts by itself once this
/// iPhone is on the cable, once; the consent only for the iPhone matched). Made-up values.
@MainActor
struct IPhoneSetupTests {
    private func mac(_ id: String, on: Bool = true, installed: Bool = true, devices: [IPhoneDevice] = [], job: ComputerJob? = nil,
                     ddi: Bool? = true, online: Bool = true) -> IPhoneSetupComputer {
        IPhoneSetupComputer(id: id, name: id, mac: true, online: online,
                            iphone: MachineIPhone(enabled: on, installed: installed, devices: devices, job: job).with(ddi: ddi))
    }

    private func me(_ d: IPhoneDevice, consented: Bool = false) -> IPhoneSeen {
        IPhoneSeen(device: d, machines: [.init(id: "mb", name: "mb", enabled: true)], consented: consented)
    }

    private let cable = IPhoneDevice(id: "dev-1", name: "我的 iPhone", productType: "iPhone18,1", ios: "27.0", paired: false, usb: true)

    private var needsPrepare: IPhoneDevice {
        var phone = cable
        phone.paired = true
        phone.connected = true
        phone.developerMode = true
        phone.code = "ddi_not_mounted"
        return phone
    }

    @Test func stepsFollowTheHub() {
        var i = IPhoneSetupInput()
        #expect(IPhoneSetupFlow.current(i) == .turnOn)
        #expect(IPhoneSetupFlow.state(.turnOn, i) == .needsYou)

        i.extensionOn = true
        i.computers = [IPhoneSetupComputer(id: "srv", name: "srv", mac: false, online: true, iphone: nil), mac("mb", on: false, installed: false)]
        #expect(IPhoneSetupFlow.current(i) == .computer, "a Linux computer is never picked by itself, nor a Mac not turned on")
        #expect(i.computers[0].unusable == .notMac)

        i.chosen = "mb"
        i.computers[1] = mac("mb", installed: false, job: ComputerJob(kind: "install", state: "running"))
        #expect(IPhoneSetupFlow.state(.computer, i) == .working)

        i.computers[1] = mac("mb")
        #expect(IPhoneSetupFlow.current(i) == .pair)
        #expect(IPhoneSetupFlow.pairPhase(i) == .waitingForCable)

        i.computers[1] = mac("mb", devices: [cable])
        #expect(IPhoneSetupFlow.pairPhase(i) == .onCable(cable), "the only iPhone on the cable, before it is identified")
        i.me = me(cable)
        #expect(IPhoneSetupFlow.pairPhase(i) == .onCable(cable))

        i.computers[1] = mac("mb", devices: [cable], job: ComputerJob(kind: "pair", state: "running", code: "trust_pending", device: "dev-1"))
        #expect(IPhoneSetupFlow.pairPhase(i) == .pairing(code: "trust_pending"))
        #expect(IPhoneSetupFlow.state(.pair, i) == .working)

        var paired = cable
        paired.paired = true
        paired.developerMode = false
        paired.code = "developer_mode_off"
        i.computers[1] = mac("mb", devices: [paired])
        i.me = me(paired)
        #expect(IPhoneSetupFlow.current(i) == .developerMode)
        #expect(IPhoneSetupFlow.devPhase(i) == .turnOn)

        paired.developerMode = true
        paired.code = "ddi_not_mounted"
        paired.connected = true
        i.computers[1] = mac("mb", devices: [paired])
        #expect(IPhoneSetupFlow.devPhase(i) == .prepare(auto: false))
        #expect(IPhoneSetupFlow.state(.developerMode, i) == .needsYou)
        paired.autoPrepare = true
        i.computers[1] = mac("mb", devices: [paired])
        #expect(IPhoneSetupFlow.state(.developerMode, i) == .working, "prepared before: the computer does it again by itself")

        paired.code = nil
        paired.ready = true
        i.computers[1] = mac("mb", devices: [paired])
        i.me = me(paired)
        #expect(IPhoneSetupFlow.current(i) == .allow)

        i.me = me(paired, consented: true)
        #expect(IPhoneSetupFlow.current(i) == nil)
        #expect(IPhoneSetupFlow.doneCount(i) == 5)
        #expect(IPhoneSetupText.summary(i, computer: "mb").contains("mb"))
    }

    @Test func theOverviewShowsLaterSteps() {
        let i = IPhoneSetupInput(extensionOn: true, computers: [mac("mb", on: false)])
        let rows = IPhoneSetupFlow.overview(i)
        #expect(rows.map(\.1) == [.done, .needsYou, .later, .later, .later])
        #expect(IPhoneSetupText.summary(i, computer: nil) != IPhoneSetupText.summary(IPhoneSetupInput(), computer: nil))
    }

    @Test func aFailedPairingAndAnOfflineMac() {
        var i = IPhoneSetupInput(extensionOn: true, computers: [mac("mb", job: ComputerJob(kind: "pair", state: "failed", error: "没信任"))], chosen: "mb")
        #expect(IPhoneSetupFlow.pairPhase(i) == .failed("没信任"))
        #expect(IPhoneSetupFlow.state(.pair, i) == .needsYou)
        i.computers = [mac("mb", online: false)]
        #expect(IPhoneSetupFlow.computer(i) == nil, "an offline Mac isn't the one to go through")
        #expect(i.computers[0].unusable == .offline)
        #expect(mac("st", ddi: false).noXcode && !mac("mb").noXcode && !mac("x", ddi: nil).noXcode)
    }

    @Test func prepareShowsTheJobForThisIPhone() {
        var phone = needsPrepare
        phone.autoPrepare = true
        let reason = "This computer can't get this version of iOS ready."
        let failure = ComputerJob(kind: "prepare", state: "failed", error: reason, code: "ddi_unsupported_device", device: phone.id)
        var input = IPhoneSetupInput(extensionOn: true, computers: [mac("mb", devices: [phone], job: failure)], chosen: "mb", me: me(phone))
        #expect(IPhoneSetupFlow.current(input) == .developerMode)
        #expect(IPhoneSetupFlow.devPhase(input) == .prepareFailed(reason: reason, developerModeOn: true))
        #expect(IPhoneSetupFlow.state(.developerMode, input) == .needsYou)
        input.computers[0].iphone?.devices[0].developerMode = false
        #expect(IPhoneSetupFlow.devPhase(input) == .prepareFailed(reason: reason, developerModeOn: false))
        input.computers[0].iphone?.devices[0].developerMode = true

        let running = ComputerJob(kind: "prepare", state: "running", step: "Getting this iPhone ready…", device: phone.id)
        input.computers[0].iphone?.job = running
        #expect(IPhoneSetupFlow.devPhase(input) == .preparing(step: running.step))
        #expect(IPhoneSetupFlow.state(.developerMode, input) == .working)

        input.computers[0].iphone?.job?.device = "other-iphone"
        #expect(IPhoneSetupFlow.devPhase(input) == .prepare(auto: true))
        input.computers[0].iphone?.job = failure
        input.computers[0].iphone?.job?.device = "other-iphone"
        #expect(IPhoneSetupFlow.devPhase(input) == .prepare(auto: true))

        phone.code = nil
        phone.ready = true
        input.computers[0] = mac("mb", devices: [phone], job: failure)
        #expect(IPhoneSetupFlow.devPhase(input) == .done)
    }

    @Test func prepareRetryFollowsTheNewJob() async throws {
        let phone = needsPrepare
        let old = try JSON.decoder.decode(ComputerJob.self, from: Data(
            #"{"kind":"prepare","state":"failed","error":"Reconnect the cable.","device":"dev-1","started":"2026-11-21T00:00:00.123456789Z"}"#.utf8
        ))
        let oldStart = try #require(old.started)
        let running = ComputerJob(kind: "prepare", state: "running", device: phone.id, started: oldStart.addingTimeInterval(2))
        var returned = MachineIPhone(enabled: true, installed: true, devices: [phone], job: running)
        var polled = MachineIPhone(enabled: true, installed: true, devices: [phone], job: old)
        var requests: [String] = []
        var machine = Machine(id: "mb", name: "MacBook", kind: "ssh", online: true)
        machine.os = "darwin"
        let deps = IPhoneSetupModel.Deps(
            list: { IPhoneList(on: true, devices: [me(phone)]) }, machines: { [machine] }, machineIPhone: { _ in polled },
            setExtension: { _ in }, setComputer: { _, _ in polled },
            job: { _, action, device in requests.append(action + " " + (device ?? "")); return returned },
            consent: { _, _ in me(phone) }, tryIt: { _ in IPhoneTryResult() },
            askEvery: { false }, setAskEvery: { _ in },
            model: "iPhone18,1", ios: "27.0", picked: { nil }, pick: { _ in }, chosen: { "mb" }, choose: { _ in })
        let setup = IPhoneSetupModel(deps: deps)
        await setup.load()
        #expect(IPhoneSetupFlow.devPhase(setup.input) == .prepareFailed(reason: old.error, developerModeOn: true))

        await setup.prepare()
        await setup.load()
        #expect(requests == ["prepare dev-1"])
        #expect(IPhoneSetupFlow.devPhase(setup.input) == .preparing(step: nil), "a read with the old failure can't replace the retry")

        var failed = running
        failed.state = "failed"
        failed.error = old.error
        polled.job = failed
        await setup.load()
        #expect(IPhoneSetupFlow.devPhase(setup.input) == .prepareFailed(reason: old.error, developerModeOn: true),
                "the same reason can be reported by a new job")

        returned.job?.started = oldStart.addingTimeInterval(4)
        returned.job?.state = "failed"
        returned.job?.error = "Unlock this iPhone."
        await setup.prepare()
        #expect(requests == ["prepare dev-1", "prepare dev-1"])
        #expect(IPhoneSetupFlow.devPhase(setup.input) == .prepareFailed(reason: returned.job?.error, developerModeOn: true),
                "the start request can return a failure")
    }

    @Test func theComputerPageNamesTheFailedAction() {
        let reason = "Reconnect the cable."
        let prepare = ComputerJob(kind: "prepare", state: "failed", error: reason)
        let pair = ComputerJob(kind: "pair", state: "failed", error: reason)
        #expect(IPhoneControlText.jobFailure(prepare) == String(localized: "Couldn't get this iPhone ready: \(reason)"))
        #expect(IPhoneControlText.jobFailure(pair) == String(localized: "Couldn't pair: \(reason)"))
        #expect(IPhoneControlText.jobFailure(ComputerJob(kind: "prepare", state: "failed", code: "ddi_missing"))
            == String(localized: "Couldn't get this iPhone ready."))
        #expect(IPhoneControlText.jobFailure(ComputerJob(kind: "prepare", state: "running")) == nil)
    }

    @Test func iOSMajor() {
        #expect(IPhoneSetupFlow.major("27.2") == 27 && IPhoneSetupFlow.major("26.1.1") == 26 && IPhoneSetupFlow.major("") == 0)
    }

    /// The model: pairing starts by itself, once, when this iPhone is on the chosen computer's cable; the consent goes
    /// out only for the iPhone matched; Try It for that iPhone.
    @Test func theModelPairsOnceAndAsksOnlyForThisIPhone() async {
        var jobs: [String] = []
        var consents: [String] = []
        var status = MachineIPhone(enabled: true, installed: true, devices: [cable])
        var machine = Machine(id: "mb", name: "MacBook", kind: "ssh", online: true)
        machine.os = "darwin"
        var list = IPhoneList(on: true, devices: [me(cable)])
        let deps = IPhoneSetupModel.Deps(
            list: { list }, machines: { [machine] }, machineIPhone: { _ in status },
            setExtension: { _ in }, setComputer: { [status] _, _ in status },
            job: { _, action, device in jobs.append(action + " " + (device ?? "")); return status },
            consent: { device, allowed in consents.append("\(device) \(allowed)"); return IPhoneSeen(device: self.cable, consented: allowed) },
            tryIt: { _ in IPhoneTryResult(opened: true, app: "com.apple.weather") },
            askEvery: { false }, setAskEvery: { _ in },
            model: "iPhone18,1", ios: "27.0", picked: { nil }, pick: { _ in }, chosen: { "mb" }, choose: { _ in })
        let setup = IPhoneSetupModel(deps: deps)
        await setup.load()
        await setup.load()
        #expect(jobs == ["pair dev-1"], "\(jobs)")
        #expect(setup.current == .pair)

        var paired = cable
        paired.paired = true
        paired.ready = true
        paired.connected = true
        status = MachineIPhone(enabled: true, installed: true, devices: [paired])
        list = IPhoneList(on: true, devices: [me(paired)])
        await setup.load()
        #expect(setup.current == .allow)
        await setup.setAllowed(true)
        #expect(consents == ["dev-1 true"])
        await setup.tryIt()
        #expect(setup.tried?.opened == true)
    }
}

private extension MachineIPhone {
    func with(ddi: Bool?) -> MachineIPhone {
        var m = self
        m.ddiPresent = ddi
        return m
    }
}
