import BeautifulUI
import SwiftUI
import UIKit

// How a conversation's page gets into a voice call and back out of one (docs/assistant/voice.md 6.1, 1.2 L, 1.3): starting it from
// the composer's button or the 「+」 menu's Voice Chat, what the menu says where there is none, Open Settings, and the line left
// where the bar was after a call ended by itself (「语音已结束 · 继续」).

extension VoiceSupport {
    /// The group's voice extension, running or not (for Open Settings when it is not ready yet).
    func voiceExtensionID(group: String) -> String? {
        probes[group]?.extensions.first { $0.kind == "voice" && VoiceAvailability.validID($0.id) }?.id
    }
}

@MainActor enum VoiceEntry {
    /// Who works in the conversation `key`, for the bar's words: the agent (「Claude」, 「Codex」…), or in the assistant's
    /// own conversations its name as their top bar says it (#9887: one name, the call's too).
    static func label(_ key: SessionKey, model: AppModel) -> String {
        guard let session = model.session(key) else { return "Claude" }
        return session.assistant ? model.assistantShownName(in: key) : session.agentLabel
    }

    /// The phone icon places a system call to its conversation on the iPhone (#9887 one button: the one voice entry, the
    /// system picks the kind); the iPad and Apple Vision Pro have the voice chat in the app (the Mac and the watch have
    /// no voice).
    static var placesSystemCall: Bool {
        #if os(iOS)
        UIDevice.current.userInterfaceIdiom == .phone
        #else
        false
        #endif
    }

    /// The composer's phone icon in `key`'s conversation (#9887 one button): on the iPhone a system call to it
    /// (`PersonaCalls.callConversation`), elsewhere the voice chat in the app; otherwise what to say about it. Not while
    /// Persona's urgent call rings or is on (#9290): its audio is the system's. While a system call is on (#9290
    /// follow-up: nothing in the app ends it by itself): in its own conversation the call screen comes up instead (it is
    /// the same conversation); elsewhere the user is asked first ("End the call with … and call …?",
    /// `VoiceUnavailableNote.callElsewhereFirst`, or "… and start a voice chat here?" where the icon is the voice chat:
    /// `endCallFirst`), and `endingCall` is their yes. `inApp`: the voice chat in the app whatever the device (DEBUG
    /// `-openVoice`). What can be said at once is returned; why the start was refused after its wait (an urgent call rang
    /// while the system call let its audio go, a system call came meanwhile) goes to `refused`, once the start has run
    /// (`begin`).
    @discardableResult static func start(_ key: SessionKey, model: AppModel, endingCall: Bool = false, inApp: Bool = false,
                                         refused: @escaping @MainActor (VoiceUnavailableNote) -> Void) -> VoiceUnavailableNote? {
        switch prepare(key, model: model, endingCall: endingCall, inApp: inApp) {
        case .said(let note): return note
        case .ready(let begin):
            Task { if let why = await begin() { refused(why) } }
            return nil
        }
    }

    /// `start`, awaited: what to say, at once or after the start ran (VoiceCalls' own refusal after its hand-off), or nil
    /// when the voice chat or the system call started, or the call screen came up instead.
    static func begin(_ key: SessionKey, model: AppModel, endingCall: Bool = false) async -> VoiceUnavailableNote? {
        switch prepare(key, model: model, endingCall: endingCall, inApp: false) {
        case .said(let note): return note
        case .ready(let begin): return await begin()
        }
    }

    /// What the phone icon in `key`'s conversation says to VoiceOver (#9887 review): what its tap does there now, as
    /// `choose` picks it. The call screen while a call is with this conversation; on the iPhone a system call to it (asked
    /// first while a call is on elsewhere: the question says so); elsewhere the voice chat in the app.
    static func iconLabel(_ key: SessionKey) -> String {
        #if os(iOS)
        iconLabel(ownCall: PersonaCalls.shared.isOwnerCallConversation(key), systemCalls: placesSystemCall)
        #else
        iconLabel(ownCall: false, systemCalls: false)
        #endif
    }

    nonisolated static func iconLabel(ownCall: Bool, systemCalls: Bool) -> String {
        if ownCall { return String(localized: "Show Call") }
        return systemCalls ? String(localized: "Call This Conversation") : String(localized: "Start Voice Chat")
    }

    /// What the page calls `key`'s conversation in the question before a call to it (#9887): the assistant's name for
    /// the main conversation, else its title.
    static func callTitle(_ key: SessionKey, model: AppModel) -> String {
        #if os(iOS)
        if let conversation = CallConversation.make(key, model: model) { return conversation.title }
        #endif
        return model.assistantShownName(in: key)
    }

