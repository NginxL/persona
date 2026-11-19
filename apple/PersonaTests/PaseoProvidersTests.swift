import Foundation
import PersonaWire
import Testing
@testable import Persona

/// A provider Paseo runs that is neither Claude Code nor Codex, such as Grok (#8144): New Agent offers it only what it
/// runs and never carries a remembered Claude Code mode to it; rows and cards name it by its provider, never as Claude
/// or as the engine; the "/" list leaves out `/model`, which it would get as a message.
@MainActor struct PaseoProvidersTests {
    private static func session(_ fields: String) throws -> Session {
        let json = #"{"machine_id":"m","session_id":"paseo-1","title":"t","cwd":"/w","status":"idle","state":"idle"\#(fields)}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private static func card(_ fields: String) throws -> Interaction {
        let json = #"{"id":"ix_1","machine_id":"m","session_id":"paseo-1","kind":"permission","tool_name":"Bash"\#(fields)}"#
        return try JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
    }

    private static let grok = Launcher(name: "grok", model: "grok-4", agent: "paseo", source: "paseo")
    private static let paseoClaude = Launcher(name: "my-claude", source: "paseo")
    private static let paseoCodex = Launcher(name: "my-codex", agent: "codex", source: "paseo")
    private static let shellClaude = Launcher(name: "claude", source: "zshrc")

    // MARK: New Agent

    @Test func onlyPaseosOtherProvidersAreLimited() {
        #expect(StartModes.limited(Self.grok))
        #expect(!StartModes.limited(Self.paseoClaude))
        #expect(!StartModes.limited(Self.paseoCodex))
        #expect(!StartModes.limited(Self.shellClaude))
        // Not a Paseo launcher: this change leaves its modes as they were.
        #expect(!StartModes.limited(Launcher(name: "traex", agent: "traex", source: "zshrc")))
        #expect(!StartModes.limited(nil))
    }

    @Test func aLimitedLauncherOffersAllowEverythingOnly() {
        #expect(StartModes.modes(for: Self.grok).map(\.id) == ["bypassPermissions"])
        #expect(StartModes.modes(for: Self.paseoClaude).map(\.id) == SessionModes.permissionModes.map(\.id))
        #expect(StartModes.modes(for: nil).map(\.id) == SessionModes.permissionModes.map(\.id))
    }

    /// The bug: a Plan Only remembered from a Claude Code start went to the next Grok start, which Paseo refuses.
    @Test func aRememberedModeTheLauncherCantRunIsNeverSent() {
        for mode in ["plan", "default", "acceptEdits", "auto"] {
            #expect(StartModes.sent(mode, for: Self.grok) == nil, "\(mode)")
            #expect(StartModes.sent(mode, for: Self.paseoClaude) == mode, "\(mode)")
        }
        #expect(StartModes.sent("bypassPermissions", for: Self.grok) == "bypassPermissions")
        #expect(StartModes.sent("", for: Self.grok) == nil && StartModes.sent("", for: Self.shellClaude) == nil)
    }

    @Test func aLimitedLauncherOffersOnlyTheEffortsItsModelLists() {
        #expect(StartModes.efforts(known: nil, for: Self.grok).isEmpty)
        #expect(StartModes.efforts(known: [], for: Self.grok).isEmpty)
        #expect(StartModes.efforts(known: ["low", "high"], for: Self.grok).map(\.id) == ["low", "high"])
        // Claude Code keeps every level while its model's are not known.
        #expect(StartModes.efforts(known: nil, for: Self.paseoClaude).map(\.id) == SessionModes.startEffortLevels.map(\.id))
        #expect(StartModes.efforts(known: ["medium"], for: Self.shellClaude).map(\.id) == ["medium"])
    }

    // MARK: Names

