#if os(iOS)
import Foundation
import os

/// One answered urgent call (docs/assistant/urgent-call.md "Flow"), one turn at a time: Persona says what happened and
/// what it needs, the owner answers, Persona reads a decision back and acts on it only after "confirm", then asks if
/// there is anything else. A pending card of the call is answered as its card (voice approval counts only for that
/// card); words with no card go to the conversation as the owner's message. When it ends, the duration, what came of it
/// and what was said go to the manager, which keeps them with the conversation.
@MainActor @Observable final class UrgentCallSession: Identifiable {
    enum Phase: Equatable {
        case connecting // waiting for the call's audio and the matter
        case speaking
        case listening
        case confirming // a decision read back, waiting for "confirm"
        case ended
    }

    struct Line: Identifiable, Equatable {
        enum Who { case persona, owner }
        let id: Int
        let who: Who
        let text: String
    }

    /// A decision read back and waiting for the owner's "confirm".
    struct Pending: Equatable {
        var resolution: Resolution
        var said: String // what the decision is, in the owner's words for the transcript
    }

    let ring: UrgentCallRing
    var id: String { ring.id }
    private(set) var phase: Phase = .connecting
    private(set) var info: UrgentCallInfo?
    private(set) var card: Interaction?
    private(set) var lines: [Line] = []
    private(set) var pending: Pending?
    private(set) var answeredAt: Date?
    /// Listening is not allowed (microphone or speech recognition off): the owner taps the card's options instead.
    private(set) var tapOnly = false
    private(set) var outcome: String?
    /// The system's Mute (its call UI, AirPods; #9290): Persona still speaks, nothing is heard.
    private(set) var muted = false
    /// On hold (the system paused the call, or its audio was interrupted): nothing is said or heard until it is not.
    private(set) var held = false

    @ObservationIgnored private let voice: any UrgentCallSpeech
    /// Answers a card: `AppModel.resolve`, exactly as a tap on the card does, so the card is answered everywhere.
    @ObservationIgnored private let resolve: @MainActor (Interaction, Resolution) async -> Void
    @ObservationIgnored private var run: Task<Void, Never>?
    @ObservationIgnored private var nextLine = 0
    @ObservationIgnored private let ended: @MainActor (UrgentCallSession) -> Void
    /// The turns waiting for the call to be unmuted or off hold.
    @ObservationIgnored private var waiting: [CheckedContinuation<Void, Never>] = []
    /// Move on at every hold and every mute: a line a hold cut is said again after it, and what was being heard when
    /// either came is not the owner's answer (it is listened for again).
    @ObservationIgnored private var holdCount = 0
    @ObservationIgnored private var muteCount = 0
    private static let log = Logger(subsystem: "persona", category: "urgent-call")

    /// `card`: the call's card when it is known already (the tests); else `prepare` finds it among the pending ones.
    init(ring: UrgentCallRing, voice: any UrgentCallSpeech = UrgentCallVoice(), card: Interaction? = nil,
         resolve: @escaping @MainActor (Interaction, Resolution) async -> Void = { await AppModel.shared.resolve($0, $1) },
         ended: @escaping @MainActor (UrgentCallSession) -> Void) {
        self.ring = ring
        self.voice = voice
        self.card = card
        self.resolve = resolve
        self.ended = ended
    }

    /// The API of the call's hub and manager.
    private var api: APIClient? {
        AppModel.shared.api(ring.group)?.manager(ring.manager.isEmpty ? nil : ring.manager)
    }

    /// Reads the matter while the phone still rings, so it is there when the owner answers.
    func prepare() async {
        guard info == nil, let api else { return }
        do {
            info = try await api.urgentCall(ring.id)
            card = currentCard()
        } catch {
            Self.log.notice("urgent call \(self.ring.id, privacy: .public): could not read the matter: \(error.localizedDescription, privacy: .public)")
        }
    }

    /// The call's audio is on (the owner answered): the conversation starts.
    func start() {
        guard run == nil else { return }
        answeredAt = .now
        tapOnly = !voice.canHear
        run = Task { await talk() }
    }

    /// Muted or unmuted from the system: listening stops and waits for the mute to end; what Persona is saying goes on.
    func setMuted(_ on: Bool) {
        guard muted != on else { return }
        muted = on
        if on {
            muteCount += 1
            voice.stopListening()
        }
        wake()
    }

    /// On hold or off it: whatever is said or heard stops now, and the turns wait for the hold to end.
    func setHeld(_ on: Bool) {
        guard held != on else { return }
        held = on
        if on {
            holdCount += 1
            voice.stop()
        }
        wake()
    }

    /// Hold or mute changed: every waiting turn looks again whether it may go on (speaking needs only the hold off).
    private func wake() {
        let waiting = waiting
        self.waiting = []
        for w in waiting { w.resume() }
    }