    /// The voice chat in the app that is on in another conversation than `key` (the fallback, which survives leaving its
    /// page, #9887), as the page names it, else nil: starting one here would end it, so the page asks first.
    static func voiceChatElsewhere(_ key: SessionKey, model: AppModel) -> String? {
        guard let chat = VoiceCalls.shared.current, chat.surface != .call, !chat.phase.isEnded, chat.key != key else { return nil }
        return callTitle(chat.key, model: model)
    }

    /// What a start does: says something at once (nil: nothing to say, the call screen came up), or is ready to begin
    /// the voice chat, which says why not when it is refused after all.
    private enum Prepared {
        case said(VoiceUnavailableNote?)
        case ready(@MainActor () async -> VoiceUnavailableNote?)
    }

    /// What the phone icon does in a conversation, from where things are (pure, for the tests; #9887 one button).
    nonisolated enum Choice: Equatable, Sendable {
        /// Persona's urgent call rings or is on: refused.
        case urgentCall
        /// A system call is on with this conversation: its call screen.
        case showCall
        /// A system call is on with another conversation (`name`): the question first.
        case ask(name: String)
        /// A system call to this conversation (the iPhone).
        case systemCall
        /// The voice chat in the app (the iPad, Apple Vision Pro, DEBUG `-openVoice`); with a system call on elsewhere it
        /// asks first (`endCallFirst`), as before.
        case inApp
        /// The voice chat in the app is on in another conversation (`name`, #9887): the question first, before a voice
        /// chat here (`call` false) or a system call to this conversation (`call` true) would end it.
        case askVoiceChat(name: String, call: Bool)
    }

    /// `voiceChat`: the voice chat in the app that is on in another conversation, by name (`voiceChatElsewhere`);
    /// `endingCall`: the owner said yes to ending what is on (a call, or that voice chat).
    nonisolated static func choose(urgent: Bool, ownCall: Bool, liveCall: String?, voiceChat: String? = nil, endingCall: Bool,
                                   systemCalls: Bool) -> Choice {
        if urgent { return .urgentCall }
        if ownCall { return .showCall }
        if systemCalls, !endingCall, let liveCall { return .ask(name: liveCall) }
        if !endingCall, liveCall == nil, let voiceChat { return .askVoiceChat(name: voiceChat, call: systemCalls) }
        return systemCalls ? .systemCall : .inApp
    }

    private static func prepare(_ key: SessionKey, model: AppModel, endingCall: Bool, inApp: Bool) -> Prepared {
        #if os(iOS)
        let calls = PersonaCalls.shared
        switch choose(urgent: calls.urgentInProgress, ownCall: calls.isOwnerCallConversation(key), liveCall: calls.liveOwnerCallName,
                      voiceChat: voiceChatElsewhere(key, model: model), endingCall: endingCall, systemCalls: !inApp && placesSystemCall) {
        case .urgentCall:
            return .said(PersonaCalls.urgentNote)
        case .showCall:
            calls.present()
            return .said(nil)
        case .ask(let name):
            return .said(.callElsewhereFirst(name, key: key, target: callTitle(key, model: model)))
        case .askVoiceChat(let name, let call):
            return .said(.endVoiceChatFirst(name, key: key, call: call ? callTitle(key, model: model) : nil))
        case .systemCall:
            // The same rules as the voice chat's decide whether this conversation can be called; the call itself asks
            // again once the hub answered (`PersonaCalls.callConversation`).
            let availability = VoiceSupport.shared.availability(for: model.session(key), group: key.group)
            guard availability.isAvailable else {
                return .said(VoiceUnavailableNote.make(availability, extensionID: VoiceSupport.shared.voiceExtensionID(group: key.group)))
            }
            Haptics.tap()
            return .ready {
                await calls.callConversation(key, endingCall: endingCall)
                return nil
            }
        case .inApp:
            if !endingCall, let name = calls.liveOwnerCallName { return .said(.endCallFirst(name, key: key)) }
        }
        #else
        // Apple Vision Pro: the voice chat in the app, asked first while one is on elsewhere (#9887).
        if !endingCall, let name = voiceChatElsewhere(key, model: model) { return .said(.endVoiceChatFirst(name, key: key)) }
        #endif
        let availability = VoiceSupport.shared.availability(for: model.session(key), group: key.group)
        guard case .available(let id) = availability else {
            return .said(VoiceUnavailableNote.make(availability, extensionID: VoiceSupport.shared.voiceExtensionID(group: key.group)))
        }
        Haptics.tap()
        let recent = VoiceLine.recent(model.chat(key).items)
        var deps = VoiceDeps.live(group: key.group)
        #if DEBUG
        if VoiceSample.silentAudio { deps.audio = { VoiceSample.SilentAudio() } }
        #endif
        // A call to Persona that is on (the user said end it) ends first, and the voice chat starts once the system let
        // its audio go (`VoiceCalls.endSystemCall`); an urgent call that rang meanwhile refuses it then.
        return .ready { await VoiceCalls.shared.start(key, extensionID: id, surface: .phone, recent: recent, deps: deps, endingCall: endingCall) }
    }

