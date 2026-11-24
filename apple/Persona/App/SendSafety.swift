import SwiftUI
import PersonaWire

// Sending into a terminal safely (docs/protocol/protocol.md「Sending messages」): the node refuses a message — nothing typed — when the
// computer's Claude input box holds a draft or a dialog has the keyboard. Each pending message carries its id as
// client_msg_id, so a retry after a lost response is not typed twice. A refused one goes again under a new id: the
// hub answers an id it refused with that refusal, without asking the computer again (#6425).

/// A send refused because the computer's input box holds a draft: the app shows it and may clear it and send.
struct SendRefusal: Identifiable {
    let key: SessionKey
    let messageID: UUID
    /// The input box as shown on the computer.
    let draft: String
    /// The draft the node can clear (one row of typed text); nil: only the user at the computer can.
    let clearable: String?
    var id: UUID { messageID }
}

/// The same operation can have a check-only Retry first and a send-anyway Retry later.
nonisolated struct HeldActionTarget: Hashable, Codable {
    let session: SessionKey
    let id: String
    let manager: String?
    let pairing: String
    let credential: String
}

nonisolated struct HeldRetryOffer: Hashable, Codable {
    let target: HeldActionTarget
    let instruction: String

    init?(_ target: HeldActionTarget?, _ message: UndeliveredMessage) {
        guard let target, message.heldForComputer, message.canRetry else { return nil }
        self.target = target
        // The wire supplies the manager's delivery instruction in error. The receipt needs no message or status text.
        self.instruction = DeviceCredentialTrade.fingerprint(message.error ?? "")
    }

    private static let name = "held-retry-receipts"

    static func read(in folder: URL?) -> Set<Self> {
        let data: Data?
        if let folder { data = try? Data(contentsOf: folder.appending(path: name)) }
        else { data = UserDefaults.standard.data(forKey: name) }
        return data.flatMap { try? JSONDecoder().decode(Set<Self>.self, from: $0) } ?? []
    }

    static func save(_ receipts: Set<Self>, in folder: URL?) {
        guard let data = try? JSONEncoder().encode(receipts) else { return }
        if let folder {
            try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            try? data.write(to: folder.appending(path: name), options: .atomic)
        } else if receipts.isEmpty {
            UserDefaults.standard.removeObject(forKey: name)
        } else {
            UserDefaults.standard.set(data, forKey: name)
        }
    }
}

/// The one startup read, plus the receipt changes and session rows that arrive before it finishes.
/// Everything but the detached file read stays on AppModel's main actor.
final class HeldRetryRestoration {
    let read: Task<Set<HeldRetryOffer>, Never>
    var done: Task<Void, Never>?
    var changes: [(Set<HeldRetryOffer>) -> Set<HeldRetryOffer>] = []
    var reconciliations: [[MachineRef: [Session]]] = []
    var recordsChanges = true

    init(in folder: URL?) {
        read = Task.detached(priority: .utility) { HeldRetryOffer.read(in: folder) }
    }
}

extension AppModel {
    func startHeldRetryRestoration() {
        let restoration = HeldRetryRestoration(in: stateFolder)
        heldRetryRestoration = restoration
        restoration.done = Task { [weak self] in
            let stored = await restoration.read.value
            self?.finishHeldRetryRestoration(stored)
        }
    }

    private func waitForHeldRetryRestoration() async {
        if let done = heldRetryRestoration?.done { await done.value }
    }

    private func finishHeldRetryRestoration(_ stored: Set<HeldRetryOffer>) {
        guard let restoration = heldRetryRestoration else { return }
        var restored = stored
        for change in restoration.changes { restored = change(restored) }
        let reconciliations = restoration.reconciliations
        // Publish without rewriting an unchanged file or recording the restoration as another change.
        restoration.recordsChanges = false
        heldRetryReceipts = restored
        heldRetryRestoration = nil
        if restored != stored { HeldRetryOffer.save(restored, in: stateFolder) }
        for rows in reconciliations { reconcileHeldRetries(rows) }
    }