    /// Returns once the call may speak (off hold) or, `listening`, listen (unmuted too), or at once if it may.
    private func untilFree(listening: Bool) async {
        while (held || (listening && muted)) && !Task.isCancelled && phase != .ended {
            await withCheckedContinuation { waiting.append($0) }
        }
    }

    /// The owner hung up, or the call ended on its own: whatever is said or heard stops, and the manager hears how it went.
    func hangUp() {
        guard phase != .ended else { return }
        run?.cancel()
        voice.stop()
        phase = .ended
        wake()
        let seconds = answeredAt.map { Int(Date.now.timeIntervalSince($0)) } ?? 0
        let transcript = lines.map { "\($0.who == .persona ? "Persona" : String(localized: "You")): \($0.text)" }.joined(separator: "\n")
        let outcome = outcome
        if let api {
            let id = ring.id
            Task {
                do {
                    try await api.reportUrgentCall(id, .ended, seconds: seconds, outcome: outcome, transcript: transcript)
                } catch {
                    Self.log.notice("urgent call \(id, privacy: .public): could not report its end: \(error.localizedDescription, privacy: .public)")
                }
            }
        }
        ended(self)
    }

    /// The owner tapped an option on the call page: read back, as if said.
    func choose(_ label: String) {
        guard let card else { return }
        offer(UrgentCallReply.option(label), card: card)
        run?.cancel()
        run = Task { await confirmPending() }
    }

    /// The owner tapped Approve or Reject on a permission or plan card: read back, as if said.
    func decide(allow: Bool) {
        guard let card else { return }
        offer(allow ? .allow : .deny, card: card)
        run?.cancel()
        run = Task { await confirmPending() }
    }

    /// The owner tapped Confirm on the read-back.
    func confirmTapped() {
        run?.cancel()
        run = Task { await act(.confirm) }
    }

    #if DEBUG
    /// `-showUrgentCallSample YES`: the page as it looks a little into a call, read from the mock hub, with no audio;
    /// `title` in the place of the hub's.
    func sample(title: String? = nil) async {
        await prepare()
        if let title { info?.title = title }
        answeredAt = .now.addingTimeInterval(-42)
        add(.persona, introduction())
        phase = .listening
    }
    #endif

    // MARK: The conversation

    private func talk() async {
        await prepare()
        await say(introduction())
        var silences = 0
        for _ in 0..<12 where !Task.isCancelled && phase != .ended {
            guard !tapOnly else { return } // the page waits for a tap
            let heard = await hearOwner()
            guard !Task.isCancelled, phase != .ended else { return }
            guard let heard else {
                silences += 1
                if silences >= 2 { break }
                await say(String(localized: "Are you still there? Say what you'd like to do, or say “hang up”."))
                continue
            }
            silences = 0
            add(.owner, heard)
            if await respond(to: heard) { break }
        }
        guard !Task.isCancelled else { return }
        await say(String(localized: "Okay. I'll take it from here. Goodbye."))
        hangUp()
    }

    /// Acts on what the owner said; true when the call should end.
    private func respond(to heard: String) async -> Bool {
        guard let card else {
            switch UrgentCallReply.answer(heard, options: [], approval: false) {
            case .hangUp: return true
            default:
                await tell(heard)
                await say(String(localized: "Got it, I've passed that on. Anything else?"))
                return false
            }
        }
        let reply = UrgentCallReply.answer(heard, options: optionLabels(card), approval: card.kind != .question)
        if reply == .hangUp { return true }
        offer(reply, card: card)
        if pending != nil {
            await confirmPending()
        } else {
            await tell(heard)
            await say(String(localized: "Got it, I've passed that on. Anything else?"))
        }
        return false
    }

    /// Reads the pending decision back and waits for "confirm".
    private func confirmPending() async {
        guard let pending else { return }
        phase = .confirming
        await say(String(localized: "You chose: \(pending.said). Say “confirm” to go ahead."))
        guard !tapOnly else { return }
        let heard = await hearOwner() ?? ""
        guard !Task.isCancelled, phase != .ended else { return }
        if !heard.isEmpty { add(.owner, heard) }
        await act(UrgentCallReply.readBack(heard))
    }

    private func act(_ reply: UrgentCallReply) async {
        guard let pending, let card else { return }
        self.pending = nil
        guard reply == .confirm else {
            await say(String(localized: "Okay, I didn't do it. What would you like instead?"))
            return
        }
        await resolve(card, pending.resolution)
        outcome = pending.said
        self.card = nil
        await say(String(localized: "Done. Anything else?"))
        if tapOnly { phase = .listening }
    }

