import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Which mark an agent's row draws and whether it carries the command line's </> badge (#6390), read off the session
/// as the hub sends it.
struct AgentMarkTests {
    private static func session(_ fields: String) -> Session {
        let json = #"{"machine_id":"m","session_id":"s","title":"t","cwd":"/w","status":"idle","state":"idle"\#(fields)}"#
        return try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    @Test func claudeAndAnOlderHubAreClaudesMark() {
        for fields in ["", #","agent":"claude""#, #","agent":"""#] {
            let k = AgentKind(Self.session(fields))
            #expect(k.brand == Theme.Brand.claude && k.name == "Claude Code", "\(fields)")
        }
    }

    @Test func codexFromATerminalOrItsAppIsOpenAIsMark() {
        for fields in [#","agent":"codex""#, #","agent":"claude","source_kind":"codex_history""#, #","source_kind":"codex_app_server""#] {
            let k = AgentKind(Self.session(fields))
            #expect(k.brand == Theme.Brand.openAI && k.name == "Codex", "\(fields)")
        }
    }

    @Test func traeHasItsMarkAndTraexItsLetter() {
        #expect(AgentKind(Self.session(#","agent":"trae""#)).brand == Theme.Brand.trae)
        #expect(AgentKind(Self.session(#","agent":"trae-cli""#)).brand == Theme.Brand.trae)
        let traex = AgentKind(Self.session(#","agent":"traex""#))
        #expect(traex.brand == nil && traex.letter == "T" && traex.name == "TRAEX")
        let dsh = AgentKind(Self.session(#","agent":"dsh","agent_label":"DSH""#))
        #expect(dsh.brand == Theme.Brand.dsh)
    }

    /// A Paseo provider that is neither Claude Code nor Codex (`agent` "paseo", #8144): named by its label, else its
    /// launcher, never the engine; a known provider (TRAE) draws its mark, any other its letter, never Claude's.
    @Test func aPaseoProviderIsNamedAndMarkedByItsProvider() {
        let grok = AgentKind(Self.session(#","agent":"paseo","agent_label":"Grok","launcher":"grok""#))
        #expect(grok.brand == Theme.Brand.grok && grok.name == "Grok")
        // The node's fallback ("Paseo", the engine's name) and a row with no label: the launcher, Paseo's provider id.
        let engine = AgentKind(Self.session(#","agent":"paseo","agent_label":"Paseo","launcher":"grok""#))
        #expect(engine.brand == Theme.Brand.grok && engine.name == "grok")
        #expect(AgentKind(Self.session(#","agent":"paseo","launcher":"kimi""#)).name == "kimi")
        // TRAE by its label, or by its launcher when the row names only the engine.
        #expect(AgentKind(Self.session(#","agent":"paseo","agent_label":"Trae CLI","launcher":"traecli""#)).brand == Theme.Brand.trae)
        #expect(AgentKind(Self.session(#","agent":"paseo","agent_label":"Paseo","launcher":"traecli""#)).brand == Theme.Brand.trae)
        // A runtime's reported agent keeps its own mark even when its launcher names a different product.
        let dsh = AgentKind(Self.session(#","agent":"dsh","agent_label":"DSH","launcher":"trae""#))
        #expect(dsh.brand == Theme.Brand.dsh && dsh.name == "DSH")
    }

    @Test func knownNamesMapToTheirMarks() {
        #expect(AgentKind.brand(named: "TRAE") == Theme.Brand.trae)
        #expect(AgentKind.brand(named: "trae-cli") == Theme.Brand.trae)
        #expect(AgentKind.brand(named: "Claude Code") == Theme.Brand.claude)
        #expect(AgentKind.brand(named: "Codex") == Theme.Brand.openAI)
        // TRAEX is an in-house Codex fork, not TRAE; an unknown provider has no mark.
        #expect(AgentKind.brand(named: "TRAEX") == nil)
        #expect(AgentKind.brand(named: "Grok · Official") == Theme.Brand.grok)
        #expect(AgentKind.brand(named: "Grok Build") == Theme.Brand.grok)
        #expect(AgentKind.brand(named: "Google Antigravity") == Theme.Brand.agy)
        #expect(AgentKind.brand(named: "Kimi CLI") == Theme.Brand.kimi)
        #expect(AgentKind.brand(named: "GitHub Copilot CLI") == Theme.Brand.copilot)
        #expect(AgentKind(Self.session(#","agent":"opencode""#)).brand == Theme.Brand.opencode)
        #expect(AgentKind(Self.session(#","agent":"pi","agent_label":"My pi profile""#)).brand == Theme.Brand.pi)
        #expect(AgentKind(Self.session(#","agent":"agy","launcher":"my-remote-launcher""#)).brand == Theme.Brand.agy)
        #expect(AgentKind.brand(named: "Unknown Harness") == nil)
        #expect(AgentKind.brand(named: nil) == nil)
    }

    @Test func theClientComesFromAgentSurface() {
        #expect(AgentKind(Self.session(#","agent":"claude","agent_surface":"cli""#)).surface == .cli)
        #expect(AgentKind(Self.session(#","agent":"codex","agent_surface":"desktop""#)).surface == .desktop)
        // A client the app does not know, and an older node that says nothing: no badge.
        #expect(AgentKind(Self.session(#","agent":"codex","agent_surface":"sdk""#)).surface == nil)
        #expect(AgentKind(Self.session(#","agent":"codex""#)).surface == nil)
    }

    @Test func theMarkSaysTheClient() {
        #expect(AgentKind(brand: nil, name: "Codex", surface: nil).spoken == "Codex")
        #expect(AgentKind(brand: nil, name: "Codex", surface: .cli).spoken != "Codex")
        #expect(AgentKind(brand: nil, name: "Codex", surface: .desktop).spoken != AgentKind(brand: nil, name: "Codex", surface: .cli).spoken)
    }
}
