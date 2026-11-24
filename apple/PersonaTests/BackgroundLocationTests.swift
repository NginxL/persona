import CoreLocation
import Foundation
import Testing
@testable import Persona

/// Location while Persona isn't open (#13200): when Persona holds the Always session and runs the significant-change
/// service, when it asks iOS for Always, when it offers it, and what a read refused in the background answers. Core
/// Location is replaced by `FakeLocationSystem`, so no system dialog shows and nothing is asked of the simulator.
@MainActor
struct BackgroundLocationTests {
    // MARK: - Holding the Always session

    /// The owner's rule for D2: a session whose goal is Always asks iOS as soon as Persona is in front, so nothing is
    /// held at launch for anyone who hasn't got Always, whatever iOS says.
    @Test func launchHoldsNothingAndAsksNothingWithoutAlways() {
        for level in [LocationLevel.notDetermined, .denied, .whileUsing] {
            let (location, fake, done) = make()
            defer { done() }
            location.launch()
            #expect(fake.started == 1)
            fake.say(level)
            #expect(fake.held == 0, "no Always session at launch for \(level)")
            #expect(fake.requests == 0, "no question at launch for \(level)")
            #expect(fake.changes.isEmpty, "no significant-change service for \(level)")
        }
    }

    @Test func alwaysHoldsOneSessionAndRunsTheServiceOnce() {
        let (location, fake, done) = make()
        defer { done() }
        location.launch()
        location.launch() // a second call starts nothing new
        fake.say(.always)
        fake.say(.always) // iOS reports it again after a return from the background
        #expect(fake.started == 1)
        #expect(fake.held == 1)
        #expect(fake.ended == 0)
        #expect(fake.changes == [true])
        #expect(fake.requests == 0)
    }

    @Test func turningLocationOffEndsBothAndOnHoldsThemAgain() {
        var on = true
        let (location, fake, done) = make(sourceOn: { on })
        defer { done() }
        location.launch()
        fake.say(.always)
        on = false
        location.apply()
        #expect(fake.ended == 1)
        #expect(fake.changes == [true, false])
        on = true
        location.apply()
        #expect(fake.held == 2)
        #expect(fake.changes == [true, false, true])
    }

    @Test func alwaysWithLocationOffHoldsNothing() {
        let (location, fake, done) = make(sourceOn: { false })
        defer { done() }
        location.launch()
        fake.say(.always)
        #expect(fake.held == 0)
        #expect(fake.changes.isEmpty)
    }

    @Test func goingBackToWhileUsingEndsTheSession() {
        let (location, fake, done) = make()
        defer { done() }
        location.launch()
        fake.say(.always)
        fake.say(.whileUsing) // changed in iOS Settings, or at iOS's own reminder
        #expect(fake.ended == 1)
        #expect(fake.changes == [true, false])
    }

    // MARK: - Asking iOS

    @Test func allowAsksOnceAndHoldsOnlyOnceIOSSaysAlways() {
        let (location, fake, done) = make()
        defer { done() }
        location.launch()
        fake.say(.whileUsing)
        #expect(location.allowAlways())
        #expect(fake.requests == 1)
        #expect(fake.held == 0, "the owner agreed, but iOS hasn't said Always yet")
        #expect(location.state.asked)
        #expect(!location.allowAlways(), "iOS asks only once: the row then opens iOS Settings")
        #expect(fake.requests == 1)
        fake.say(.always)
        #expect(fake.held == 1)
    }

    @Test func allowCanOnlyAskFromWhileUsing() {
        for level in [LocationLevel.notDetermined, .denied, .always] {
            let (location, fake, done) = make()
            defer { done() }
            location.launch()
            fake.say(level)
            #expect(!location.allowAlways(), "\(level)")
            #expect(fake.requests == 0, "\(level)")
        }
        let (location, fake, done) = make()
        defer { done() }
        location.launch() // no level heard yet
        #expect(!location.allowAlways())
        #expect(fake.requests == 0)
    }