    /// Apply a change now and replay it over the older disk snapshot if the startup read is still pending.
    /// Unlike a property observer, this also records a clear or removal which is a no-op on the initially empty set.
    func updateHeldRetryReceipts(_ change: @escaping (Set<HeldRetryOffer>) -> Set<HeldRetryOffer>) {
        guard let restoration = heldRetryRestoration else {
            heldRetryReceipts = change(heldRetryReceipts)
            return
        }
        restoration.changes.append(change)
        restoration.recordsChanges = false
        heldRetryReceipts = change(heldRetryReceipts)
        restoration.recordsChanges = true
    }

    /// Managing a held copy belongs to this device's own credential at the hub that advertised it.
    func canManageHeld(_ group: String) -> Bool {
        heldPairing(group) != nil
    }

    private func heldPairing(_ group: String) -> PairedHub? {
        guard let link = link(group), link.features.contains(UndeliveredMessage.agentHoldFeature),
              link.surfaceCapabilitiesConfig == link.config else { return nil }
        return registry.hubs.first { $0.group == group && $0.config.token == link.config.token && $0.deviceID?.isEmpty == false }
    }

    func heldTarget(_ key: SessionKey, _ id: String, row: Session?) -> HeldActionTarget? {
        guard let row, let pairing = heldPairing(key.group) else { return nil }
        let owner = machine(key.machineRef)?.manager ?? row.manager
        return HeldActionTarget(session: key, id: id, manager: owner?.isEmpty == false ? owner : nil,
                                pairing: pairing.id, credential: DeviceCredentialTrade.fingerprint(pairing.config.token))
    }

    func heldRetryDisabled(_ offer: HeldRetryOffer?) -> Bool {
        guard let offer else { return false }
        return heldActionRequests.contains(offer.target) || heldRetryReceipts.contains(offer)
    }

    /// Reconcile only the receipt's own operation, on its paired manager. A paged/filtered list can omit a whole chat.
    func reconcileHeldRetries(_ rows: [MachineRef: [Session]]) {
        if let restoration = heldRetryRestoration {
            restoration.reconciliations.append(rows)
            return
        }
        guard !heldRetryReceipts.isEmpty else { return }
        let remaining = heldRetryReceipts.filter { offer in
            let target = offer.target, key = target.session
            guard let row = rows[key.machineRef]?.first(where: { $0.sessionID == key.sessionID }),
                  heldTarget(key, target.id, row: row) == target else { return true }
            return row.undelivered?.first { $0.id == target.id }.flatMap { HeldRetryOffer(target, $0) } == offer
        }
        if remaining != heldRetryReceipts { updateHeldRetryReceipts { _ in remaining } }
    }

    /// Only the computer being away is recoverable by holding a message. Its manager must still be reachable.
    func offlineAgent(_ session: Session) -> (line: String, canHold: Bool)? {
        #if os(iOS)
        guard !session.assistant, session.canSend, !session.isStopped, let computer = machine(session.machineRef), !computer.online else { return nil }
        let canHold = !session.isTerminalOnly && session.starting != true && offlineManager(of: session) == nil
            && offlineManager(of: computer, group: session.group) == nil && canManageHeld(session.group)
        let line = canHold ? String(localized: "\(computer.name) is offline. Sends when it's back.")
                           : String(localized: "\(computer.name) is offline. Messages can be sent once it's back.")
        return (line, canHold)
        #else
        return nil
        #endif
    }

