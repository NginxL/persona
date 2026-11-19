import Foundation
import Testing

/// What only the sources can show about the app's calls (#9290), read next to this file like `CarPlayWiringSourceTests`:
/// one LiveCommunicationKit manager in the whole app (a second one takes every action from the first), no CallKit
/// anywhere (App Store China rejects a binary that links it; LiveCommunicationKit's errors are read by their domain
/// string instead), a system call's audio never touching the system's session, and voice chat refused during an
/// urgent call.
struct CallSourceTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    /// Every Swift file under `apple/` but the local packages, with `//` comments taken out.
    /// Paths relative to `apple/`, both sides with symlinks resolved: a checkout under a symlinked folder (`/var` →
    /// `/private/var`, where scripts/ci/apple.sh puts its snapshot) is walked under the resolved name.
    private static func sources(tests: Bool) throws -> [(path: String, lines: [String])] {
        let root = apple.resolvingSymlinksInPath()
        let walker = try #require(FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil))
        var out: [(String, [String])] = []
        for case let url as URL in walker where url.pathExtension == "swift" {
            let full = url.resolvingSymlinksInPath().path
            guard full.hasPrefix(root.path + "/") else { continue }
            let path = String(full.dropFirst(root.path.count + 1))
            if path.hasPrefix("Packages/") || path.contains("/.build/") || path.contains("DerivedData") { continue }
            if !tests, path.hasPrefix("PersonaTests/") || path.hasPrefix("PersonaUITests/") { continue }
            let lines = try String(contentsOf: url, encoding: .utf8).components(separatedBy: "\n")
                .map { l in l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l }
            out.append((path, lines))
        }
        return out
    }

    @Test func theAppMakesOneConversationManagerInOnePlace() throws {
        let files = try Self.sources(tests: false)
        #expect(files.count > 500, "the sources were found")
        var sites: [String] = []
        for file in files {
            for (n, line) in file.lines.enumerated() where Self.constructs(line, "ConversationManager(") {
                sites.append("\(file.path):\(n + 1)")
            }
        }
        #expect(sites.count == 1, "\(sites)")
        #expect(sites.first?.hasPrefix("Persona/Calls/CallSystem.swift") == true, "\(sites)")
    }

    @Test func noSwiftFileImportsCallKit() throws {
        let kit = "Call" + "Kit"
        let imports = try Regex(#"^\s*(@\w+\s+)*import\s+(class\s+|struct\s+|enum\s+|protocol\s+|func\s+|var\s+|let\s+|typealias\s+)?"# + kit + #"(\.|\s|$)"#)
        let types = try Regex(#"\bCX(Call|Provider|Transaction|Handle|Action|Start|End|Answer|SetMuted|SetHeld|Error)[A-Za-z]*\b"#)
        let files = try Self.sources(tests: true)
        var found: [String] = []
        for file in files {
            for (n, line) in file.lines.enumerated() where line.contains(imports) || line.contains(types) {
                found.append("\(file.path):\(n + 1): \(line.trimmingCharacters(in: .whitespaces))")
            }
        }
        #expect(found.isEmpty, "\(found)")
    }

    /// `line` calls `call` itself: not a longer name that ends so (`TelephonyConversationManager(`), nor a member's.
    private static func constructs(_ line: String, _ call: String) -> Bool {
        var rest = line[...]
        while let r = rest.range(of: call) {
            let before = r.lowerBound == line.startIndex ? nil : line[line.index(before: r.lowerBound)]
            if before.map({ !($0.isLetter || $0.isNumber || $0 == "_" || $0 == ".") }) ?? true { return true }
            rest = line[r.upperBound...]
        }
        return false
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

    private static func lines(_ path: String) throws -> [String] {
        try #require(try sources(tests: false).first { $0.path == path }).lines
    }

    /// The system owns a call's session: nothing on the system-call path sets its category, activates it, moves the
    /// route or picks a microphone (the call's manager sets the category once, before Start).
    @Test func aSystemCallsAudioNeverTouchesTheSystemsSession() throws {
        let lines = try Self.lines("Persona/Voice/PhoneVoiceAudio.swift")
        for name in ["func startOnSystemSession(", "func activate()", "func deactivate()", "func setHeld(", "func bringUp()",
                     "func tearDown()", "func interrupted()"] {
            let body = try #require(Self.body(lines, name), "\(name)")
            for forbidden in ["setActive(", "setCategory(", "overrideOutputAudioPort(", "setPreferredInput(", "follow("] {
                #expect(!body.contains(forbidden), "\(name) calls \(forbidden)")
            }
        }
        let moved = try #require(Self.body(lines, "func routeMoved("))
        #expect(moved.contains("if owner == .app {"), "only the app's own session follows VoiceRoute")
    }

    /// Every place under `apple/` that sets up the audio session (category, activation, the route, the microphone) has
    /// a verdict for a live system call (docs/apple/calls.md, "Nothing in the app ends a call"): the call's own, gated
    /// (asks `LiveCallGate` / the call manager first and leaves the session alone), or able to run only where a call
    /// cannot be (a recording, which is refused during one). A new place fails here until it is given one.
    @Test func everyAudioSessionChangeHasAVerdictForALiveCall() throws {
        let audited: [String: (verdict: String, gate: String?)] = [
            "Persona/Calls/CallRoute.swift": ("the call's own Speaker", nil),
            "Persona/Calls/CallSystem.swift": ("the call's own: the watchdog's one activation, the Simulator's stand-in", nil),
            "Persona/Calls/PersonaCalls.swift": ("the call's own category, before its Start or as it rings", nil),
            "Persona/Voice/PhoneVoiceAudio.swift": ("an in-app voice chat, started only through VoiceCalls (refused or asked first)", "systemCallName"),
            "Persona/Views/Chat/PromptComposer.swift": ("dictation: refused", "callGate"),
            "Shared/Recording/RecordingCapture.swift": ("a recording: refused, a paused one stays paused", "callGate"),
            "Persona/Recording/MeetingReplay.swift": ("meeting replay: refused, audio-session changes skipped", "LiveCallGate.liveNow"),
            "Persona/Screen/VNCPiP.swift": ("picture in picture: the session left alone", "LiveCallGate.callIsLive"),
            "Persona/Recording/MeetingSpeaker.swift": ("a meeting recording's answers: a recording a call paused leaves the call's route alone", "LiveCallGate.callIsLive"),
        ]
        let calls = ["setCategory(", "setActive(", "overrideOutputAudioPort(", "setPreferredInput(", "setMode("]
        var found: Set<String> = []
        for file in try Self.sources(tests: false) where file.lines.contains(where: { line in calls.contains { line.contains($0) } }) {
            found.insert(file.path)
        }
        #expect(found == Set(audited.keys), "audit every new place for a live call (docs/apple/calls.md): \(found.symmetricDifference(audited.keys).sorted())")
        let voiceCalls = try Self.lines("Persona/Voice/VoiceCalls.swift").joined(separator: "\n")
        for (path, entry) in audited {
            guard let gate = entry.gate else { continue }
            let text = path == "Persona/Voice/PhoneVoiceAudio.swift" ? voiceCalls : try Self.lines(path).joined(separator: "\n")
            #expect(text.contains(gate), "\(path) (\(entry.verdict)) does not ask \(gate)")
        }
        // The two refusals come before anything is asked or opened.
        let dictation = try #require(Self.body(try Self.lines("Persona/Views/Chat/PromptComposer.swift"), "func start(hints:"))
        #expect(dictation.range(of: "callGate()").map { $0.lowerBound < (dictation.range(of: "Task {")?.lowerBound ?? dictation.endIndex) } == true)
        let capture = try Self.lines("Shared/Recording/RecordingCapture.swift")
        for name in ["func start(locale:", "private func resume("] {
            let body = try #require(Self.body(capture, name), "\(name)")
            let gate = try #require(body.range(of: "callGate()"), "\(name) does not ask the gate")
            #expect(gate.lowerBound < (body.range(of: "configureSession(")?.lowerBound ?? body.endIndex), "\(name)")
            #expect(gate.lowerBound < (body.range(of: "askPermission()")?.lowerBound ?? body.endIndex), "\(name)")
        }
        let pip = try #require(Self.body(try Self.lines("Persona/Screen/VNCPiP.swift"), "if !LiveCallGate.callIsLive {"))
        #expect(pip.contains("setCategory(") && pip.contains("setActive("), "picture in picture sets the session only with no call")
        // A replay asks on the main actor before its tape opens, then again on the session queue before every session
        // change: a call that went live while its audio file opened or ended keeps its own session (#10693).
        let replay = try Self.lines("Persona/Recording/MeetingReplay.swift")
        let canStart = try #require(Self.body(replay, "static var canStart"))
        #expect(canStart.contains("callGate() == nil"), "a replay starts during a live call")
        let prepare = try #require(Self.body(replay, "private func prepareAudio("))
        let queue = try #require(prepare.range(of: "LiveCallGate.sessionQueue.sync"))
        let live = try #require(prepare.range(of: "!LiveCallGate.liveNow", range: queue.upperBound..<prepare.endIndex))
        for call in ["setCategory(", "setActive(true)"] {
            #expect(live.lowerBound < (prepare.range(of: call)?.lowerBound ?? prepare.startIndex), "\(call) runs before the replay's call gate")
        }
        let deactivate = try #require(Self.body(replay, "private static func deactivateSession()"))
        let offQueue = try #require(deactivate.range(of: "LiveCallGate.sessionQueue.sync"))
        let offLive = try #require(deactivate.range(of: "!LiveCallGate.liveNow", range: offQueue.upperBound..<deactivate.endIndex))
        #expect(offLive.lowerBound < (deactivate.range(of: "setActive(false")?.lowerBound ?? deactivate.startIndex))
    }

    /// Nothing in the app ends a live call to Persona by itself (#9290 follow-up): every way into a voice chat asks the call
    /// manager first (its own conversation shows the call screen, another asks End Call and Start), CarPlay says the call
    /// is on the phone, a meeting recording is refused before its page opens, and the call keeps the microphone.
    @Test func nothingInTheAppEndsALiveCallByItself() throws {
        let entry = try Self.lines("Persona/Views/Voice/VoiceEntry.swift")
        let start = try #require(Self.body(entry, "private static func prepare("))
        let own = try #require(start.range(of: "case .showCall:"))
        let ask = try #require(start.range(of: "return .said(.endCallFirst(name, key: key))"))
        let begin = try #require(start.range(of: "VoiceCalls.shared.start("))
        #expect(own.lowerBound < ask.lowerBound && ask.lowerBound < begin.lowerBound)
        #expect(start.contains("calls.present()"))
        #expect(begin.upperBound < start.endIndex && start.contains("endingCall: endingCall"))
        let strip = try #require(Self.body(entry, "struct VoiceEndedStrip"))
        #expect(strip.contains("refusal = .endCallFirst(name, key: call.key, again: true)"))
        let alert = try #require(Self.body(entry, "func voiceUnavailableAlert("))
        #expect(alert.contains("Button(proceed.label, role: .destructive)") && alert.contains("Button(\"Cancel\", role: .cancel) {}"))
        // The phone icon on the iPhone (#9887 one button): its own call's conversation shows the call screen, another asks
        // End and Call first, before anything is placed.
        let call = try #require(start.range(of: "return .said(.callElsewhereFirst(name, key: key, target: callTitle(key, model: model)))"))
        let placed = try #require(start.range(of: "await calls.callConversation(key, endingCall: endingCall)"))
        #expect(call.lowerBound < placed.lowerBound && placed.lowerBound < ask.lowerBound)
        #expect(start.contains("ownCall: calls.isOwnerCallConversation(key)"))

        let car = try Self.lines("Persona/CarPlay/CarPlayVoiceSession.swift")
        for name in ["static func start()", "static func resume()"] {
            let body = try #require(Self.body(car, name), "\(name)")
            #expect(body.contains("if let onPhone = onPhoneCall() { return onPhone }"), "\(name)")
        }
        let talk = try Self.lines("Persona/Intents/TalkToPersonaIntent.swift").joined(separator: "\n")
        #expect(talk.contains("CarPlayVoiceSession.onPhoneCall()"))

        let voiceCalls = try Self.lines("Persona/Voice/VoiceCalls.swift")
        for name in ["func start(", "func resume("] {
            let body = try #require(Self.body(voiceCalls, name), "\(name)")
            #expect(body.contains("callRefusal("), "\(name)")
        }
        let personaCalls = try Self.lines("Persona/Calls/PersonaCalls.swift").joined(separator: "\n")
        #expect(personaCalls.contains("voiceCalls.systemCallName = { [weak self] in self?.liveOwnerCallName }"))

        let meeting = try #require(Self.body(try Self.lines("Persona/Recording/MeetingRecorder.swift"), "func startMeeting("))
        let refused = try #require(meeting.range(of: "if let refusal = capture.callGate() {"))
        #expect(refused.lowerBound < (meeting.range(of: "Self.openRecordings()")?.lowerBound ?? meeting.startIndex))

        let voice = try #require(Self.body(try Self.lines("Persona/Voice/VoiceConversation.swift"), "func begin() async"))
        #expect(voice.contains("surface == .call ? deps.microphone.claim(microphoneOwner)"), "a system call gives the microphone away")

        let tap = try #require(Self.body(try Self.lines("Persona/DesignSystem/DictationBar.swift"), "func start(_ dictation: any DictationStarting"))
        let hold = try #require(Self.body(try Self.lines("Persona/DesignSystem/ComposerHoldMic.swift"), "func begin(_ dictation: Dictation"))
        for body in [tap, hold] {
            let gate = try #require(body.range(of: "if let refusal = dictation.callRefusal {"))
            #expect(gate.lowerBound < (body.range(of: "withAnimation")?.lowerBound ?? body.endIndex), "the bar opens before the refusal")
        }
    }

    /// The system call carries the assistant's name (#9290 follow-up): no literal handle left on the call's path.
    @Test func theSystemCallCarriesTheAssistantsName() throws {
        let system = try Self.lines("Persona/Calls/CallSystem.swift").joined(separator: "\n")
        #expect(system.contains("case .generic: return CallHandle(kind: .generic, value: name, displayName: shown)"))
        #expect(system.contains("members: [Handle(type: .generic, value: name, displayName: title)]"))
        #expect(system.contains("iconTemplateImageData: icon") && system.contains("let icon = CallIcon.ready"))
        for path in ["Persona/Calls/CallSystem.swift", "Persona/Calls/CallScreen.swift", "Persona/Calls/OwnerCall.swift", "Persona/Calls/PersonaCalls.swift"] {
            let text = try Self.lines(path).joined(separator: "\n")
            #expect(!text.contains("CallHandle.persona"), "\(path)")
        }
    }

    /// The refusal itself is `VoiceCalls`' (`VoiceCallsTests`, `PersonaCallsTests`); here, that every way in says it:
    /// the page's Voice Chat at once, its Continue, CarPlay's start and Continue, and that `PersonaCalls` answers it.
    @Test func voiceChatIsRefusedDuringAnUrgentCall() throws {
        let entry = try Self.lines("Persona/Views/Voice/VoiceEntry.swift")
        let start = try #require(Self.body(entry, "private static func prepare("))
        #expect(start.contains("let calls = PersonaCalls.shared") && start.contains("switch choose(urgent: calls.urgentInProgress,")
                && start.contains("case .urgentCall:\n            return .said(PersonaCalls.urgentNote)"))
        let strip = try #require(Self.body(entry, "struct VoiceEndedStrip"))
        #expect(strip.contains("refusal = await VoiceCalls.shared.resume(") && strip.contains(".voiceUnavailableAlert($refusal"))
        let car = try Self.lines("Persona/CarPlay/CarPlayVoiceSession.swift").joined(separator: "\n")
        #expect(car.contains("if let refused = await VoiceCalls.shared.start("))
        #expect(car.contains("if let refused = await VoiceCalls.shared.resume("))
        let calls = try Self.lines("Persona/Calls/PersonaCalls.swift").joined(separator: "\n")
        #expect(calls.contains("voiceCalls.urgentCallOn = { [weak self] in self?.urgentInProgress ?? false }"))
        // Every way in waits for a call to Persona to let its audio go, in `VoiceCalls` itself (CarPlay too).
        #expect(calls.contains("voiceCalls.endSystemCall = { [weak self] in await self?.endForVoiceChat() }"))
        let voiceCalls = try Self.lines("Persona/Voice/VoiceCalls.swift")
        for name in ["func start(", "func resume("] {
            let body = try #require(Self.body(voiceCalls, name), "\(name)")
            #expect(body.contains("await handOff("), "\(name)")
        }
    }

    /// The second review's findings (#9290 follow-up), as rules only the sources show.
    ///
    /// A recording that stops, never starts or changes under a live call leaves the session alone; every wait in a
    /// recording's start and resume asks the gate again; dictation asks after its permission and opens its session on
    /// the queue the call sets its category on; the voice chat question's yes hears the refusal that comes after its wait.
    @Test func whatTheSecondReviewFoundStaysFixed() throws {
        let capture = try Self.lines("Shared/Recording/RecordingCapture.swift")
        let release = try #require(Self.body(capture, "private func releaseAudioSession()"))
        let gate = try #require(release.range(of: "if callGate() == nil {"))
        #expect(gate.lowerBound < (release.range(of: "session.deactivate()")?.lowerBound ?? release.startIndex), "a stop deactivates the call's session")
        #expect(gate.lowerBound < (release.range(of: "setPreferredInput(nil)")?.lowerBound ?? release.startIndex))
        let aloud = try #require(Self.body(capture, "func setPlaysAloud("))
        #expect(aloud.contains("callGate() == nil"), "Persona's speaking changes the call's category")
        let echo = try #require(Self.body(capture, "func turnOnEchoCancellation()"))
        #expect(echo.contains("callGate() == nil"))
        let configure = try #require(Self.body(capture, "private func configureSession(token:"))
        let search = try #require(configure.range(of: "await bluetoothInput("))
        let after = try #require(configure.range(of: "try checkCall()", range: search.upperBound..<configure.endIndex), "the Bluetooth search's wait is not followed by the gate")
        #expect(after.lowerBound < (configure.range(of: "setPreferredInput(port)")?.lowerBound ?? configure.startIndex))
        let start = try #require(Self.body(capture, "func start(locale:"))
        let asked = try #require(start.range(of: "await askPermission()"))
        #expect(start.range(of: "callGate()", range: asked.upperBound..<start.endIndex) != nil, "the permission's wait is not followed by the gate")
        #expect((start.range(of: "try checkCall()")?.lowerBound ?? start.endIndex) < (start.range(of: "try writer.start()")?.lowerBound ?? start.startIndex))
        let resume = try #require(Self.body(capture, "private func resume("))
        #expect((resume.range(of: "try self.checkCall()")?.lowerBound ?? resume.endIndex) < (resume.range(of: "try writer.resume()")?.lowerBound ?? resume.startIndex))

        let composer = try Self.lines("Persona/Views/Chat/PromptComposer.swift")
        let listen = try #require(Self.body(composer, "private func listen(_ turn:"))
        let permission = try #require(listen.range(of: "granted = await Self.allowed()"))
        let recheck = try #require(listen.range(of: "if let refusal = callGate() { return refusal }"))
        #expect(permission.upperBound < recheck.lowerBound, "dictation does not ask the gate after its permission")
        #expect(recheck.lowerBound < (listen.range(of: "RecordingMicrophone.shared.claim(")?.lowerBound ?? listen.startIndex))
        #expect(listen.contains("guard !LiveCallGate.liveNow else { return c.resume(returning: .call) }"))
        #expect(composer.joined(separator: "\n").contains("private static var audioQueue: DispatchQueue { LiveCallGate.sessionQueue }"))
        let calls = try Self.lines("Persona/Calls/PersonaCalls.swift")
        let category = try #require(Self.body(calls, "static func setCallCategory()"))
        #expect(category.contains("LiveCallGate.sessionQueue.async"), "the call's category is not on the session's queue")
        let guards = try Self.lines("Persona/Calls/CallGuards.swift").joined(separator: "\n")
        #expect(guards.contains("LiveCallGate.callLiveChanged(live)"))

        let entry = try Self.lines("Persona/Views/Voice/VoiceEntry.swift")
        let proceed = try #require(Self.body(entry, "static func proceed("))
        #expect(proceed.contains("return await begin(key, model: model, endingCall: true)"), "the yes does not wait for its refusal")
        let begin = try #require(Self.body(entry, "private static func prepare("))
        #expect(begin.contains("return .ready { await VoiceCalls.shared.start("), "a start's refusal is dropped")
        #expect(!entry.joined(separator: "\n").contains("Task { await VoiceCalls.shared.start("), "a start's refusal is dropped")
        let starts = try #require(Self.body(entry, "@discardableResult static func start("))
        #expect(starts.contains("if let why = await begin() { refused(why) }"))

        let signOut = try Self.lines("Persona/App/HubSignOut.swift")
        let signing = try #require(Self.body(signOut, "private func signingOut("))
        let refused = try #require(signing.range(of: "if let refusal = disconnectRefusal(hubs, scope: scope) {"))
        #expect(refused.lowerBound < (signing.range(of: "Self.signOut(hubs)")?.lowerBound ?? signing.startIndex), "credentials go before the call is asked")
        #expect(refused.lowerBound < (signing.range(of: "forget(self)")?.lowerBound ?? signing.startIndex))

        let system = try Self.lines("Persona/Calls/CallSystem.swift").joined(separator: "\n")
        #expect(!system.contains("templatePNG("), "the icon is drawn as the manager is made, on the main actor")
        let owner = try Self.lines("Persona/Calls/OwnerCall.swift")
        let place = try #require(Self.body(owner, "func place(_ call: PersonaCall"))
        let ready = try #require(place.range(of: "await readySystem()"))
        #expect(ready.lowerBound < (place.range(of: "let system = system")?.lowerBound ?? place.startIndex))
        #expect(place.contains("call.assistant.retake(assistant(), name: assistantName(), owner: assistantOwner())"),
                "the look and the top bar's name, together, for the same assistant only")

        // The call's pages draw only what the call's assistant has ready: nothing read or decoded in their bodies.
        for path in ["Persona/Calls/CallAvatar.swift", "Persona/Calls/CallScreen.swift"] {
            let text = try Self.lines(path).joined(separator: "\n")
            for loader in ["PersonaDot(", "PersonaAvatar(", "personas.frames(", "personas.avatar("] {
                #expect(!text.contains(loader), "\(path) uses \(loader)")
            }
        }

        let window = try Self.lines("Persona/Calls/CallWindow.swift")
        let follow = try #require(Self.body(window, "private func followRoom()"))
        #expect(follow.contains("guard Self.watchesRoom(bar: barShown, active: active) else {") && follow.contains("watching?.cancel()"))
        #expect(window.joined(separator: "\n").contains("UIApplication.willResignActiveNotification"))
    }
}
