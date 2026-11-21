import Foundation

/// What New Agent offers for a start's permission mode and thinking effort, by the launcher that will run (#8144, #8261).
/// Claude Code's launchers take its five modes, and the efforts its model lists. A provider of another agent that Paseo
/// runs (Grok, OpenCode, Paseo's Codex…) takes none of Claude Code's modes: the bridge refuses Plan Only for it
/// (`readonly_unsupported`) and drops Ask First, Auto-Edit and Auto. Its launcher lists the modes a start on it takes
/// (`Launcher.permissionModes`): its own approval (Use Launcher's, sent as no mode) and, where it has one, Allow
/// Everything (Paseo's Auto Accept or full access, docs/integrations/paseo.md "Capabilities"); and it takes only the
/// efforts its model lists. Both are picked on the sheet and never remembered, the same rule as the web and Android
/// (docs/design/DESIGN_SYSTEM.md "Permission modes a provider lists (#8261)"). A computer older than that list says
/// nothing: there a Paseo provider that is neither Claude Code nor Codex is told by its agent instead (#8144). A native
/// omp start takes neither (`Launcher.startsOmp`): omp's own config decides, and the node refuses a start that sets
/// them. A registered omp runtime keeps its own listed modes and efforts (#12173).
nonisolated enum StartModes {
    /// The permission modes a start on it takes when they are not Claude Code's (the node's `permission_modes`, #8261);
    /// nil for Claude Code's launchers, for the others, which take none, and from an older computer.
    static func listed(_ launcher: Launcher?) -> [String]? {
        // A native omp start always uses the launcher's own approval. It does not advertise Claude's controls.
        if launcher?.startsOmp == true { return [SessionModes.ModeID.launcherDefault] }
        guard let modes = launcher?.permissionModes, !modes.isEmpty else { return nil }
        return modes
    }

    /// A known terminal Traex, discovered in the shell or explicitly typed without another runtime source. It takes no
    /// Claude Code start choices; Paseo, desktop and unknown runtime sources keep their own paths (#1842).
    static func terminalTraex(_ launcher: Launcher?) -> Bool {
        guard let launcher, launcher.runtimeAgent == "traex" else { return false }
        return launcher.source == "zshrc" || launcher.source?.isEmpty != false
    }

    /// It takes its own modes, not Claude Code's: it lists them (`listed`), or, from a computer older than that list, it
    /// is a launcher of the Paseo engine (`source` "paseo") whose agent is neither Claude Code nor Codex, which the node
    /// reports with an agent of its own ("paseo", #8144).
    static func limited(_ launcher: Launcher?) -> Bool {
        if listed(launcher) != nil { return true }
        guard let launcher, launcher.source == MachineEngine.paseo.rawValue else { return false }
        let agent = launcher.agent ?? ""
        return !(agent.isEmpty || agent == "claude" || agent == "codex")
    }

    /// The permission modes offered besides the launcher's own (Use Launcher's): Claude Code's five; what a launcher that
    /// lists its own has besides its own approval (Allow Everything, which says it can be switched back in the chat);
    /// Allow Everything for a limited launcher from an older computer. Empty: no choice to make.
    static func modes(for launcher: Launcher?) -> [SessionModes.Option] {
        if terminalTraex(launcher) || launcher?.startsOmp == true { return [] }
        if let listed = listed(launcher) {
            return listed.filter { $0 != SessionModes.ModeID.launcherDefault }.map { SessionModes.listedOption($0) }
        }
        guard limited(launcher) else { return SessionModes.permissionModes }
        return SessionModes.permissionModes.filter { $0.id == SessionModes.ModeID.bypass }
    }

    /// The mode a start sends: the pick when the launcher runs it; nil for the launcher's own, or a mode it can't run.
    static func sent(_ mode: String, for launcher: Launcher?) -> String? {
        guard !mode.isEmpty, modes(for: launcher).contains(where: { $0.id == mode }) else { return nil }
        return mode
    }

    /// What a picked mode does, as the launcher runs it (the section's footer); nil for the launcher's own.
    static func detail(_ mode: String, for launcher: Launcher?) -> String? {
        modes(for: launcher).first { $0.id == mode }?.detail
    }

    /// The efforts offered: the ones the model that will run lists (`known`), every start level while that is not
    /// known. A limited launcher's only from its model's list: none while that is not known. None for native omp.
    static func efforts(known: [String]?, for launcher: Launcher?) -> [SessionModes.Option] {
        if terminalTraex(launcher) || launcher?.startsOmp == true { return [] }
        guard let known else { return limited(launcher) ? [] : SessionModes.startEffortLevels }
        return SessionModes.startEffortLevels.filter { known.contains($0.id) }
    }
}