    @Test func askedIsRememberedAcrossLaunches() {
        let (defaults, done) = Self.defaults()
        defer { done() }
        let first = FakeLocationSystem()
        let location = BackgroundLocation(system: first, defaults: defaults, sourceOn: { true }, inFront: { true })
        location.launch()
        first.say(.whileUsing)
        location.allowAlways()
        let again = FakeLocationSystem()
        let relaunched = BackgroundLocation(system: again, defaults: defaults, sourceOn: { true }, inFront: { true })
        relaunched.launch()
        again.say(.whileUsing)
        #expect(!relaunched.allowAlways())
        #expect(again.requests == 0)
    }

    // MARK: - The offer

    @Test func aReadRefusedInTheBackgroundOffersOnceInFront() {
        var front = false
        let (location, fake, done) = make(inFront: { front })
        defer { done() }
        location.launch()
        fake.say(.whileUsing)
        location.noteRefusedInBackground()
        #expect(!location.offering, "never while Persona is not in front")
        front = true
        location.cameToFront()
        #expect(location.offering)
        #expect(fake.requests == 0, "the sheet asks nothing by itself")
        location.offerShown()
        location.offering = false // Not Now, or a swipe down
        location.noteRefusedInBackground()
        location.cameToFront()
        #expect(!location.offering, "shown once")
    }

    @Test func noOfferWithoutARefusal() {
        let (location, fake, done) = make()
        defer { done() }
        location.launch()
        fake.say(.whileUsing)
        location.cameToFront()
        #expect(!location.offering)
    }

    @Test func noOfferOnceAlwaysAskedOrWithLocationOff() {
        do {
            let (location, fake, done) = make()
            defer { done() }
            location.launch()
            fake.say(.always)
            location.noteRefusedInBackground()
            location.cameToFront()
            #expect(!location.offering, "already Always")
        }
        do {
            let (location, fake, done) = make()
            defer { done() }
            location.launch()
            fake.say(.whileUsing)
            location.allowAlways() // from Settings, before any refusal
            location.noteRefusedInBackground()
            location.cameToFront()
            #expect(!location.offering, "iOS was asked already")
        }
        do {
            let (location, fake, done) = make(sourceOn: { false })
            defer { done() }
            location.launch()
            fake.say(.whileUsing)
            location.noteRefusedInBackground()
            location.cameToFront()
            #expect(!location.offering, "Location is off")
        }
    }

    /// A launch into the front: the scene is active before Core Location has said the level; the offer waits for it.
    @Test func theOfferWaitsForTheLevel() {
        let (defaults, done) = Self.defaults()
        defer { done() }
        let earlier = BackgroundLocation(system: FakeLocationSystem(), defaults: defaults, sourceOn: { true }, inFront: { false })
        earlier.noteRefusedInBackground()
        let fake = FakeLocationSystem()
        let location = BackgroundLocation(system: fake, defaults: defaults, sourceOn: { true }, inFront: { true })
        location.launch()
        location.cameToFront()
        #expect(!location.offering)
        fake.say(.whileUsing)
        #expect(location.offering)
    }

    @Test func aRefusalIsForgottenOnceTheLevelIsNotWhileUsing() {
        let (location, fake, done) = make()
        defer { done() }
        location.launch()
        fake.say(.whileUsing)
        location.noteRefusedInBackground()
        fake.say(.always) // allowed in iOS Settings meanwhile
        #expect(!location.state.pending)
    }

    // MARK: - What a refused read answers

    @Test func aBackgroundReadWithWhileUsingSaysToOpenPersona() {
        var noted = 0
        let error = LocationTool.whileUsingRefusal(ToolFailure(.denied, "Location"), level: .whileUsing, inFront: false) {
            noted += 1
        }
        let text = ToolErrors.describe(error)
        #expect(noted == 1)
        #expect(text.contains("only while Persona is open"))
        #expect(text.contains("Ask the user to open Persona"))
        #expect(!text.contains("Permission denied"), "While Using is allowed: nothing to turn on in iOS Settings")
    }

