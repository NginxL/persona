import SwiftUI

/// Settings › Assistant › Exact Approval Replies. The view model owns hub access so tests can exercise every write
/// against a FakeHub. Every write is checked against both the PUT response and a fresh GET; a stale or partial answer
/// restores the previous switch instead of presenting a local opt-in as saved.
@MainActor @Observable final class OwnerApprovalSettingsModel {
    struct Deps {
        var api: () -> APIClient?
        var saved: (APIClient.AssistantState) -> Void

        static var live: Deps {
            let model = AppModel.shared
            return Deps(api: { model.api }, saved: { model.assistantSettingsSaved($0) })
        }
    }

    enum Switch { case reply, persistent }

    private(set) var config: APIClient.AssistantState?
    var phraseText = ""
    private(set) var loading = true
    private(set) var saving = false
    private(set) var failure: String?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    var phrases: [String] {
        phraseText.components(separatedBy: .newlines)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
    }

    func load() async {
        guard let api = deps.api() else { loading = false; return }
        loading = true
        failure = nil
        do {
            let st = try await api.assistant()
            guard st.ownerReplyApproval != nil, st.persistentOwnerActionCards != nil, st.ownerReplyApprovalPhrases != nil else {
                config = nil
                loading = false
                return
            }
            config = st
            phraseText = st.ownerReplyApprovalPhrases?.joined(separator: "\n") ?? ""
            deps.saved(st)
        } catch {
            failure = error.localizedDescription
        }
        loading = false
    }

    func saveSwitch(_ field: Switch, on: Bool) async {
        guard !saving, let api = deps.api(), let before = config else { return }
        saving = true
        failure = nil
        var optimistic = before
        var body = APIClient.AssistantConfigBody()
        switch field {
        case .reply:
            optimistic.ownerReplyApproval = on
            body.ownerReplyApproval = on
        case .persistent:
            optimistic.persistentOwnerActionCards = on
            body.persistentOwnerActionCards = on
        }
        config = optimistic
        do {
            let answered = try await api.setAssistant(body)
            try verify(answered, field: field, on: on, manager: before.manager)
            let persisted = try await api.assistant()
            try verify(persisted, field: field, on: on, manager: before.manager)
            config = persisted
            phraseText = persisted.ownerReplyApprovalPhrases?.joined(separator: "\n") ?? phraseText
            deps.saved(persisted)
        } catch {
            config = before
            failure = error.localizedDescription
        }
        saving = false
    }

    private func verify(_ st: APIClient.AssistantState, field: Switch, on: Bool, manager: String?) throws {
        let saved = switch field {
        case .reply: st.ownerReplyApproval
        case .persistent: st.persistentOwnerActionCards
        }
        guard st.manager == manager, saved == on else {
            throw APIError(status: 502, message: String(localized: "The hub did not confirm this approval setting."))
        }
    }

    func savePhrases() async {
        guard !saving, let api = deps.api(), let before = config else { return }
        saving = true
        failure = nil
        let wanted = phrases
        var body = APIClient.AssistantConfigBody()
        body.ownerReplyApprovalPhrases = wanted
        do {
            let answered = try await api.setAssistant(body)
            guard answered.manager == before.manager, answered.ownerReplyApprovalPhrases == wanted else {
                throw APIError(status: 502, message: String(localized: "The hub did not confirm these approval phrases."))
            }
            let persisted = try await api.assistant()
            guard persisted.manager == before.manager, persisted.ownerReplyApprovalPhrases == wanted else {
                throw APIError(status: 502, message: String(localized: "The hub did not confirm these approval phrases."))
            }
            config = persisted
            phraseText = wanted.joined(separator: "\n")
            deps.saved(persisted)
        } catch {
            config = before
            failure = error.localizedDescription
        }
        saving = false
    }
}

struct OwnerApprovalSettingsView: View {
    @State private var page = OwnerApprovalSettingsModel()

    var body: some View {
        @Bindable var page = page
        List {
            if let approval = page.config?.ownerReplyApproval,
               let persistent = page.config?.persistentOwnerActionCards,
               let confirmed = page.config?.ownerReplyApprovalPhrases {
                Section {
                    Toggle(String(localized: "Approve a Batch with an Exact Reply"), isOn: Binding(
                        get: { page.config?.ownerReplyApproval ?? approval },
                        set: { on in Task { await page.saveSwitch(.reply, on: on) } }
                    ))
                    .accessibilityIdentifier("owner-reply-approval")
                    Toggle(String(localized: "Keep Unanswered Proposals Until Answered"), isOn: Binding(
                        get: { page.config?.persistentOwnerActionCards ?? persistent },
                        set: { on in Task { await page.saveSwitch(.persistent, on: on) } }
                    ))
                    .accessibilityIdentifier("persistent-owner-action-cards")
                } footer: {
                    Text("Off by default. An exact reply approves only the displayed original text, recipients and actions, once. Edits need fresh approval; this never creates an Always rule.")
                }
                Section {
                    TextEditor(text: $page.phraseText)
                        .scaledFrame(minHeight: Theme.Size.box110)
                        .disabled(page.saving)
                        .accessibilityIdentifier("owner-approval-phrases")
                    Button(String(localized: "Save Phrases")) { Task { await page.savePhrases() } }
                        .disabled(page.saving || page.phrases == confirmed)
                } header: {
                    Text("Approval Phrases")
                } footer: {
                    Text("One exact phrase per line. The whole reply must match. An empty list disables reply approval; ordinary Allow and Deny still work.")
                }
            }
            if let failure = page.failure {
                Section { Text(failure).foregroundStyle(.red) }
            }
        }
        .navigationTitle(String(localized: "Exact Approval Replies"))
        .disabled(page.saving)
        .task { await page.load() }
        .refreshable { await page.load() }
    }
}