    /// Sends a pending message, telling apart why it failed: a draft on the computer (an alert offers to clear it), an
    /// open dialog, a failure after typing began (it may have arrived; a retry with the same id cannot double it), or
    /// no answer at all (no network, the hub out of reach, a timeout): then it waits and goes again by itself, which
    /// the same id makes safe too.
    func sendPending(_ key: SessionKey, _ msg: PendingMessage, clearDraft: String? = nil) async -> SendOutcome {
        noteFailure(key, msg.id, nil)
        // Restored outbox entries wait for metadata; opaque IDs do not identify a writable source.
        guard let session = latest(key) else { return .waiting }
        if let effort = effortRequests[key] ?? session.pendingEffort {
            holdForEffort(key, msg.id, effort: effort)
            return .effortWaiting
        }
        if let effort = msg.requiredEffort, session.effort != effort {
            noteFailure(key, msg.id, String(localized: "Thinking effort could not be confirmed. Choose it again before resending."))
            return .failed
        }
        guard session.canSend else {
            noteFailure(key, msg.id, session.sourceSubtitle ?? String(localized: "This session is view-only"))
            return .failed
        }
        guard msg.mayHaveArrived != true else {
            noteFailure(key, msg.id, String(localized: "It may have reached the computer already. Check the chat before sending it again."))
            return .failed
        }
        if let away = offlineAgent(session), !away.canHold {
            noteFailure(key, msg.id, away.line)
            return .failed
        }
        guard let api = api(key.group) else {
            noteFailure(key, msg.id, String(localized: "Can't reach the hub"))
            return .failed
        }
        let socket = socket(key.group)
        do {
            // The message's id is its op id too (docs/protocol/protocol.md「Operation receipts」): however often it arrives, the hub
            // types it once. Through a short drop (a hub restart) it keeps trying with that id instead of going to
            // the outbox's clock; once the drop's grace is over the outbox takes it (flushOutbox).
            let launcher = assistantLaunchers[key], model = assistantModels[key]
            #if os(iOS)
            let holds = !session.assistant && msg.requiredEffort == nil && canManageHeld(key.group)
            #else
            let holds = false
            #endif
            let reply = try await OpRetry.untilDelivered(keepTrying: { socket?.takesWrites == true }) {
                try await api.sendReply(key, text: msg.text, images: msg.images, launcher: launcher, model: model,
                                        clientMsgID: msg.id.uuidString, clearDraft: clearDraft, via: msg.via, opID: msg.id,
                                        holdsAgentMessages: holds, waitForEffort: session.agent == "claude")
            }
            if reply.kept {
                // The manager kept it. Its row now carries its status to every device.
                keptUndelivered(key, msg.id, reply.undelivered)
                return .kept
            }
            let waits = reply.waitsOnComputer == true
            if waits, visibleSession == key {
                // Sent, but the Agent reads it only after the question on the computer is answered (#5890). Only on
                // that chat's screen: the toast does not name the chat, and an outbox flush can run from anywhere.
                toast = Toast(String(localized: "Sent. The Agent will read it after the question on the computer is answered."))
            }
            return .sent
        } catch let e as APIError where e.code == "effort_change_pending" {
            // The node refused before typing. The operation receipt keeps the refusal under this ID.
            markRefused(key, msg.id, code: e.code)
            guard let effort = e.fields["effort"] else {
                noteFailure(key, msg.id, e.shown ?? e.localizedDescription)
                return .failed
            }
            holdForEffort(key, msg.id, effort: effort)
            return .effortWaiting
        } catch let e as APIError where e.code == "input_draft" {
            Haptics.attention()
            markRefused(key, msg.id, code: e.code)
            noteFailure(key, msg.id, e.shown ?? e.localizedDescription)
            sendRefusal = SendRefusal(key: key, messageID: msg.id, draft: e.fields["draft"] ?? "", clearable: e.fields["clearable"])
            return .failed
        } catch let e as APIError where e.fields["delivered"] == "unknown" || e.code == OpReceipt.unknownCode || e.code == "op_id_reused" {
            // op_id_reused: this id went once with other words (Clear and Send's clear_draft, then a resend of the
            // waiting message without it); what became of that try is the op's to say, as for an unknown outcome.
            let reason = String(localized: "It may have reached the computer already. Check the chat before sending it again.")
            noteFailure(key, msg.id, reason)
            toast = Toast(reason, style: .error)
            if let i = chat(key).pending.firstIndex(where: { $0.id == msg.id }) { chat(key).pending[i].mayHaveArrived = true }
            return .failed
        } catch where error.isOffline || error.isTimeout || error.isGap {
            return .waiting
        } catch let e as APIError where e.code == "dialog_open" {
            // A dialog holds the computer's terminal (#3752): nothing was typed. Say which one, and open the terminal
            // from here: answering at the computer is no way out for someone away from it.
            Haptics.attention()
            markRefused(key, msg.id, code: e.code)
            let head = e.fields["dialog"]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            let reason = head.isEmpty ? (e.shown ?? e.localizedDescription) : String(localized: "The terminal is waiting on “\(head)”.")
            noteFailure(key, msg.id, reason)
            toast = Toast(reason, style: .error, action: Toast.Action(title: String(localized: "Open terminal")) { [weak self] in
                self?.terminalAsk = key
            })
            return .failed
        } catch {
            if (error as? APIError)?.fields["delivered"] == "none" { markRefused(key, msg.id, code: (error as? APIError)?.code) } // a dialog open, say
            noteFailure(key, msg.id, error.shown ?? error.localizedDescription)
            show(error)
            return .failed
        }
    }