    @Test func otherFailuresPassAsTheyWere() {
        let cases: [(LocationLevel, Bool)] = [(.whileUsing, true), (.always, false), (.always, true), (.denied, false)]
        for (level, front) in cases {
            var noted = 0
            let original = ToolFailure(.unavailable, "No location fix within 15 seconds.")
            let error = LocationTool.whileUsingRefusal(original, level: level, inFront: front) { noted += 1 }
            #expect(ToolErrors.describe(error) == ToolErrors.describe(original), "\(level), in front: \(front)")
            #expect(noted == 0, "\(level), in front: \(front)")
        }
    }

    // MARK: - Settings › Connectors › This iPhone

    @Test func theLocationRowSaysTheLevelAndOneThingToDo() {
        #expect(BackgroundLocationPolicy.line(.whileUsing, asked: false)?.action == .allowAlways)
        #expect(BackgroundLocationPolicy.line(.whileUsing, asked: true)?.action == .openSettings)
        #expect(BackgroundLocationPolicy.line(.always, asked: false)?.action == .openSettings)
        #expect(BackgroundLocationPolicy.line(.always, asked: true)?.level == .always)
        for level: LocationLevel? in [.notDetermined, .denied, nil] {
            #expect(BackgroundLocationPolicy.line(level, asked: false) == nil, "the switch says it: \(String(describing: level))")
        }
        let words = Set([LocationLevel.whileUsing, .always].compactMap { BackgroundLocationPolicy.line($0, asked: false)?.text })
        #expect(words.count == 2)
        #expect(words.allSatisfy { !$0.isEmpty })
    }

    @Test func theSheetSaysWhenItReadsAndThatNothingIsRecorded() {
        #expect(LocationAlwaysPoint.allCases == [.readWhenNeeded, .noTrail])
        #expect(LocationAlwaysPoint.allCases.allSatisfy { !$0.text.isEmpty && !$0.symbol.isEmpty })
    }

    @Test func levelsFollowCoreLocation() {
        #expect(LocationLevel(.authorizedAlways) == .always)
        #expect(LocationLevel(.authorizedWhenInUse) == .whileUsing)
        #expect(LocationLevel(.denied) == .denied)
        #expect(LocationLevel(.restricted) == .denied)
        #expect(LocationLevel(.notDetermined) == .notDetermined)
    }

    // MARK: - Helpers

    private func make(sourceOn: @escaping () -> Bool = { true },
                      inFront: @escaping () -> Bool = { true }) -> (BackgroundLocation, FakeLocationSystem, () -> Void) {
        let (defaults, done) = Self.defaults()
        let fake = FakeLocationSystem()
        let location = BackgroundLocation(system: fake, defaults: defaults, sourceOn: sourceOn, inFront: inFront)
        return (location, fake, done)
    }

    /// A suite of its own, and how to remove it.
    private static func defaults() -> (UserDefaults, () -> Void) {
        let name = "BackgroundLocationTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: name)!
        return (defaults, { defaults.removePersistentDomain(forName: name) })
    }
}

/// Counts what `BackgroundLocation` asks of Core Location, and says levels as iOS would.
@MainActor
final class FakeLocationSystem: BackgroundLocationSystem {
    private(set) var started = 0
    private(set) var requests = 0
    private(set) var held = 0
    private(set) var ended = 0
    private(set) var changes: [Bool] = []
    private var heard: ((LocationLevel) -> Void)?

    func start(heard: @escaping (LocationLevel) -> Void) {
        started += 1
        self.heard = heard
    }

    func requestAlways() { requests += 1 }

    func holdAlwaysSession() -> () -> Void {
        held += 1
        return { [weak self] in self?.ended += 1 }
    }

    func significantChanges(_ on: Bool) { changes.append(on) }

    /// Core Location reports a level.
    func say(_ level: LocationLevel) { heard?(level) }
}
