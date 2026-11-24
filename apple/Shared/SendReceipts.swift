import Foundation
import PersonaWire

/// One of the hub's transport receipts for a send (docs/protocol/protocol.md "Message feedback (#5258)"):
/// `message_send_state` for a session's message by its `client_msg_id`, and `todo_send_state` for a to-do's message or
/// answer by its `X-Persona-Op-Id` ("To-do writes", #10465). Processing says the hub has the send, before scheduling or
/// the node; the final state says how its relay ended. Feedback only: the HTTP answer, or the op id's receipt, stays
/// authoritative, and these events are never replayed on reconnect.
nonisolated struct SendReceipt: Equatable, Sendable {
    enum State: String, Sendable {
        case processing, delivered, failed, unknown
        /// Nothing went in, and the assistant chat's manager keeps the message to send by itself (#10194). Sessions only.
        case kept

        var isFinal: Bool { self != .processing }
    }

    /// The two id spaces: a session message's `client_msg_id` and a to-do write's op id.
    enum Kind: Sendable { case message, todo }

    /// Which send a receipt is for. Ids are compared without case: the hub lowercases op ids, and iOS sends upper-case
    /// UUIDs. `group`: the hub group whose socket it came on (docs/protocol/multi-hub.md), stamped by
    /// `ServerEvent.stamped`. A sender names the group it writes to (`HubLink.group`), which need not be the default
    /// one, so it has no default.
    struct Key: Hashable, Sendable {
        var group: String
        let kind: Kind
        let id: String

        init(group: String, kind: Kind, id: String) {
            self.group = group
            self.kind = kind
            self.id = id.lowercased()
        }
    }

    /// What the send went to: a receipt that names something else for the same id is not this send's.
    enum Target: Hashable, Sendable {
        case session(machineID: String, sessionID: String)
        case todo(String)
    }

    static let messageType = "message_send_state"
    static let todoType = "todo_send_state"
    /// What an app declares in `client_hello.features` to be sent `todo_send_state` (`message_send_state` needs none: it
    /// goes to the session's subscribers).
    static let todoFeature = todoType

    var key: Key
    var target: Target
    var state: State

    private struct Fields: Decodable {
        var machineID, sessionID, clientMsgID, todoID, clientOpID, state: String?
        enum CodingKeys: String, CodingKey {
            case state
            case machineID = "machine_id", sessionID = "session_id", clientMsgID = "client_msg_id"
            case todoID = "todo_id", clientOpID = "client_op_id"
        }
    }

    /// The receipt a `message_send_state` or `todo_send_state` frame says; nil for anything else, an id missing, or a
    /// state this build does not know (or one the event type never carries).
    static func decode(type: String, _ data: Data) -> SendReceipt? {
        guard type == messageType || type == todoType,
              let f = try? JSON.decoder.decode(Fields.self, from: data),
              let state = f.state.flatMap(State.init(rawValue:)) else { return nil }
        // The default group until `ServerEvent.stamped` gives it its link's.
        if type == todoType {
            guard state != .kept, let op = f.clientOpID, !op.isEmpty, let todo = f.todoID, !todo.isEmpty else { return nil }
            return SendReceipt(key: Key(group: "", kind: .todo, id: op), target: .todo(todo), state: state)
        }
        guard let id = f.clientMsgID, !id.isEmpty, let m = f.machineID, !m.isEmpty, let s = f.sessionID, !s.isEmpty else { return nil }
        return SendReceipt(key: Key(group: "", kind: .message, id: id), target: .session(machineID: m, sessionID: s), state: state)
    }
}

/// Which receipts the current socket may be trusted with: the authenticated hello's top-level `message_send_state` and
/// `todo_send_state`, each version 1. The hub writes these itself; a feature-array entry is not proof, since older hubs
/// merged extension feature strings there and did not reserve the event names. Cleared with the socket: a new
/// connection, or a new credential, must say it again (`LiveConnection`).
nonisolated struct SendReceiptTrust: Hashable, Sendable {
    var messages = false
    var todos = false

    /// Neither kind (not `none`, which an optional chain would read as `Optional.none`).
    static let untrusted = SendReceiptTrust()

    func admits(_ receipt: SendReceipt) -> Bool {
        switch receipt.key.kind {
        case .message: messages
        case .todo: todos
        }
    }

    private struct Fields: Decodable {
        var messages, todos: Version?
        enum CodingKeys: String, CodingKey {
            case messages = "message_send_state", todos = "todo_send_state"
        }
    }

    /// A marker of any other shape (or version) is no trust, never a lost hello.
    private struct Version: Decodable {
        var one = false
        init(from decoder: Decoder) throws { one = (try? decoder.singleValueContainer().decode(Double.self)) == 1 }
    }

    /// The markers a hello frame carries.
    static func decode(hello data: Data) -> SendReceiptTrust {
        guard let f = try? JSON.decoder.decode(Fields.self, from: data) else { return .untrusted }
        return SendReceiptTrust(messages: f.messages?.one == true, todos: f.todos?.one == true)
    }
}

/// The sends this app waits on a receipt for, keyed by `client_msg_id` or op id: a receipt for any other id is
/// ignored, and so is one that names another session or to-do for the same id. Processing moves an entry on; its final
/// state ends it, and the entry leaves the table then (the sender keeps what it was told). At most `limit` entries:
/// when one more is expected, the oldest leaves without a state, and its sender falls back on the HTTP answer.
nonisolated struct SendReceipts: Equatable, Sendable {
    static let limit = 64

    struct Entry: Equatable, Sendable {
        var target: SendReceipt.Target
        /// nil until the hub says anything: the send is only Sending (local).
        var state: SendReceipt.State?
    }

    private(set) var entries: [SendReceipt.Key: Entry] = [:]
    /// The keys, oldest first.
    private var order: [SendReceipt.Key] = []

    var count: Int { entries.count }

    func state(_ key: SendReceipt.Key) -> SendReceipt.State? { entries[key]?.state }

    func expects(_ key: SendReceipt.Key) -> Bool { entries[key] != nil }

    /// Wait for `key`'s receipts. Expecting a key again starts it over (a send again under the same id, which the hub
    /// runs anew only when nothing was taken the first time). Returns the key that left to make room, if any.
    @discardableResult
    mutating func expect(_ key: SendReceipt.Key, target: SendReceipt.Target) -> SendReceipt.Key? {
        if entries[key] != nil { order.removeAll { $0 == key } }
        entries[key] = Entry(target: target)
        order.append(key)
        guard order.count > Self.limit else { return nil }
        let oldest = order.removeFirst()
        entries[oldest] = nil
        return oldest
    }

    /// Takes a trusted receipt: the entry's new state when it moved, nil when the receipt is not for a send this table
    /// waits on or says nothing new. A final state takes the entry out.
    mutating func apply(_ receipt: SendReceipt) -> SendReceipt.State? {
        guard var entry = entries[receipt.key], entry.target == receipt.target, entry.state != receipt.state else { return nil }
        if receipt.state.isFinal {
            forget(receipt.key)
        } else {
            entry.state = receipt.state
            entries[receipt.key] = entry
        }
        return receipt.state
    }

    /// The sender has its answer (or gave up): later receipts for `key` are ignored.
    mutating func forget(_ key: SendReceipt.Key) {
        guard entries.removeValue(forKey: key) != nil else { return }
        order.removeAll { $0 == key }
    }
}