    /// A kept send leaves the outbox for the row at once; the hub supplies its later status.
    func keptUndelivered(_ key: SessionKey, _ id: UUID, _ kept: UndeliveredMessage?) {
        chat(key).settleSendFeedback(id, state: SendOutcome.kept.state) // no Processing left hanging (`settleSend` finds no bubble)
        chat(key).pending.removeAll { $0.id == id }
        saveOutbox()
        guard let kept else { return }
        patchListedSession(key) { row in
            var list = row.undelivered ?? []
            if !list.contains(where: { $0.id == kept.id }) { list.append(kept) }
            row.undelivered = list
        }
    }

    func retryUndelivered(_ key: SessionKey, _ id: String, shown: HeldRetryOffer? = nil) async {
        await changeUndelivered(key, id, retry: true, target: shown?.target, offer: shown)
    }

    func deleteUndelivered(_ key: SessionKey, _ id: String, target: HeldActionTarget? = nil) async {
        await changeUndelivered(key, id, retry: false, target: target, offer: nil)
    }

    private func changeUndelivered(_ key: SessionKey, _ id: String, retry: Bool, target: HeldActionTarget?, offer: HeldRetryOffer?) async {
        // The existing guards below must see the restored receipt before another Retry or Delete can run.
        await waitForHeldRetryRestoration()
        guard let row = latest(key), let message = row.undelivered?.first(where: { $0.id == id }),
              !retry || message.canRetry, let client = api(key.group) else { return }
        if message.heldForComputer {
            guard let current = heldTarget(key, id, row: row), target == nil || target == current else { return }
            let currentOffer = HeldRetryOffer(current, message)
            guard !retry || (offer == nil || offer == currentOffer) && !heldRetryDisabled(currentOffer) else { return }
            await changeHeld(current, message, retry: retry, api: client.manager(current.manager))
            return
        }
        guard target == nil, undeliveredActions[key, default: []].insert(id).inserted else { return }
        defer {
            undeliveredActions[key]?.remove(id)
            if undeliveredActions[key]?.isEmpty == true { undeliveredActions.removeValue(forKey: key) }
        }
        do {
            if retry {
                try await client.retryUndelivered(key, message)
                patchListedSession(key) { row in
                    guard let i = row.undelivered?.firstIndex(of: message) else { return }
                    row.undelivered?[i].state = "waiting"
                    row.undelivered?[i].error = nil
                }
            } else {
                try await client.deleteUndelivered(key, message)
                patchListedSession(key) { $0.undelivered?.removeAll { $0.id == id } }
            }
        } catch { show(error) }
    }