    /// The user said yes to `note`'s question (End Call and Start): the call to Persona ends and the voice chat starts or
    /// is picked up again. What to say if it cannot after all, awaited to the end: a refusal that comes after the call
    /// ended (an urgent call rang while its audio went) is said too.
    static func proceed(_ note: VoiceUnavailableNote, model: AppModel) async -> VoiceUnavailableNote? {
        switch note.proceed {
        case .startVoice(let key): return await begin(key, model: model, endingCall: true)
        case .resumeVoice(let key): return await VoiceCalls.shared.resume(key, endingCall: true)
        case .callConversation(let key), .replaceVoice(let key): return await begin(key, model: model, endingCall: true)
        case nil: return nil
        }
    }

    /// Open Settings. The voice model's use opens over the page (`modelConfigurationSheet`); the caller does that one.
    static func open(_ place: VoiceSettingsPlace, model: AppModel) {
        switch place {
        case .extensions:
            model.settingsPath = NavigationPath([SettingsPage.plugins])
            model.selectedTab = .settings
        case .voiceExtension(let id):
            // The default assistant's copy, which calls talk to (`VoiceSupport` reads it naming none), whichever is current.
            model.settingsPath = NavigationPath([SettingsPage.plugins, SettingsPage.plugin(id, manager: model.settingsRoute(nil))])
            model.selectedTab = .settings
        case .model:
            break
        case .system:
            if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
        }
    }
}

extension View {
    /// The 「+」 menu's Voice Chat where the page offers no call: why, and Open Settings when there is something to set.
    /// A question (`proceed`: a call to Persona is on) asks End Call and Start, or Cancel, which keeps the call.
    func voiceUnavailableAlert(_ note: Binding<VoiceUnavailableNote?>, model: AppModel) -> some View {
        alert(note.wrappedValue?.title ?? "", isPresented: Binding(get: { note.wrappedValue != nil }, set: { if !$0 { note.wrappedValue = nil } }),
              presenting: note.wrappedValue) { shown in
            if let proceed = shown.proceed {
                Button(proceed.label, role: .destructive) {
                    Task {
                        // A reason it still cannot (the hub went meanwhile) comes once this alert has gone.
                        guard let why = await VoiceEntry.proceed(shown, model: model) else { return }
                        try? await Task.sleep(for: Theme.Motion.alertHandoff)
                        note.wrappedValue = why
                    }
                }
                .accessibilityIdentifier("voice-end-call-and-start")
                Button("Cancel", role: .cancel) {}
                    .accessibilityIdentifier("voice-keep-call")
            } else {
                if let place = shown.settings {
                    Button("Open Settings [Voice Entry 93112c]") { VoiceEntry.open(place, model: model) }
                }
                Button("OK [In App Tunnel View f867f3]", role: .cancel) {}
            }
        } message: { shown in
            Text(shown.message)
        }
    }
}

