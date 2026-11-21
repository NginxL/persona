import Foundation
import PersonaWire
import Testing
@testable import Persona

/// New Agent on an omp launcher (docs/integrations/omp.md): omp's own config.yml picks its model and thinking level, so
/// the sheet offers no permission mode, effort or ultracode and sends none, and the first message goes with the start as
/// text, as a Codex's does (the node types it into omp's command and refuses a start that sets those options).
struct OmpNewAgentTests {
    private static let omp = Launcher(name: "omp", model: "", agent: "omp", source: "zshrc")
    private static let claude = Launcher(name: "claude", source: "zshrc")

    @Test func ompIsToldByTheAgentItRuns() throws {
        #expect(Self.omp.isOmp)
        // The hub's configured kind wins (Launcher.runtimeAgent): a provider the hub says runs omp is omp.
        #expect(Launcher(name: "omp-work", agent: nil, agentType: "omp").isOmp)
        #expect(!Self.claude.isOmp)
        #expect(!Launcher(name: "codex", agent: "codex").isOmp)
        #expect(!Launcher(name: "pi", agent: "pi").isOmp)
        // As the node lists it (server/internal/node/launchers.go).
        let listed = try JSON.decoder.decode(Launcher.self, from: Data(#"{"name":"omp","agent":"omp","source":"zshrc"}"#.utf8))
        #expect(listed.isOmp && !listed.isCodex && !listed.needsFirstMessage)
    }

    @Test func ompTakesNoModeAndNoEffort() {
        #expect(StartModes.modes(for: Self.omp).isEmpty)
        #expect(StartModes.efforts(known: nil, for: Self.omp).isEmpty)
        #expect(StartModes.efforts(known: ["low", "high"], for: Self.omp).isEmpty)
        #expect(StartModes.sent(SessionModes.ModeID.bypass, for: Self.omp) == nil)
        #expect(StartModes.sent("plan", for: Self.omp) == nil)
        // Claude Code keeps its own.
        #expect(!StartModes.modes(for: Self.claude).isEmpty)
        #expect(!StartModes.efforts(known: nil, for: Self.claude).isEmpty)
        #expect(StartModes.sent("plan", for: Self.claude) == "plan")
    }

    @Test func ompTakesNoAttachments() {
        let omp = String(localized: "omp takes no attachments")
        #expect(FirstMessage.hold(codex: false, omp: true, attached: true, uploading: false) == omp)
        #expect(FirstMessage.hold(codex: false, omp: true, attached: true, uploading: true) == omp)
        #expect(FirstMessage.hold(codex: false, omp: true, attached: false, uploading: false) == nil)
        #expect(FirstMessage.hold(codex: false, attached: true, uploading: false) == nil)
    }
}