    private func changeHeld(_ target: HeldActionTarget, _ message: UndeliveredMessage, retry: Bool, api: APIClient) async {
        let key = target.session
        guard heldActionRequests.insert(target).inserted else { return }
        do {
            do {
                defer { heldActionRequests.remove(target) }
                if retry { try await api.retryUndelivered(key, message) }
                else { try await api.deleteUndelivered(key, message) }
            } catch let error as URLError where retry && (error.isTimeout || error.code == .networkConnectionLost) {
                // The manager may already have checked the chat. Reconcile its instruction before allowing another Retry.
            }
            let row = latest(key)
            let stillHere = heldTarget(key, target.id, row: row) == target
            if !retry {
                if stillHere { patchListedSession(key) { $0.undelivered?.removeAll { $0.id == target.id } } }
                updateHeldRetryReceipts { receipts in receipts.filter { $0.target != target } }
                return
            }
            guard let offer = HeldRetryOffer(target, message),
                  registry.hubs.contains(where: { $0.id == target.pairing && DeviceCredentialTrade.fingerprint($0.config.token) == target.credential })
            else { return }
            // The POST belongs to this pairing even if its chat is no longer showing. A push on this same
            // target may already have replaced or removed its instruction while the request was in flight.
            if stillHere, row?.undelivered?.first(where: { $0.id == target.id }).flatMap({ HeldRetryOffer(target, $0) }) != offer { return }
            updateHeldRetryReceipts { $0.union([offer]) }
            guard stillHere, let row else { return }
            // The lookup may lag a push about another device's actions. Update only the operation we asked about
            // and keep a change to that operation which landed during the read. A missing session can be temporary.
            let previous = row.undelivered?.first { $0.id == target.id }
            guard let fresh = try await api.sessionListDetail(key, title: row.title),
                  heldTarget(key, target.id, row: latest(key)) == target, heldRetryReceipts.contains(offer),
                  latest(key)?.undelivered?.first(where: { $0.id == target.id }) == previous else { return }
            let next = fresh.undelivered?.first { $0.id == target.id }
            guard next.flatMap({ HeldRetryOffer(target, $0) }) != offer else { return }
            patchListedSession(key) { row in
                guard let index = row.undelivered?.firstIndex(where: { $0.id == target.id }) else { return }
                if let next { row.undelivered?[index] = next }
                else { row.undelivered?.remove(at: index) }
            }
            updateHeldRetryReceipts { $0.subtracting([offer]) }
        } catch {
            if heldTarget(key, target.id, row: latest(key)) == target { show(error) }
        }
    }

    private func holdForEffort(_ key: SessionKey, _ id: UUID, effort: String) {
        guard let i = chat(key).pending.firstIndex(where: { $0.id == id }) else { return }
        chat(key).pending[i].requiredEffort = effort
    }

    func noteFailure(_ key: SessionKey, _ id: UUID, _ reason: String?) {
        guard let i = chat(key).pending.firstIndex(where: { $0.id == id }) else { return }
        chat(key).pending[i].failure = reason
        if reason == nil { chat(key).pending[i].failureCode = nil }
    }

    /// The hub answered this message's id with a refusal it keeps: the next try goes with a new id (`retry`).
    private func markRefused(_ key: SessionKey, _ id: UUID, code: String? = nil) {
        if let i = chat(key).pending.firstIndex(where: { $0.id == id }) {
            chat(key).pending[i].refused = true
            chat(key).pending[i].failureCode = code
        }
    }

    /// The user chose Clear and Send: the node clears that draft (only if the box still shows it) and sends.
    func clearDraftAndSend(_ r: SendRefusal) {
        guard let msg = chat(r.key).pending.first(where: { $0.id == r.messageID }) else { return }
        retry(r.key, msg, clearDraft: r.clearable)
    }

