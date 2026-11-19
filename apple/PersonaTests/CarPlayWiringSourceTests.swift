import Foundation
import Testing

/// The CarPlay scene delegate and the Siri intent use the rules tested in CarPlayVoiceScreenTests (CarPlayAutoStart,
/// CarPlayTalk, opening states). The delegate needs a real car connection, so these read the sources next to this file,
/// like `HoldSourceTests`: each bug came back by calling the session or the template straight from a lifecycle method.
struct CarPlayWiringSourceTests {
    private static let ios = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    /// The file's lines with `//` comments taken out.
    private static func code(_ path: String) throws -> [String] {
        try String(contentsOf: ios.appending(path: path), encoding: .utf8).components(separatedBy: "\n")
            .map { l in l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l }
    }

    /// The body of the first `{ … }` starting on a line that contains `start`.
    private static func body(_ lines: [String], _ start: String) -> String? {
        guard let first = lines.firstIndex(where: { $0.contains(start) }) else { return nil }
        var depth = 0, opened = false, out = ""
        for line in lines[first...] {
            for c in line {
                if c == "{" { depth += 1; opened = true }
                if c == "}" { depth -= 1 }
            }
            out += line + "\n"
            if opened, depth == 0 { return out }
        }
        return nil
    }

    private static let delegate = "Persona/CarPlay/CarPlaySceneDelegate.swift"

    /// A banner, Siri or a call makes the scene active again: only an armed CarPlayAutoStart may start it.
    @Test func becomingActiveStartsOnlyWhenArmed() throws {
        let lines = try Self.code(Self.delegate)
        let active = try #require(Self.body(lines, "func sceneDidBecomeActive("))
        #expect(active.contains("autoStart.takeOnBecomingActive()"))
        let foreground = try #require(Self.body(lines, "func sceneWillEnterForeground("))
        #expect(foreground.contains("autoStart.arm()"))
        let connect = try #require(Self.body(lines, "didConnect interfaceController"))
        #expect(connect.contains("autoStart.arm()"))
    }

    /// Siri may only open the microphone with Persona on the car screen, so the foreground is tracked.
    @Test func siriChecksTheForeground() throws {
        let intent = try Self.code("Persona/Intents/TalkToPersonaIntent.swift").joined(separator: "\n")
        #expect(!intent.contains("CarPlayVoicePresence.isConnected"))
        #expect(intent.contains("CarPlayVoicePresence.talk"))
        let lines = try Self.code(Self.delegate)
        let background = try #require(Self.body(lines, "func sceneDidEnterBackground("))
        #expect(background.contains("CarPlayVoicePresence.isForeground = false"))
        let foreground = try #require(Self.body(lines, "func sceneWillEnterForeground("))
        #expect(foreground.contains("CarPlayVoicePresence.isForeground = true"))
    }

    /// Talk to Persona with no car is a call to Persona on the phone (#9290): only for `.notConnected`, in front first
    /// (the system takes a call's Start only from an app in front), the way Call Persona places it. The car's path is
    /// as it was: the same check, the same delayed start of the car's session.
    @Test func withoutACarSiriCallsPersonaOnThePhone() throws {
        let lines = try Self.code("Persona/Intents/TalkToPersonaIntent.swift")
        let perform = try #require(Self.body(lines, "func perform() async throws"))
        let phone = try #require(Self.body(perform.components(separatedBy: "\n"), "if talk.callsOnThePhone"))
        let foreground = try #require(phone.range(of: "try await continueInForeground(alwaysConfirm: false)"))
        let place = try #require(phone.range(of: "CallPersonaIntent.place()"))
        #expect(foreground.upperBound < place.lowerBound, "in front before the call's Start")
        #expect(!phone.contains("CarPlayVoiceSession"))
        // The decision is `talkNow`'s, which asks the audio route when Persona has no scene (a car showing Maps, or Siri
        // having just launched the app, is a car): Persona's scene alone is not whether a car is there.
        let decided = try #require(perform.range(of: "let talk = await CarPlayVoicePresence.talkNow()"))
        #expect(perform.range(of: "if talk.callsOnThePhone").map { decided.upperBound < $0.lowerBound } == true)
        #expect(!perform.contains("MainActor.run { CarPlayVoicePresence.talk }"))
        let car = try #require(perform.range(of: "guard talk == .start else"))
        #expect(perform.range(of: "if talk.callsOnThePhone").map { $0.upperBound < car.lowerBound } == true)
        let start = try #require(Self.body(perform.components(separatedBy: "\n"), "Task { @MainActor in"))
        #expect(start.contains("try? await Task.sleep(for: .seconds(1.5))"))
        #expect(start.contains("guard CarPlayVoicePresence.talk == .start else { return }"))
        #expect(start.contains("_ = await CarPlayVoiceSession.start()"))
        let all = lines.joined(separator: "\n")
        #expect(all.contains("supportedModes: IntentModes { [.background, .foreground(.dynamic)] }"), "the car's path stays in the background")
    }

