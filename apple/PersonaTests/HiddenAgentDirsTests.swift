import Foundation
import PersonaWire
import Testing
@testable import Persona

@MainActor struct HiddenAgentDirsTests {
    @Test func decodingAndEditingKeepTheOtherRules() throws {
        let json = #"{"rules":[{"pattern":"~/work","enabled":true},{"pattern":"*scratch*","enabled":false}],"home":"/home/me"}"#
        let state = try JSON.decoder.decode(HiddenAgentDirs.self, from: Data(json.utf8))
        #expect(state.home == "/home/me")
        let first = try #require(state.rules.first)
        var disabled = first
        disabled.enabled = false
        let changed = state.replacing(first, with: disabled)
        #expect(changed.rules.first?.enabled == false)
        #expect(changed.rules.last == state.rules.last)
        #expect(changed.replacing(disabled, with: first) == state)
        #expect(state.replacing(first, with: nil).rules == [state.rules[1]])
    }

    @Test func typedFoldersAreCheckedLikeTheManagerDoes() throws {
        let state = HiddenAgentDirs(rules: [HiddenAgentDirRule(pattern: "~/work", enabled: true)])
        #expect(HiddenAgentDirs.cleaned(" ~/workspaces/ ") == "~/workspaces")
        #expect(state.problem("") == nil)
        #expect(state.problem("*botfarm*") == nil)
        #expect(state.problem("/opt/bots") == nil)
        #expect(state.problem("relative") != nil)
        #expect(state.problem("~/") != nil)
        for everything in ["/", "//", "*", "**", "/*", "/**", "//*/"] { #expect(state.problem(everything) != nil, "\(everything)") }
        #expect(state.problem("~/*") == nil)
        #expect(state.problem("/opt/*") == nil)
        #expect(state.problem("~/work/") != nil)
    }

    @Test func visibilityAnnotationDefaultsToVisibleAndSurvivesCoding() throws {
        let old = #"{"machine_id":"one","session_id":"s","title":"test","cwd":"/workspace","status":"idle"}"#
        var session = try JSON.decoder.decode(Session.self, from: Data(old.utf8))
        #expect(session.hiddenByDirectory == nil)
        session.hiddenByDirectory = true
        let data = try JSONEncoder().encode(session)
        #expect(try JSON.decoder.decode(Session.self, from: data).hiddenByDirectory == true)
    }

    @Test func revealingIsAnExplicitListQueryOnly() {
        var query = SessionListQuery()
        #expect(query.parameters["include_hidden"] == nil)
        query.includeHidden = true
        #expect(query.wireParameters(hierarchy: true)["include_hidden"] == "1")
        #expect(query.wireParameters(hierarchy: true)["roots"] == "1")
    }
}
