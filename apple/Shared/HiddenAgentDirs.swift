import Foundation
import PersonaWire

/// What an Agent folder rule does with the sessions working there (#10053): block keeps them out of Persona (that
/// computer stops collecting them), hide leaves them recorded but out of Agents and the assistant's view.
nonisolated enum AgentDirAction: String, Codable, Sendable, CaseIterable {
    case block, hide
}

/// One computer's Agent folder rules (docs/protocol/protocol.md "Agent directory visibility"): the parallel list to its
/// Blocked Config Folders (ClaudeDirsSection), with the same pattern syntax. The node and the manager match them
/// against each session's working folder; a rule switched off stays listed and does nothing.
nonisolated struct HiddenAgentDirRule: Codable, Equatable, Identifiable, Sendable {
    var pattern: String
    var enabled: Bool
    /// nil from a hub before blocking: such a rule hides.
    var action: AgentDirAction?
    var id: String { pattern }
    var effectiveAction: AgentDirAction { action ?? .hide }
}

nonisolated struct HiddenAgentDirs: Codable, Equatable, Sendable {
    var rules: [HiddenAgentDirRule] = []
    /// The computer's home, for "~" (nil before its node first said).
    var home: String?

    func replacing(_ rule: HiddenAgentDirRule, with replacement: HiddenAgentDirRule?) -> Self {
        Self(rules: rules.compactMap { $0.id == rule.id ? replacement : $0 }, home: home)
    }

    /// The typed folder as it is saved: trimmed, without trailing slashes.
    static func cleaned(_ input: String) -> String {
        var p = input.trimmingCharacters(in: .whitespacesAndNewlines)
        while p.count > 1, p.hasSuffix("/") { p.removeLast() }
        return p
    }

    /// Why a typed folder cannot be added (nil = it can), said before the round trip; the manager checks again.
    func problem(_ input: String) -> String? {
        let p = Self.cleaned(input)
        if p.isEmpty { return nil }
        // the home folder, the root, or a pattern every folder matches ("*", "/*"), as the manager refuses them
        if p == "~" || p.drop(while: { $0 == "/" }).allSatisfy({ $0 == "*" }) { return String(localized: "Can't hide the whole home or root folder") }
        if !p.hasPrefix("/") && !p.hasPrefix("~/") && !p.hasPrefix("*") { return String(localized: "Start with / or ~/, or * for a folder name") }
        if rules.contains(where: { $0.pattern == p }) { return String(localized: "Already in the list") }
        return nil
    }
}

extension APIClient {
    private struct SetHiddenAgentDirs: Encodable { var rules: [HiddenAgentDirRule] }

    func hiddenAgentDirs(machine: String) async throws -> HiddenAgentDirs {
        try await getDecodedOffMain("/api/v1/machines/\(machine)/agent-dirs/hidden")
    }

    func setHiddenAgentDirs(machine: String, rules: [HiddenAgentDirRule]) async throws -> HiddenAgentDirs {
        let data = try await request("PUT", "/api/v1/machines/\(machine)/agent-dirs/hidden", body: SetHiddenAgentDirs(rules: rules))
        return try await Task.detached(priority: .userInitiated) {
            try JSON.decoder.decode(HiddenAgentDirs.self, from: data)
        }.value
    }
}