    /// Call Persona and Talk to Persona on the phone place the call as Siri's (`.siri`), so the Start waits for the app
    /// to be active: one started while it still comes to the front is refused (LiveCommunicationKit's code 6).
    @Test func siriCallsWaitForTheAppToBeActive() throws {
        let intent = try Self.code("Persona/Intents/CallPersonaIntent.swift").joined(separator: "\n")
        #expect(intent.contains("supportedModes: IntentModes { .foreground(.immediate) }"))
        #expect(intent.contains("PersonaCalls.shared.callPersona(origin: .siri)"))
        let place = try #require(Self.body(try Self.code("Persona/Calls/OwnerCall.swift"), "func place("))
        #expect(place.contains("if origin != .app { await Self.waitUntilActive() }"))
    }

    /// Without Persona's scene the route is read off the main actor (a system service, docs/apple/performance.md P1):
    /// `talkNow` is `@concurrent`, and the route is asked only when there is no scene.
    @Test func theCarsRouteIsReadOffTheMainActor() throws {
        let lines = try Self.code("Persona/CarPlay/CarPlayVoiceScreen.swift")
        let talkNow = try #require(lines.first { $0.contains("static func talkNow(") })
        #expect(talkNow.contains("@concurrent nonisolated static func talkNow(car: @Sendable () -> Bool = soundGoesToACar)"))
        let body = try #require(Self.body(lines, "static func talkNow("))
        #expect(body.contains("carOnRoute: !connected && car()"))
        let onMain = try #require(Self.body(body.components(separatedBy: "\n"), "MainActor.run {"))
        #expect(!onMain.contains("car()"), "the route asked on the main actor")
    }

    /// Siri, Shortcuts and Recents leave the page or sheet the app was on as it is (#9290 follow-up): the call's page and a
    /// refusal's alert come over it in the call's own window (`CallWindow`), so nothing closes Settings any more.
    @Test func siriCallsLeaveThePageTheAppWasOn() throws {
        let lines = try Self.code("Persona/Calls/OwnerCall.swift")
        let call = try #require(Self.body(lines, "func callPersona("))
        #expect(!call.contains("shellSettings") && !call.contains("clearTheWay"), "the call closes what the owner was doing")
        #expect(Self.body(lines, "static func clearTheWay(") == nil)
    }

    /// CarPlay ignores state switches before the template is on screen: `show` never switches directly.
    @Test func statesSwitchOnlyOnceTheTemplateIsPresented() throws {
        let lines = try Self.code(Self.delegate)
        let show = try #require(Self.body(lines, "func showEndedOrCall("))
        #expect(!show.contains("activateVoiceControlState"))
        #expect(!show.contains("setRootTemplate"))
        let activate = try #require(Self.body(lines, "func activateCurrentState("))
        #expect(activate.contains("guard presented"))
        let present = try #require(Self.body(lines, "func present("))
        #expect(present.contains("presented = true"))
        let all = lines.joined(separator: "\n")
        #expect(all.components(separatedBy: "activateVoiceControlState").count == 2, "only activateCurrentState switches")
    }

    /// The car call goes through the shared engine, not the old DuplexVoiceSession.
    @Test func theCarUsesVoiceCalls() throws {
        let delegate = try Self.code(Self.delegate).joined(separator: "\n")
        #expect(delegate.contains("CarPlayVoiceSession"))
        #expect(delegate.contains("VoiceCalls"))
        #expect(!delegate.contains("DuplexVoiceSession"))
        let intent = try Self.code("Persona/Intents/TalkToPersonaIntent.swift").joined(separator: "\n")
        #expect(intent.contains("CarPlayVoiceSession"))
        #expect(!intent.contains("DuplexVoiceSession"))
    }
}
