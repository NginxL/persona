import AppIntents
import Foundation

/// "Send Clipboard to Computer" (#3064): the phone's clipboard, or the text a shortcut passes in, onto a computer's
/// clipboard. Reading the clipboard from a shortcut may ask "Allow Paste?"; a shortcut that starts with Get Clipboard and
/// passes it as Text never asks.
struct SendClipboardIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Send Clipboard to Computer"
    nonisolated static let description = IntentDescription(
        "Puts this phone's clipboard, or the text you pass in, on a computer's clipboard. The computer needs clipboard sharing turned on in Persona.",
        categoryName: "Clipboard",
        searchKeywords: ["Clipboard", "Copy", "Paste"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Computer")
    var machine: MachineEntity?

    @Parameter(title: "Text", description: "Sent instead of the clipboard", inputOptions: String.IntentInputOptions(multiline: true))
    var text: String?

    nonisolated init() {}
    nonisolated init(machine: MachineEntity?, text: String? = nil) {
        self.machine = machine
        self.text = text
    }

    nonisolated static var parameterSummary: some ParameterSummary {
        Summary("Send the clipboard to \(\.$machine)") { \.$text }
    }

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog {
        let target = try await ClipboardIntentFlow.target(machine)
        let content: ClipboardContent?
        if let text = TodoIntentText.cleaned(text) { content = .text(text) } else { content = await MainActor.run { PhonePasteboard.live.read() } }
        let api = try ClipboardIntentFlow.client(target)
        let done = try await ClipboardIntentFlow.send(api, content, to: target)
        return .result(dialog: IntentSupport.dialog(done))
    }
}

/// "Get Computer's Clipboard": a computer's clipboard onto the phone's, and its text as the result for the next action.
struct GetClipboardIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Get Computer's Clipboard"
    nonisolated static let description = IntentDescription(
        "Copies a computer's clipboard (text or an image) to this phone and returns the text. The computer needs clipboard sharing turned on in Persona.",
        categoryName: "Clipboard",
        searchKeywords: ["Clipboard", "Copy", "Paste"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Computer")
    var machine: MachineEntity?

    nonisolated init() {}
    nonisolated init(machine: MachineEntity?) { self.machine = machine }

    nonisolated static var parameterSummary: some ParameterSummary {
        Summary("Get the clipboard of \(\.$machine)")
    }

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        let target = try await ClipboardIntentFlow.target(machine)
        let api = try ClipboardIntentFlow.client(target)
        let content = try await ClipboardIntentFlow.fetch(api, from: target)
        await MainActor.run { PhonePasteboard.live.write(content) }
        let said = ClipboardOutcome.fetched(content.kind).message(machine: target.name)
        return .result(value: content.text ?? "", dialog: IntentSupport.dialog(said))
    }
}

/// The network part of the clipboard intents, apart from the pairing and the pasteboard so the tests can hand it a
/// FakeHub client. Every failure comes back as one sentence Siri can read.
nonisolated enum ClipboardIntentFlow {
    /// The computer asked for, else the one last sent to or the first that shares its clipboard.
    static func target(_ picked: MachineEntity?) async throws -> MachineEntity {
        if let picked { return picked }
        let machines: [Machine]
        do { machines = try await IntentSupport.machines() } catch { throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host())) }
        guard let m = ClipboardOffer.target(machines, preferred: UserDefaults.standard.string(forKey: ClipboardOffer.lastMachineKey)) else {
            throw IntentFailureSentence(noComputer)
        }
        return MachineEntity(m)
    }

    static func client(_ machine: MachineEntity) throws -> APIClient {
        do { return try IntentSupport.client(group: machine.ref.group) } catch { throw IntentFailureSentence(IntentFailure.sentence(for: error)) }
    }

    /// Sends; the sentence to say when done.
    static func send(_ api: APIClient, _ content: ClipboardContent?, to machine: MachineEntity) async throws -> String {
        guard let content, content.kind == .text || content.kind == .image else {
            throw IntentFailureSentence(ClipboardOutcome.nothingToSend.message(machine: machine.name))
        }
        do {
            try await api.setClipboard(machine: machine.ref.id, content)
        } catch {
            throw IntentFailureSentence(failure(error, machine: machine.name))
        }
        return ClipboardOutcome.sent(content.kind).message(machine: machine.name)
    }

    /// The computer's clipboard; empty or hidden content is a failure (nothing to put on the phone).
    static func fetch(_ api: APIClient, from machine: MachineEntity) async throws -> ClipboardContent {
        let content: ClipboardContent
        do {
            content = try await api.clipboard(machine: machine.ref.id)
        } catch {
            throw IntentFailureSentence(failure(error, machine: machine.name))
        }
        switch content.kind {
        case .text where content.text?.isEmpty == false, .image where content.image?.bytes != nil: return content
        case .concealed: throw IntentFailureSentence(ClipboardOutcome.remoteConcealed.message(machine: machine.name))
        default: throw IntentFailureSentence(ClipboardOutcome.remoteEmpty.message(machine: machine.name))
        }
    }

    static let noComputer = String(localized: "No computer shares its clipboard yet. Turn on clipboard sharing on a computer's page in Persona.")

    /// The hub's clipboard refusals in their own words; anything else as the other intents say it.
    static func failure(_ error: Error, machine name: String) -> String {
        if let api = error as? APIError, [400, 403, 409, 413, 502, 503].contains(api.status) { return ClipboardFailure(error).sentence(machine: name) }
        return IntentFailure.sentence(for: error, host: IntentSupport.host())
    }
}
