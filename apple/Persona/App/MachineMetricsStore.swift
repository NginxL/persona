import Foundation
import Observation
import PersonaWire

/// Machines' current metrics, kept separately by hub and manager so one failed read cannot clear another's numbers.
@MainActor
@Observable
final class MachineMetricsLoader {
    private(set) var metrics: [MachineRef: MachineMetrics] = [:]
    private(set) var unmonitoredGroups: Set<String> = []
    /// One last-good answer per installed manager, pruned when the roster removes one; offline managers retain theirs.
    @ObservationIgnored private var snapshots: [Scope: [MachineMetrics]] = [:]
    @ObservationIgnored private var configs: [String: ServerConfig] = [:]
    @ObservationIgnored private var read = 0

    nonisolated private struct Scope: Hashable, Sendable {
        var group: String
        var manager: String?
    }

    /// Read every connected hub at once. Settings names one captured hub and manager instead of fanning out over its roster.
    func load(_ groups: [String], rosters: [String: ManagerRoster], apis: [String: APIClient],
              connected: Set<String>, settings: AssistantSettingsRequestScope? = nil) async {
        read += 1
        let current = read
        let selected = settings.map { [$0.group] } ?? groups
        for (group, api) in apis where selected.contains(group) && configs[group] != api.config {
            snapshots = snapshots.filter { $0.key.group != group }
            metrics = metrics.filter { $0.key.group != group }
            unmonitoredGroups.remove(group)
            configs[group] = api.config
        }
        let available = selected.filter { apis[$0] != nil }
        let asked = HubFanOut.asked(available) { connected.contains($0) }
        await HubFanOut.run(asked, fetch: { group in
            await self.loadGroup(group, api: apis[group]!, roster: rosters[group], settings: settings, read: current)
        }) { _, _ in }
    }

    /// The original store's call shape, kept for its regression coverage: every supplied group is considered connected.
    func load(groups: [String], apis: [String: APIClient], rosters: [String: ManagerRoster]) async {
        await load(groups, rosters: rosters, apis: apis, connected: Set(groups))
    }

    private func loadGroup(_ group: String, api: APIClient, roster cached: ManagerRoster?,
                           settings: AssistantSettingsRequestScope?, read current: Int) async {
        var roster = cached
        if settings == nil, roster == nil {
            do { roster = ManagerRoster(try await api.manager(nil).managers()) }
            catch let error as APIError where error.status == 404 && error.code == nil { }
            catch { return } // an unread roster must not turn into an unscoped dashboard request
        }
        guard self.read == current, !Task.isCancelled else { return }

        let retained: [Scope]
        if let settings {
            retained = [Scope(group: group, manager: settings.manager)]
        } else if let roster {
            retained = roster.installed.map { Scope(group: group, manager: $0.id) }
        } else {
            retained = [Scope(group: group, manager: nil)] // a hub before the roster: the legacy bare request
        }
        snapshots = snapshots.filter { $0.key.group != group || retained.contains($0.key) }
        let scopes = settings == nil ? retained.filter { scope in
            guard let manager = scope.manager, let roster else { return true }
            return roster.installed.first { $0.id == manager }?.online == true
        } : retained
        if scopes.isEmpty {
            metrics = metrics.filter { $0.key.group != group }
            return
        }

        var unavailable = Set<Scope>()
        let requestRoster = roster
        // Work out nil-for-default on this actor before the sendable fetch closures start; nil must stay the bare route.
        let routes: [Scope: String?] = Dictionary(uniqueKeysWithValues: scopes.map {
            // Settings keeps its captured manager even before this launch's roster arrives; a legacy overview stays bare.
            ($0, settings != nil && requestRoster == nil ? $0.manager : requestRoster?.naming($0.manager))
        })
        await HubFanOut.run(scopes, fetch: { scope in
            try await api.manager(routes[scope] ?? nil).metricsOverview()
        }) { scope, result in
            guard self.read == current, !Task.isCancelled else { return }
            switch result {
            case .success(let fresh):
                self.snapshots.updateValue(fresh, forKey: scope)
                var merged = self.metrics.filter { $0.key.group != scope.group }
                for target in scopes {
                    for machine in self.snapshots[target] ?? [] {
                        let key = MachineRef(group: scope.group, id: machine.machineID)
                        if merged[key] == nil { merged[key] = machine } // roster order, not response completion order
                    }
                }
                self.metrics = merged
                self.unmonitoredGroups.remove(scope.group)
            case .failure(let error):
                // A routed no_manager (or another manager-specific 404) does not mean the hub predates monitoring.
                if let error = error as? APIError, error.status == 404, error.code == nil {
                    unavailable.insert(scope)
                    if scopes.allSatisfy(unavailable.contains) { self.unmonitoredGroups.insert(scope.group) }
                }
            }
        }
    }
}
