import Foundation
import PersonaWire
import UserNotifications

/// Fixed built-in reminder buttons; the amount remains text so the hub applies its localized input rules.
nonisolated enum WaterReminderActions {
    static let category = "HEALTH_WATER"
    static let drink = "HEALTH_WATER_DRINK"
    static let snooze = "HEALTH_WATER_SNOOZE"
    static let log = "HEALTH_WATER_LOG"

    static var notificationCategory: UNNotificationCategory {
        UNNotificationCategory(identifier: category, actions: [
            UNNotificationAction(identifier: drink, title: String(localized: "Drank a Glass"), options: []),
            UNNotificationAction(identifier: snooze, title: String(localized: "Later"), options: []),
            UNTextInputNotificationAction(identifier: log, title: String(localized: "Log Another Amount"), options: [],
                                          textInputButtonTitle: String(localized: "Log Water"),
                                          textInputPlaceholder: String(localized: "Millilitres, for example 300")),
        ], intentIdentifiers: [], options: [])
    }

    struct Request: Encodable, Sendable, Hashable {
        var reminder: String
        var action: String
        var amount: String?
    }

    static func request(category: String, action: String, info: [AnyHashable: Any], text: String?) -> Request? {
        guard category == self.category, let reminder = reminderID(info) else { return nil }
        switch action {
        case drink: return Request(reminder: reminder, action: "drink")
        case snooze: return Request(reminder: reminder, action: "snooze")
        case log: return Request(reminder: reminder, action: "log", amount: text ?? "")
        default: return nil
        }
    }

    static func reminderID(_ info: [AnyHashable: Any]) -> String? {
        guard let health = info["health"] as? [String: Any],
              let reminder = health["reminder"] as? String, !reminder.isEmpty, reminder.utf8.count <= 128,
              reminder == reminder.trimmingCharacters(in: .whitespacesAndNewlines),
              !reminder.contains(where: { $0 == "\0" || $0 == "\r" || $0 == "\n" }) else { return nil }
        return reminder
    }

    static func run(_ request: Request, group: String) async {
        do {
            let api = try await Task.detached { try HubStore.client(group: group) }.value
            guard let toast = try await WaterReminderDelivery.shared.send(request, api: api) else { return }
            await LogWaterRefresh.run(group: group)
            await show(toast, id: request.reminder)
        } catch {
            let message = (error as? APIError)?.status == 404
                ? LogWaterFlow.failure(error) : IntentFailure.sentence(for: error)
            await show(message, id: request.reminder)
        }
    }

    static func show(_ text: String, id: String) async {
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Water")
        content.body = text
        try? await UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "health-water-result-" + id,
                                                                              content: content, trigger: nil))
    }
}

/// Suppress concurrent/repeated callbacks for recent successful buttons. Failed requests remain retryable with the same id.
actor WaterReminderDelivery {
    static let shared = WaterReminderDelivery()
    private nonisolated struct Key: Hashable { var group: String; var reminder: String; var action: String }
    private var pending: Set<Key> = []
    private var completed: [Key] = []

    func send(_ request: WaterReminderActions.Request, api: APIClient) async throws -> String? {
        let key = Key(group: api.group, reminder: request.reminder, action: request.action)
        guard !pending.contains(key), !completed.contains(key) else { return nil }
        pending.insert(key)
        defer { pending.remove(key) }
        let toast = try await api.waterReminder(request)
        completed.append(key)
        if completed.count > 256 { completed.removeFirst(completed.count - 256) }
        return toast
    }
}

nonisolated extension APIClient {
    func waterReminder(_ request: WaterReminderActions.Request) async throws -> String {
        nonisolated struct Answer: Decodable { var toast: String }
        let data = try await manager(nil).request("POST", "/api/v1/assistant/health/water/reminder",
                                                query: ["space": "health"], body: request)
        return try JSON.decoder.decode(Answer.self, from: data).toast
    }
}

/// Origin is copied on the system callback queue; an unknown source never falls back to the selected hub.
nonisolated struct WaterReminderOrigin: Sendable {
    var hubID: String?
    var sealedGroup: String?
    init(_ info: [AnyHashable: Any]) {
        hubID = info["hub_id"] as? String
        sealedGroup = info[PushCrypto.groupKey] as? String
    }

    func group(hubGroups: [String: String], groups: [String]) -> String? {
        if let hubID {
            guard let group = hubGroups[hubID], groups.contains(group), sealedGroup == nil || sealedGroup == group else { return nil }
            return group
        }
        if let sealedGroup { return groups.contains(sealedGroup) ? sealedGroup : nil }
        return groups.count == 1 ? groups.first : nil
    }

    @MainActor func group(_ model: AppModel) -> String? {
        var mapping: [String: String] = [:]
        if let hubID, let group = model.registry.group(hubID: hubID) { mapping[hubID] = group }
        return group(hubGroups: mapping, groups: model.groupIDs)
    }
}