    private static let draftsName = "drafts"

    static func savedDrafts() -> [SessionKey: String] {
        let list = StateCache.load(draftsName, as: [SavedDraft].self) ?? []
        return Dictionary(list.map { ($0.key, $0.text) }, uniquingKeysWith: { a, _ in a })
    }

    func saveDrafts() {
        draftWriter.run { [weak self] in
            guard let self else { return }
            StateCache.save(Self.draftsName, self.drafts.filter { !$0.value.isEmpty }.map { SavedDraft(key: $0.key, text: $0.value) })
        }
    }
}

extension AppModel {
    private static let contextsName = "composer-contexts"

    static func savedContexts() -> [SessionKey: [ContextRef]] {
        let list = StateCache.load(contextsName, as: [SavedContexts].self) ?? []
        return Dictionary(list.map { ($0.key, $0.refs) }, uniquingKeysWith: { a, _ in a })
    }

    func saveContexts() {
        contextWriter.run { [weak self] in
            guard let self else { return }
            StateCache.save(Self.contextsName, self.composerContexts.filter { !$0.value.isEmpty }.map { SavedContexts(key: $0.key, refs: $0.value) })
        }
    }

    private static let commentsName = "composer-comments"

    static func savedComments() -> [SessionKey: [MessageComment]] {
        let list = StateCache.load(commentsName, as: [SavedComments].self) ?? []
        return Dictionary(list.map { ($0.key, $0.comments) }, uniquingKeysWith: { a, _ in a })
    }

    func saveComments() {
        commentWriter.run { [weak self] in
            guard let self else { return }
            StateCache.save(Self.commentsName, self.composerComments.filter { !$0.value.isEmpty }.map { SavedComments(key: $0.key, comments: $0.value) })
        }
    }

    nonisolated static func marks(_ drafts: [SessionKey: String], _ contexts: [SessionKey: [ContextRef]],
                                  _ comments: [SessionKey: [MessageComment]] = [:]) -> Set<SessionKey> {
        Set(drafts.filter { !$0.value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }.keys)
            .union(contexts.filter { !$0.value.isEmpty }.keys)
            .union(comments.filter { !$0.value.isEmpty }.keys)
    }

    /// `draftMarks` from the drafts, quotes and comments, set only when it differs.
    func markDrafts() {
        let now = Self.marks(drafts, composerContexts, composerComments)
        if now != draftMarks { draftMarks = now }
    }
}

private nonisolated struct SavedComments: Codable, Sendable {
    var key: SessionKey
    var comments: [MessageComment]
}

private nonisolated struct SavedContexts: Codable, Sendable {
    var key: SessionKey
    var refs: [ContextRef]
}

private nonisolated struct SavedDraft: Codable, Sendable {
    var key: SessionKey
    var text: String
}

extension View {
    /// The alert for a send the computer refused because of a draft in its input box.
    func sendRefusalAlert(_ model: AppModel) -> some View {
        alert(String(localized: "There's Text in the Computer's Input Box"), isPresented: Binding(
            get: { model.sendRefusal != nil },
            set: { if !$0 { model.sendRefusal = nil } }
        ), presenting: model.sendRefusal) { r in
            if r.clearable != nil {
                Button("Clear and Send") { model.clearDraftAndSend(r); model.sendRefusal = nil }
            }
            Button("Not Now", role: .cancel) { model.sendRefusal = nil }
        } message: { r in
            let shown = r.draft.count > 200 ? String(r.draft.prefix(200)) + "…" : r.draft
            Text(r.clearable != nil
                 ? String(localized: "“\(shown)”\\nYour message wasn’t sent so it wouldn’t merge with this text. Clear it on the computer and send?")
                 : String(localized: "“\(shown)”\\nYour message wasn’t sent so it wouldn’t merge with this text. Deal with it on the computer, then tap the exclamation mark next to your message to resend."))
        }
    }
}
