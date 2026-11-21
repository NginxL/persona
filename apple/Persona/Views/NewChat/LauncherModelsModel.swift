import Foundation
import Observation

/// A launcher's /model list on one computer (`GET /api/v1/machines/{mid}/launchers/{name}/models`): New Agent's model
/// picker, and the model IDs typed there before (`custom: true`, #1644) that "Other Model…" lists under its field in a
/// running session's switch and the Persona chat's chip. A typed ID can be forgotten (`DELETE …/models/custom`): the
/// row goes at once and comes back when the hub refuses.
@Observable final class LauncherModelsModel {
    struct Deps {
        /// The hub of a group of hubs (`MachineRef.group`); nil: not connected.
        var api: (_ group: String) -> APIClient?
        static var live: Deps { .init(api: { AppModel.shared.api($0) }) }
    }

    /// The computer (`MachineRef.path`), launcher and optional purpose the list below is for; "": none yet.
    private(set) var key = ""
    /// Nil: not asked yet, still loading, or an older hub without the route (no picker).
    private(set) var list: LauncherModels?
    private(set) var loading = false
    /// Why the list couldn't be read.
    private(set) var error: String?
    /// Why the last Forget didn't go through (its row is back).
    private(set) var forgetError: String?
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var requestID = 0

    init(deps: Deps = .live) { self.deps = deps }

    static func key(_ machine: MachineRef, _ launcher: String, purpose: String? = nil, manager: String? = nil, host: String? = nil) -> String {
        let owner = "\(machine.path)|\(launcher)" + (purpose.map { "|\($0)" } ?? "") + (manager.map { "|\($0)" } ?? "")
        guard let host, !host.isEmpty else { return owner }
        return owner + "|host=" + host
    }

    func catalogKey(_ machine: MachineRef, launcher: String, purpose: String? = nil, host: String? = nil) -> String {
        let api = deps.api(machine.group)
        let manager = purpose == "assistant" ? api?.currentAssistantSettings().managerID : api?.managerID
        return Self.key(machine, launcher, purpose: purpose, manager: manager, host: host)
    }

    /// The IDs typed before, most recent first.
    var remembered: [AgentModel] { list?.models.filter(\.custom) ?? [] }

    /// Moves to another computer, launcher or purpose: the last one's list and what went wrong with it are dropped.
    func select(_ machine: MachineRef, launcher: String, purpose: String? = nil, host: String? = nil) {
        let k = catalogKey(machine, launcher: launcher, purpose: purpose, host: host)
        guard k != key else { return }
        requestID += 1
        key = k
        list = nil
        loading = false
        error = nil
        forgetError = nil
    }

    /// Reads the list for `machine` and `launcher` (again with `refresh`: the node asks the CLI once more). An answer
    /// that comes after the sheet moved to another launcher is dropped.
    func load(_ machine: MachineRef, launcher: String, refresh: Bool = false, purpose: String? = nil, host: String? = nil) async {
        select(machine, launcher: launcher, purpose: purpose, host: host)
        let k = key
        guard let given = deps.api(machine.group) else { return }
        let api = purpose == "assistant" ? given.currentAssistantSettings() : given
        requestID += 1
        let request = requestID
        loading = true
        error = nil
        defer { if key == k && requestID == request { loading = false } }
        do {
            let answer = try await api.launcherModels(machine: machine.id, launcher: launcher, refresh: refresh, purpose: purpose, host: host)
            guard key == k && requestID == request else { return }
            list = answer
        } catch {
            guard key == k, requestID == request, !error.isCancellation else { return }
            if list?.supportsStartModel == true { list = nil } // a failed refresh cannot authorize another start
            if let e = error as? APIError, e.status == 404 { return } // an older hub or node: no list, as before
            self.error = error.shown
        }
    }

    /// Forgets a typed ID on the hub: off the list at once, back where it was when the hub says no.
    func forget(_ id: String, machine: MachineRef, launcher: String, purpose: String? = nil, host: String? = nil) async {
        guard catalogKey(machine, launcher: launcher, purpose: purpose, host: host) == key, let at = list?.models.firstIndex(where: { $0.id == id && $0.custom }),
              let row = list?.models[at] else { return }
        let given = deps.api(machine.group)
        let api = purpose == "assistant" ? given?.currentAssistantSettings() : given
        let k = key
        forgetError = nil
        list?.models.remove(at: at)
        do {
            guard let api else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
            try await api.forgetCustomModel(machine: machine.id, launcher: launcher, model: id)
        } catch {
            guard key == k, var models = list?.models else { return }
            if !models.contains(where: { $0.id == id }) { models.insert(row, at: min(at, models.count)) }
            list?.models = models
            forgetError = String(localized: "Couldn't forget “\(id)”: \(error.shown ?? error.localizedDescription)")
        }
    }
}
