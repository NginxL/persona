import Foundation
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// One phone button (#9887, docs/apple/calls.md "One phone button"): the composer's phone icon is the one voice entry. On
/// the iPhone it places a system call to the conversation it is in, whose key the call carries and whose voice starts
/// bound to it; one fixed handle; a redial calls the main conversation; the call screen and the bar show the
/// conversation; a live call elsewhere asks first; where a system call can't be used, the voice chat in the app, which
/// switching away no longer ends.
@MainActor
struct CallOneButtonTests {
    private static func session(_ fields: String) -> Session {
        let json = #"{"machine_id":"m2","session_id":"s9","title":"修登录页","cwd":"/w/web","status":"idle","state":"idle"\#(fields)}"#
        return try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private static func code(_ path: String) throws -> String {
        let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        return try String(contentsOf: apple.appending(path: path), encoding: .utf8)
    }

    // MARK: The icon's choice

    /// The icon in any conversation on the iPhone (the main one, another assistant conversation, an Agent session) is a
    /// system call to it; on the iPad and Apple Vision Pro the voice chat in the app; a call already with this
    /// conversation shows its screen; one elsewhere asks first, and the yes places the call; an urgent call refuses.
    @Test func theIconsChoicePerContext() {
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: nil, endingCall: false, systemCalls: true) == .systemCall)
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: nil, endingCall: false, systemCalls: false) == .inApp)
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: "Persona", endingCall: false, systemCalls: true)
                == .ask(name: "Persona"))
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: "Persona", endingCall: true, systemCalls: true) == .systemCall)
        #expect(VoiceEntry.choose(urgent: false, ownCall: true, liveCall: "Persona", endingCall: false, systemCalls: true) == .showCall)
        #expect(VoiceEntry.choose(urgent: true, ownCall: false, liveCall: nil, endingCall: false, systemCalls: true) == .urgentCall)
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: "Persona", endingCall: false, systemCalls: false) == .inApp,
                "the in-app path asks its own question (endCallFirst)")
        #expect(VoiceEntry.placesSystemCall == (UIDevice.current.userInterfaceIdiom == .phone))
    }

    /// A live call and the icon in another conversation: "End the call with <name> and call <this>?", End and Call or
    /// Cancel; the yes calls this conversation.
    @Test func aLiveCallAndTheIconElsewhereAsks() {
        let key = SessionKey(group: "g", machineID: "m2", sessionID: "s9")
        let note = VoiceUnavailableNote.callElsewhereFirst("Persona", key: key, target: "修登录页")
        #expect(note.proceed == .callConversation(key))
        #expect(note.title.contains("Persona") && note.title.contains("修登录页"))
        #expect(note.proceed?.label == String(localized: "End and Call"))
        #expect(VoiceUnavailableNote.endCallFirst("Persona", key: key).proceed?.label == String(localized: "End Call and Start"))
    }

    // MARK: The call to a conversation

    /// The call's kind carries the conversation's key: it is that conversation's call from "Calling…" (its icon shows the
    /// call screen), not the main one's; the voice starts bound to it with `surface: "call"`.
    @Test func aCallCarriesItsConversationsKey() async throws {
        let rig = CallRig()
        let agent = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s9")
        let call = rig.calls.add(kind: .owner(group: agent.group, key: agent), direction: .outgoing)
        #expect(call.kind == .owner(group: agent.group, key: agent))
        #expect(rig.calls.isOwnerCallConversation(agent) && !rig.calls.isOwnerCallConversation(rig.voice.key))
        let deps = rig.calls.ownerDeps
        let engine = OwnerCallEngine(key: agent, extensionID: "voice", recent: [], audio: rig.voice.audio, calls: rig.voiceCalls,
                                     deps: { audio, events in deps(agent.group, audio, events) })
        rig.calls.attach(engine, to: call)
        await rig.calls.place(call, origin: .app)
        rig.system.act(.start, call.id)
        await rig.voice.waitUntil { rig.voice.socket != nil }
        let socket = try #require(rig.voice.socket)
        #expect(socket.start?["surface"] as? String == "call")
        #expect(socket.start?["target"] as? [String: String] == ["machine_id": "m2", "session_id": "s9"])
        #expect(rig.calls.isOwnerCallConversation(agent))
    }

    /// One fixed handle for every call: the assistant's name as its value whatever the conversation; the conversation's
    /// title only in its display name. A redial from Recents (any value) calls the main conversation.
    @Test func theHandleIsFixedAndARedialCallsTheMainConversation() async throws {
        let handle = CallHandle.outgoing(name: "Persona", kind: .generic, conversation: "修登录页")
        #expect(handle.value == "Persona" && handle.displayName == "Persona · 修登录页")
        #expect(CallHandle.outgoing(name: "Persona", kind: .generic).displayName == "Persona")

        let rig = CallRig()
        let agent = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s9")
        let call = rig.calls.add(kind: .owner(group: agent.group, key: agent), direction: .outgoing)
        call.conversation = CallConversation(title: "修登录页", detail: "Claude · MacBook", agent: AgentKind(Self.session("")))
        rig.attachEngine(call)
        await rig.calls.place(call, origin: .app)
        #expect(rig.system.startHandles.last == CallHandle(kind: .generic, value: "Persona", displayName: "Persona · 修登录页"))

        let owner = try Self.code("Persona/Calls/OwnerCall.swift")
        #expect(owner.contains("Task { await callPersona(origin: .recents) }"), "a redial calls Persona")
        #expect(owner.contains("await call(nil, group: group, origin: origin)"), "Persona is the main conversation")
    }

    /// The system refusing the Start: the voice chat in the app on the same conversation, with a line saying why.
    @Test func aRefusedStartFallsBackToTheVoiceChatWithANote() async {
        let rig = CallRig()
        let agent = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s9")
        let call = rig.calls.add(kind: .owner(group: agent.group, key: agent), direction: .outgoing)
        rig.attachEngine(call)
        rig.system.startErrors = [NSError(domain: CallSystemError.requestTransaction, code: 1)]
        var instead: CallSystemError?
        await rig.calls.place(call, origin: .app) { instead = $0 }
        #expect(instead == CallSystemError(domain: CallSystemError.requestTransaction, code: 1))
        #expect(call.phase == .ended(.failed) && call.note == PersonaCalls.insteadLine(instead!))
        let owner = try? Self.code("Persona/Calls/OwnerCall.swift")
        #expect(owner?.contains("self?.chatInstead(key, extensionID: extensionID, recent: recent, why: why)") == true,
                "the voice chat on the call's own conversation")
    }

    /// A call already on with another conversation ends only when the owner said End and Call; otherwise it is shown.
    @Test func anotherConversationsCallEndsOnlyOnTheYes() throws {
        let owner = try Self.code("Persona/Calls/OwnerCall.swift")
        let guarded = try #require(owner.range(of: "guard endingCall, let target, !isOwnerCallConversation(target) else {"))
        let ends = try #require(owner.range(of: "await endForVoiceChat()"))
        #expect(guarded.upperBound < ends.lowerBound)
    }

    /// End and Call while the call on still dials (the system has not handed its Start back: no audio yet), or is held
    /// with its audio gone (#9887 review): the hang-up's End comes back from the system a moment later, and only then is
    /// the new conversation called. Waiting for the audio alone returned at once, found the old call still on and showed
    /// it instead, so the new call was never placed and the End hung the old one up after.
    @Test func endAndCallWaitsForTheOldCallsEndWithoutItsAudio() async throws {
        for held in [false, true] {
            let rig = CallRig()
            let old: PersonaCall
            if held {
                let (call, _) = await rig.connected()
                rig.system.act(.pause(true), call.id)
                rig.system.deactivate()
                #expect(call.held && !rig.voice.audio.active, "held without its audio")
                old = call
            } else {
                old = await rig.dial()
                #expect(old.phase == .dialing && !rig.voice.audio.active)
            }
            let what = held ? "held" : "dialling"
            let other = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s9")
            let placing = Task { await rig.calls.callConversation(other, endingCall: true) }
            await rig.voice.waitUntil { rig.system.requests.contains(.end) }
            try await Task.sleep(for: .milliseconds(150))
            #expect(rig.calls.ownerCall === old && old.phase.isLive, "\(what): the new call before the old one ended")
            rig.system.act(.end, old.id)
            await placing.value
            #expect(old.phase == .ended(.hungUp), "\(what)")
            let next = try #require(rig.calls.ownerCall)
            // This process has no hub for the new call's group: it is refused there, after it was made for the conversation.
            #expect(next !== old && next.kind == .owner(group: other.group, key: other), "\(what): the new conversation was not called")
        }
    }

    // MARK: What the pages show

    /// The call screen and the bar show the conversation: an Agent session's title, its agent and machine, its mark; another
    /// assistant conversation's title under the assistant's look; the main conversation the assistant's look and name.
    @Test func thePagesShowTheConversation() {
        let agent = CallConversation.make(session: Self.session(#","agent":"claude""#), main: false, assistant: "Persona", machine: "MacBook")
        #expect(agent?.title == "修登录页" && agent?.detail == "Claude · MacBook" && agent?.agent?.brand == Theme.Brand.claude)
        let other = CallConversation.make(session: Self.session(#","assistant":true"#), main: false, assistant: "Persona", machine: nil)
        #expect(other?.title == "修登录页" && other?.detail == "Persona" && other?.agent == nil)
        #expect(CallConversation.make(session: Self.session(#","assistant":true"#), main: true, assistant: "Persona", machine: nil) == nil)
        #expect(CallConversation.make(session: nil, main: false, assistant: "Persona", machine: nil) == nil)
    }

    /// Another assistant's conversation opened without switching (from the Inbox, a notification, a link; #9887 review):
    /// the call names the assistant it belongs to under its title, as its top bar does, not the selected one.
    @Test func anotherAssistantsConversationNamesItsOwn() throws {
        let roster = ManagerRoster(managers: [
            HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true), // i18n-ignore: a test's names
            HubManager(id: "m-1", name: "工作", online: true), // i18n-ignore: a test's names
            HubManager(id: "m-2", name: "家里", online: true), // i18n-ignore: a test's names
        ], defaultID: "assistant")
        let hub = FakeHub()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "call-owner-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = roster
        let away = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"NAS","session_id":"c-away","title":"周末计划","state":"idle","assistant":true,"manager":"m-2"}"#.utf8))
        model.applySessions(Dictionary(grouping: [away], by: \.machineRef))
        model.selectAssistantManager("m-1")
        #expect(model.assistantShownName == "工作")
        let key = SessionKey(machineID: "NAS", sessionID: "c-away")
        let conversation = try #require(CallConversation.make(key, model: model))
        #expect(conversation.title == "周末计划" && conversation.detail == "家里", "the selected assistant named as its owner: \(conversation)")
        #expect(VoiceEntry.callTitle(key, model: model) == "周末计划")
    }

    /// The call's title is the conversation's (or the assistant's name for the main one); the question elsewhere names it.
    @Test func theCallsTitleIsItsConversations() async {
        let rig = CallRig()
        rig.assistant = "Mochi"
        let (call, _) = await rig.connected()
        #expect(call.title == "Mochi" && rig.calls.liveOwnerCallName == "Mochi")
        call.conversation = CallConversation(title: "修登录页", detail: nil, agent: nil)
        #expect(call.title == "修登录页" && call.name == "Mochi", "the handle stays the assistant's name")
        #expect(rig.calls.liveOwnerCallName == "修登录页")
    }

    /// The bar back to the phone's voice chat away from its page shows in the call's window; an urgent call's page covers it.
    @Test func theVoiceChatsBarShowsAwayFromItsPage() {
        #expect(CallWindowMode.make(presented: false, owner: nil, refusal: false, urgentUp: false, voiceAway: true) == .bar)
        #expect(CallWindowMode.make(presented: false, owner: nil, refusal: false, urgentUp: true, voiceAway: true) == .none)
        #expect(CallWindowMode.make(presented: false, owner: nil, refusal: false, urgentUp: false, voiceAway: false) == .none)
    }

    // MARK: The + rows and the in-app voice chat's survival

    /// The bar back to the voice chat asks its page to close what covers it (a full-screen picture, the terminal) and goes
    /// only when that page shows again, not at the tap (#9887 review: under a picture of the same conversation, opening it
    /// again changed nothing and the bar went, leaving the picture up).
    @Test func theBarBackClosesItsPagesCovers() throws {
        let calls = try Self.code("Persona/Calls/PersonaCalls.swift")
        let back = try #require(calls.range(of: "func returnToVoiceChat("))
        let body = String(calls[back.upperBound...].prefix(300))
        #expect(body.contains("voiceCalls.returnTo(chat.key)") && !body.contains("voiceCalls.returned("), "the bar goes at the tap")
        let chat = try Self.code("Persona/Views/Chat/ChatView.swift")
        #expect(chat.contains(".onChange(of: VoiceCalls.shared.returning == key) { _, back in if back { closeCovers() } }"))
        let close = try #require(chat.range(of: "private func closeCovers() {"))
        let covers = String(chat[close.upperBound...].prefix(300))
        #expect(covers.contains("viewingImages = nil") && covers.contains("showTerminal = false"))
        #expect(chat.contains(".onTabVisibility(appear: { [key] in VoiceCalls.shared.returned(key) }"), "the page showing takes the bar away")
    }

    /// No + row offers voice or a call; the in-app voice chat goes on in the background and away from its page on iOS.
    @Test func theRulesInTheSources() throws {
        let sheet = try Self.code("Persona/Views/Chat/AttachSheet.swift")
        #expect(!sheet.contains("\"composer-menu-voice\"") && !sheet.contains("\"composer-menu-call\""))
        #expect(VoiceCalls.keepsChatAway)
        let calls = try Self.code("Persona/Voice/VoiceCalls.swift")
        #expect(calls.contains("if !Self.keepsChatAway { end(.background, from: .phone) }"))
    }
}