/// Where the bar was, after a call ended by itself (docs/assistant/voice.md 1.2 L): Voice Ended, why, and Continue (or Retry), Open Settings,
/// Details as the ending allows. It stays until the page goes; Continue picks the call up with its captions and what it
/// handed over (`VoiceCalls.resume`), and the voice says what finished meanwhile.
struct VoiceEndedStrip: View {
    let call: VoiceConversation
    let note: VoiceEndNote
    @Environment(AppModel.self) private var model
    @State private var showDetails = false
    @State private var showModel = false
    /// Why Continue did not pick the call up (Persona's urgent call is on, #9290).
    @State private var refusal: VoiceUnavailableNote?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.voiceChannel).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                (call.surface == .call ? Text("Call ended · \(note.text)") : Text("Voice ended · \(note.text)"))
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .accessibilityIdentifier("voice-ended-text")
                Spacer(minLength: 0)
            }
            HStack(spacing: Theme.Space.m) {
                if let again = note.again {
                    Button {
                        Haptics.tap()
                        #if os(iOS)
                        // A system call that ended is not picked up again: its conversation is called again (a new
                        // system call, #9887 one button).
                        if call.surface == .call {
                            let key = call.key
                            Task { await PersonaCalls.shared.callConversation(key) }
                            return
                        }
                        #endif
                        #if os(iOS)
                        // A call to Persona that is on: asked first, never ended by itself.
                        if let name = PersonaCalls.shared.liveOwnerCallName {
                            refusal = .endCallFirst(name, key: call.key, again: true)
                            return
                        }
                        #endif
                        Task { refusal = await VoiceCalls.shared.resume(call.key) }
                    } label: {
                        // Calling Persona again is Call Persona's glyph, not the in-app voice chat's.
                        Label(again == .resume ? String(localized: "Continue") : String(localized: "Retry [Components b8784c]"),
                              systemImage: call.surface == .call ? Theme.Icon.callPersona : Theme.Icon.voiceChannel)
                            .lineLimit(1)
                    }
                    .accessibilityIdentifier("voice-continue")
                }
                if let place = note.settings {
                    Button {
                        if case .model = place { showModel = true } else { VoiceEntry.open(place, model: model) }
                    } label: {
                        Label("Open Settings [Voice Entry 93112c]", systemImage: Theme.Icon.settings).lineLimit(1)
                    }
                    .accessibilityIdentifier("voice-settings")
                }
                if note.details != nil {
                    Button { showDetails = true } label: { Label("Details [Readiness Card 979a33]", systemImage: Theme.Icon.info).lineLimit(1) }
                        .accessibilityIdentifier("voice-details")
                }
                Spacer(minLength: 0)
            }
            .font(Theme.Font.secondaryStrong)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("voice-ended")
        .onAppear {
            VoiceHaptics.play(note.haptic)
            AccessibilityNotification.Announcement(String(localized: "Voice Ended")).post()
        }
        .alert("Details [Readiness Card 979a33]", isPresented: $showDetails) {
            Button("OK [In App Tunnel View f867f3]", role: .cancel) {}
        } message: {
            Text(note.details ?? "")
        }
        .voiceUnavailableAlert($refusal, model: model)
        // The default assistant's: calls talk to its voice extension.
        .modelConfigurationSheet(isPresented: $showModel, purposeID: "plugin." + call.extensionID, owner: .defaultAssistant)
    }
}

extension PromptItems {
    /// Voice Chat in the platform's BeautifulUI prompt bar, beside its attachments (docs/assistant/voice.md 6.1).
    static var voice: PromptSource {
        PromptSource(id: "voice", name: String(localized: "Voice Chat"), detail: String(localized: "What you say goes to this chat"),
                     icon: .symbol(Theme.Icon.voiceChannel), kind: .attach)
    }
}

/// When the composer asks the group's hub about voice again: another group, or the app back in front.
nonisolated struct VoiceProbeID: Hashable, Sendable {
    var group: String
    var active: Bool
}

/// The composer's side of voice (docs/assistant/voice.md 6.1, 6.8): asks the group's hub whether it has a voice extension that
/// speaks protocol 2 (3.1; at most once a minute, the entry keeps the last answer meanwhile, `VoiceSupport`), says why the
/// 「+」 menu's Voice Chat cannot start one, and in DEBUG the screenshot switches (`VoiceSample`).
struct ComposerVoice: ViewModifier {
    let key: SessionKey
    @Binding var note: VoiceUnavailableNote?
    /// The app is in front.
    let active: Bool
    /// The page offers a call now.
    let offered: () -> Bool
    let start: () -> Void
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content
            .tabActiveTask(id: VoiceProbeID(group: key.group, active: active)) {
                guard active, VoicePlatform.current == .phone, let api = model.api(key.group) else { return }
                await VoiceSupport.shared.refresh(group: key.group, api: api)
            }
            .voiceUnavailableAlert($note, model: model)
            #if DEBUG
            .task {
                guard let request = VoiceSample.request else { return }
                try? await Task.sleep(for: .seconds(2)) // past the launch's back and forth: the page that stays takes it
                // The real path against the hub (the mock's MOCK_VOICE=1): as soon as the page offers a call.
                if request == .open { for _ in 0..<30 where !offered() && !Task.isCancelled { try? await Task.sleep(for: .seconds(1)) } }
                guard !Task.isCancelled, VoiceSample.claim() else { return }
                switch request {
                case .open: start()
                case .sample(.setup): note = VoiceSample.setupNote
                case .sample(let sample): VoiceSample.start(sample, key: key, model: model)
                }
            }
            #endif
    }
}