    @Test func aRowNamesItsProviderNeverTheEngine() throws {
        let labelled = try Self.session(#","agent":"paseo","agent_label":"Grok","launcher":"grok""#)
        // The node's "Paseo" on a row with no label of its own, and a row with none at all: the launcher.
        let engine = try Self.session(#","agent":"paseo","agent_label":"Paseo","launcher":"grok""#)
        let unlabelled = try Self.session(#","agent":"paseo","launcher":"grok""#)
        let bare = try Self.session(#","agent":"paseo""#)
        #expect(labelled.agentLabel == "Grok")
        #expect(engine.agentLabel == "grok" && unlabelled.agentLabel == "grok")
        #expect(bare.agentLabel == "paseo") // nothing better to call it
        // Other runtimes and the known agents are unchanged.
        let dsh = try Self.session(#","agent":"dsh","agent_label":"DSH","launcher":"dsh-work""#)
        let claude = try Self.session(#","agent":"claude","launcher":"my-claude""#)
        let codex = try Self.session(#","agent":"codex","launcher":"my-codex""#)
        #expect(dsh.agentLabel == "DSH" && claude.agentLabel == "Claude" && codex.agentLabel == "Codex")
    }

    @Test func aCardThatNamesOnlyTheEngineTakesItsRowsProvider() throws {
        let row = try Self.session(#","agent":"paseo","agent_label":"Grok","launcher":"grok""#)
        let engine = try Self.card(#","agent":"paseo","agent_label":"Paseo""#)
        #expect(engine.namesOnlyEngine)
        #expect(engine.agentName(session: row) == "Grok")
        #expect(engine.agentName(session: nil) == "Paseo") // no row: as before
        #expect(engine.title(session: row) == String(localized: "\("Grok") Is Waiting for Your Approval"))
        // A card that carries its provider's label (#8142) keeps it, row or not.
        let labelled = try Self.card(#","agent":"paseo","agent_label":"Grok""#)
        let other = try Self.session(#","agent":"paseo","launcher":"other""#)
        #expect(!labelled.namesOnlyEngine)
        #expect(labelled.agentName(session: nil) == "Grok")
        #expect(labelled.agentName(session: other) == "Grok")
        // Claude Code's card stays Claude's, Codex's Codex's, another runtime's its own.
        let claude = try Self.card("")
        let codex = try Self.card(#","agent":"codex""#)
        let dsh = try Self.card(#","agent":"dsh","agent_label":"DSH""#)
        #expect(claude.agentName(session: row) == nil)
        #expect(codex.agentName(session: row) == "Codex")
        #expect(!dsh.namesOnlyEngine && dsh.agentName(session: row) == "DSH")
    }

    @Test func theShareCardUsesAProvidersMarkOrLetter() {
        #expect(AgentKind.brand(named: "Grok") == Theme.Brand.grok)
        #expect(AgentKind(brand: nil, name: "Example", surface: nil).letter == "E")
        #expect(AgentKind.brand(named: "Claude") == Theme.Brand.claude)
        #expect(AgentKind.brand(named: "Codex") == Theme.Brand.openAI)
    }

    // MARK: /model

    @Test func onlyClaudeInATerminalPaneTakesATypedModelCommand() throws {
        let pane = try Self.session(#","agent":"claude","pane_id":"%1""#)
        #expect(pane.switchesModel && pane.takesModelCommand)
        // Paseo's Grok switches models through its menus (capabilities.model), never a typed /model.
        let grok = try Self.session(#","agent":"paseo","source_kind":"runtime","capabilities":{"read":true,"send":true,"model":true}"#)
        #expect(grok.switchesModel && !grok.takesModelCommand)
        let paseoClaude = try Self.session(#","agent":"claude","source_kind":"runtime","capabilities":{"read":true,"send":true,"model":true}"#)
        #expect(!paseoClaude.takesModelCommand)
        // Another agent in a pane, and Claude Code without a pane.
        let gemini = try Self.session(#","agent":"gemini","pane_id":"%2""#)
        let paneless = try Self.session(#","agent":"claude""#)
        #expect(!gemini.takesModelCommand && !paneless.takesModelCommand)
    }
}
