import SwiftUI

/// The current assistant's saved prompts (#9436, docs/apple/ios.md "Saved prompts"): what every chat's "/" list offers
/// after its own commands, and what Assistant Profile › Saved Prompts shows and changes. One list for both, read again on
/// hello, on `assistant_saved_prompts` (another device changed it, only the shown assistant's) and when another
/// assistant becomes the current one; until it is in, the "/" list has none. The hub's routes are the web's
/// (docs/protocol/protocol.md, `/api/v1/assistant/saved-prompts`).
@MainActor @Observable final class SavedPromptStore {
    static let shared = SavedPromptStore(deps: .live)

    private(set) var prompts: [SavedPrompt] = []
    /// A read has answered for the assistant shown (the page shows its empty state only then).
    private(set) var loaded = false
    /// False on an assistant from before saved prompts (404): the page says so and the "/" list has none.
    private(set) var supported = true
    /// Why the latest read failed; the page shows it only when it has no list to show.
    private(set) var error: String?
    /// The prompts a delete or a move is on its way for (their rows wait).
    private(set) var pending: Set<String> = []

    @ObservationIgnored private let deps: AssistantDeps
    /// The latest read: an older one that answers after it does not land, nor one from before a change made here.
    @ObservationIgnored private var reads = 0
    /// Which assistant the list is: `forget()` moves it on, and a change that answers after that leaves the list alone
    /// (the assistant before's prompts would show in the new one's chats).
    @ObservationIgnored private var epoch = 0
    /// Prompts deleted here (on their way or done): a move that answers after a delete does not bring one back.
    @ObservationIgnored private var deleted: Set<String> = []

    init(deps: AssistantDeps) { self.deps = deps }

    /// Reads the list again (hello, the event, the page's pull to refresh); a read already under way is overtaken.
    @discardableResult
    func reload() -> Task<Void, Never> { Task { await load() } }

    func load() async {
        guard let api = deps.api() else { return }
        reads += 1
        let read = reads
        do {
            let list = try await api.savedPrompts()
            guard read == reads else { return }
            prompts = list
            supported = true
            error = nil
        } catch {
            guard read == reads else { return }
            if TodosLogic.isOldHub(error) {
                prompts = []
                supported = false
                self.error = nil
            } else {
                self.error = error.shown ?? self.error
            }
        }
        loaded = true
    }

    /// Another hub or assistant: nothing of the one before stays (its list would be offered in the wrong chats).
    func forget() {
        reads += 1
        epoch += 1
        deleted = []
        prompts = []
        loaded = false
        supported = true
        error = nil
        pending = []
    }

    /// Adds a prompt (`id` nil) or changes one; the hub's refusal is thrown so the editor keeps what was typed.
    @discardableResult
    func save(id: String?, name: String, text: String) async throws -> SavedPrompt {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub.")) }
        let mine = epoch
        let saved = try await api.saveSavedPrompt(id: id, name: SavedPromptRules.cleanName(name), text: text)
        guard mine == epoch else { return saved }
        reads += 1 // a read under way from before this change would put the old list back
        if let i = prompts.firstIndex(where: { $0.id == saved.id }) { prompts[i] = saved } else { prompts.append(saved) }
        return saved
    }

    /// Moves a prompt one place up (-1) or down (+1): at once on screen, back with the reason if the hub refuses. A list
    /// that changed elsewhere in the meantime (409) is read again.
    func move(_ id: String, by step: Int) async {
        guard let api = deps.api(), let from = prompts.firstIndex(where: { $0.id == id }) else { return }
        let to = from + step
        guard prompts.indices.contains(to), pending.isEmpty else { return }
        let before = prompts, mine = epoch, deletedBefore = deleted
        prompts.swapAt(from, to)
        pending.insert(id)
        reads += 1
        defer { pending.remove(id) }
        do {
            let answer = try await api.orderSavedPrompts(prompts.map(\.id))
            guard mine == epoch else { return }
            prompts = answer.filter { !deleted.contains($0.id) }
        } catch {
            guard mine == epoch else { return }
            prompts = before.filter { !deleted.contains($0.id) }
            if (error as? APIError)?.status == 409 {
                // a delete made here meanwhile changed the ids the order was sent with: nothing to tell
                if deleted == deletedBefore {
                    deps.toast(Toast(String(localized: "The list changed on another device. Here it is as it is now.")))
                }
                await load()
            } else {
                deps.report(error)
            }
        }
    }

    /// Deletes a prompt: gone at once, back in its place with the reason if the hub refuses.
    func delete(_ id: String) async {
        guard let api = deps.api(), let at = prompts.firstIndex(where: { $0.id == id }) else { return }
        let removed = prompts.remove(at: at), mine = epoch
        pending.insert(id)
        deleted.insert(id)
        reads += 1
        defer { pending.remove(id) }
        do {
            try await api.deleteSavedPrompt(id)
        } catch {
            guard mine == epoch else { return }
            deleted.remove(id)
            prompts.insert(removed, at: min(at, prompts.count))
            deps.report(error)
        }
    }

    /// Whether an `assistant_saved_prompts` event is about the assistant shown: one without `manager` is the default
    /// assistant's, one with it only that assistant's (docs/protocol/protocol.md, the event's row).
    static func concerns(eventManager: String?, shownManager: String?) -> Bool {
        (eventManager?.isEmpty == false ? eventManager : nil) == (shownManager?.isEmpty == false ? shownManager : nil)
    }
}