    /// Turns a reply into the pending decision for card (nil when it names none).
    private func offer(_ reply: UrgentCallReply, card: Interaction) {
        switch (reply, card.kind) {
        case (.option(let label), .question):
            guard let question = card.questions?.first else { return }
            pending = Pending(resolution: Resolution(answers: [question.question: label]), said: QuestionOption.display(label).0)
        case (.allow, .permission):
            pending = Pending(resolution: Resolution(allow: true), said: String(localized: "Allow"))
        case (.deny, .permission):
            pending = Pending(resolution: Resolution(allow: false), said: String(localized: "Deny"))
        case (.allow, .plan):
            pending = Pending(resolution: Resolution(approve: true, mode: "acceptEdits"), said: String(localized: "Approve"))
        case (.deny, .plan):
            pending = Pending(resolution: Resolution(approve: false, feedback: ""), said: String(localized: "Reject"))
        case (.words(let text), .question) where !text.isEmpty:
            guard let question = card.questions?.first else { return }
            pending = Pending(resolution: Resolution(answers: [question.question: text]), said: text)
        default:
            pending = nil
        }
    }

    /// Sends the owner's words to the call's conversation, as their message.
    private func tell(_ text: String) async {
        guard let info, let machine = info.machineID, let conversation = info.conversationID, !text.isEmpty else { return }
        let key = SessionKey(group: ring.group, machineID: machine, sessionID: conversation)
        if await AppModel.shared.send(key, text: text, haptic: false) {
            outcome = outcome ?? text
        }
    }

    /// One turn of listening, once the call may listen: what the owner said (nil: nothing). Muted or held while it
    /// listened, what it had is not their answer: it listens again once it may.
    private func hearOwner() async -> String? {
        while !Task.isCancelled, phase != .ended {
            await untilFree(listening: true)
            guard !Task.isCancelled, phase != .ended else { return nil }
            phase = .listening
            let (holds, mutes) = (holdCount, muteCount)
            let heard = await voice.listen()
            if holds == holdCount, mutes == muteCount { return heard }
        }
        return nil
    }

    /// Says `text` once the call is off hold (muted, Persona still speaks). A hold that cuts it: said again, whole, after.
    private func say(_ text: String) async {
        var shown = false
        while !Task.isCancelled, phase != .ended {
            await untilFree(listening: false)
            guard !Task.isCancelled, phase != .ended else { return }
            phase = .speaking
            if !shown {
                add(.persona, text)
                shown = true
            }
            let holds = holdCount
            await voice.say(text)
            if holds == holdCount { return }
        }
    }

    private func add(_ who: Line.Who, _ text: String) {
        lines.append(Line(id: nextLine, who: who, text: text))
        nextLine += 1
    }

    /// What Persona says first: what happened, and what it needs.
    func introduction() -> String {
        var parts = [String(localized: "This is Persona, calling about: \(info?.title ?? ring.title).")]
        if let what = info?.happening ?? info?.body, !what.isEmpty { parts.append(what) }
        if let card {
            switch card.kind {
            case .question:
                if let q = card.questions?.first {
                    parts.append(q.question)
                    let options = optionLabels(card).map { QuestionOption.display($0).0 }
                    if !options.isEmpty { parts.append(String(localized: "You can say: \(options.formatted(.list(type: .or))).")) }
                }
            case .permission, .plan:
                parts.append(card.summary ?? card.toolDisplayName ?? "")
                parts.append(String(localized: "Say “approve” or “reject”."))
            default:
                parts.append(String(localized: "It's waiting on your phone."))
            }
        } else if tapOnly {
            parts.append(String(localized: "The details are on your screen."))
        } else {
            parts.append(String(localized: "What would you like to do?"))
        }
        // Chinese and Japanese sentences run on without a space; each part ends as a sentence.
        let language = DictationLanguage.language(of: LanguageStore.shared.launched) // "zh-Hans", "ja", "en"
        let spaced = !(language.hasPrefix("zh") || language == "ja")
        return parts.map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
            .map { Self.sentence($0, spaced: spaced) }.joined(separator: spaced ? " " : "")
    }

    /// `text` ending as a sentence: a full stop added when it ends without one (the assistant's line often does).
    nonisolated static func sentence(_ text: String, spaced: Bool) -> String {
        guard let last = text.last, !".。!！?？".contains(last) else { return text } // i18n-ignore: punctuation
        return text + (spaced ? "." : "。") // i18n-ignore: punctuation
    }

    private func optionLabels(_ card: Interaction) -> [String] {
        card.questions?.first?.options.map(\.label) ?? []
    }

    /// The call's card while it is still pending in the app.
    private func currentCard() -> Interaction? {
        guard let id = info?.interactionID, !id.isEmpty else { return nil }
        return AppModel.shared.interactions.values.first { $0.id == id && $0.group == ring.group }
    }
}
#endif
