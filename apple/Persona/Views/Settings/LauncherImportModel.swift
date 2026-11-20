import Foundation
import Observation

/// Import from ~/.zshrc (#2455): pick a machine, see what its claude aliases come to (merged by gateway and key), tick the
/// ones to keep and import them; the hub reads the keys on the machine again itself, so none passes through the app.
@Observable final class LauncherImportModel {
    struct Deps {
        var api: () -> APIClient?
        static var live: Deps {
            let app = AppModel.shared
            let scope = AssistantSettingsRequestScope(app)
            return .init(api: { scope.api(app) })
        }
    }

    enum Phase: Equatable {
        /// Choosing the machine.
        case machine
        /// Reading its ~/.zshrc.
        case loading
        /// The providers found, to tick.
        case preview
        case importing
        case done
    }

    private(set) var phase: Phase = .machine
    var machine = ""
    /// Set only when the user chose a single alias in Add Provider.
    var alias: String?
    private(set) var preview: LauncherImportPreview?
    /// The providers ticked, by name (all of them once the preview is in).
    var picked: Set<String> = []
    private(set) var result: LauncherImportResult?
    private(set) var error: String?
    /// The machine's personad cannot read ~/.zshrc for this (409 node_outdated): it needs deploying again.
    private(set) var outdated = false
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    var providers: [LauncherImportPreview.Provider] { preview?.providers ?? [] }
    var canImport: Bool { phase == .preview && !picked.isEmpty }

    func toggle(_ name: String) {
        if picked.contains(name) { picked.remove(name) } else { picked.insert(name) }
    }

    /// Reads the machine's aliases; every provider found starts ticked.
    func load() async {
        guard !machine.isEmpty, let api = deps.api() else { return }
        phase = .loading
        error = nil
        outdated = false
        do {
            let p = try await api.launcherImportPreview(machine: machine, alias: alias)
            preview = p
            picked = Set(p.providers.map(\.name))
            phase = .preview
        } catch {
            let e = error as? APIError
            outdated = e?.status == 409 && e?.code == "node_outdated"
            self.error = error.shown ?? error.localizedDescription
            phase = .machine
        }
    }

    func runImport() async {
        guard canImport, let api = deps.api() else { return }
        phase = .importing
        error = nil
        do {
            // In the order the hub listed them.
            result = try await api.importLaunchers(machine: machine, providers: providers.map(\.name).filter(picked.contains), alias: alias)
            phase = .done
        } catch {
            self.error = error.shown ?? error.localizedDescription
            phase = .preview
        }
    }

    /// Back to choosing a machine.
    func restart() {
        phase = .machine
        alias = nil
        preview = nil
        picked = []
        result = nil
        error = nil
        outdated = false
    }
}
