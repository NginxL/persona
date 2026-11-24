#if !WIDGET_EXTENSION
import AppIntents
import Foundation
import WidgetKit

/// A background action shared by the phone, the wrist and Vision Pro; it uses this device's App Group credential.
struct LogWaterIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Log Water"
    nonisolated static let description = IntentDescription("Log water in Persona. Leave the amount empty to record one glass.")
    nonisolated static var openAppWhenRun: Bool { false }
    @available(iOS 26.0, watchOS 26.0, visionOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Amount (ml)") var amount: Int?
    nonisolated init() {}
    nonisolated init(amount: Int?) { self.amount = amount }
    nonisolated static var parameterSummary: some ParameterSummary {
        Summary("Log water") { \.$amount }
    }

    nonisolated func perform() async throws -> some IntentResult & ReturnsValue<String> & ProvidesDialog {
        // Reading the shared file and the selected-group default never blocks the app's main actor (P1/P2).
        let api = try await Task.detached {
            try LogWaterFlow.client(snapshot: ExtensionSurfaceStore.shared.read(),
                                    selectedGroup: UserDefaults.standard.string(forKey: "hub.group"))
        }.value
        let text = try await LogWaterFlow.log(api, amount: amount)
        await LogWaterRefresh.run(group: api.group)
        return .result(value: text, dialog: IntentDialog("\(text)"))
    }
}

nonisolated enum LogWaterFlow {
    static func client(snapshot: ExtensionSurfaceSnapshot, selectedGroup: String?) throws -> APIClient {
        let group: String
        if let selectedGroup { group = selectedGroup }
        else if snapshot.groups[""] != nil { group = "" }
        else if snapshot.groups.count == 1, let only = snapshot.groups.keys.first { group = only }
        else { throw LogWaterFailure(message: String(localized: "Open Persona to select and sync a hub before logging water.")) }
        guard let credential = snapshot.groups[group]?.credential else {
            throw LogWaterFailure(message: String(localized: "Open Persona to select and sync a hub before logging water."))
        }
        var api = APIClient(config: ServerConfig(url: credential.url, token: credential.token, access: credential.access), group: group)
        api.clientMetadata = RequestClientMetadata(surface: .intent)
        return api
    }

    static func log(_ api: APIClient, amount: Int?) async throws -> String {
        if let amount, !(1...3000).contains(amount) {
            throw LogWaterFailure(message: String(localized: "Enter an amount from 1 to 3000 ml."))
        }
        do {
            // Read the goal before writing: a failed progress read must never turn a successful write into a retry.
            let before = try await api.healthIntake()
            let change = try await api.recordHealthIntake(.init(kind: .water, amount: amount.map(Double.init)))
            guard let entry = change.entry else { throw LogWaterFailure(message: String(localized: "The hub returned no water record.")) }
            let logged = Int(entry.amount), today = Int(change.today.water), goal = before.water.goal
            return String(localized: "Logged \(logged) ml, today \(today) / \(goal) ml")
        } catch { throw LogWaterFailure(message: failure(error)) }
    }

    static func failure(_ error: Error) -> String {
        if let api = error as? APIError, [403, 404].contains(api.status) {
            return String(localized: "This hub doesn't support logging water yet. Update the hub first.")
        }
        if let api = error as? APIError, api.status == 401 {
            return String(localized: "The pairing has expired. Open Persona to pair again.")
        }
        return error.localizedDescription
    }
}

nonisolated struct LogWaterFailure: LocalizedError {
    var message: String
    var errorDescription: String? { message }
}

/// The containing app installs its snapshot writer; Shortcuts and the watch still reload their widget timelines.
@MainActor enum LogWaterRefresh {
    static var writeSnapshot: (@MainActor @Sendable (String) async -> Void)?
    static func run(group: String) async {
        await writeSnapshot?(group)
        WidgetCenter.shared.reloadAllTimelines()
    }
}
#endif
