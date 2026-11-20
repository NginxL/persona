import Foundation
import PersonaWire
import Testing
@testable import Persona

/// The Add Provider wizard consumes the manager's answer. All settings and addresses here are synthetic.
@MainActor
struct ProviderPasteTests {
    private let route = "/api/v1/launchers/import/parse"
    private let draft = #"""
    {"label":"Lab gateway","provider":{"kind":"launcher","base_url":"https://lab.invalid","api_key":"fixture-only","auth":"bearer","model":"from-default",
      "models":[{"id":"shared","context_window":999},{"id":"from-list"}],"effort":"low","env":"LAB_NOTE=synthetic-value","headers":"X-Team: synthetic-header"},
     "matches":[{"name":"saved","same_key":true},{"name":"other","display_name":"Other","same_key":false}],
     "env_names":["LAB_NOTE"],"skipped_env":[{"name":"APP_PASSWORD","reason":"credential"}],
     "skipped_headers":[{"name":"Authorization","reason":"credential"}],"notes":[{"code":"key_helper"}]}
    """#

    private func requestBody(_ r: LocalRequest?) throws -> [String: JSONValue] {
        let data = try #require(r?.body)
        return try #require(JSON.decoder.decode(JSONValue.self, from: data).object)
    }

    @Test func multipleDraftsRequireAChoiceAndReviewShowsOnlyFieldNamesAndReasons() async throws {
        let hub = FakeHub()
        hub.on("POST", route, json: #"{"format":"shell","drafts":[\#(draft),{"provider":{"display_name":"Second"}}],"notes":[{"code":"line_unused","line":7}]}"#)
        let paste = ProviderPasteModel()
        let text = "SYNTHETIC_PROVIDER=example"
        paste.text = text
        await paste.read(using: hub.client.manager("assistant-a"))
        let request = try #require(hub.sent("POST", route).first)
        #expect(try requestBody(request)["text"]?.string == text)
        #expect(request.headers[APIClient.managerHeader] == "assistant-a")
        #expect(paste.answer?.drafts?.count == 2 && paste.selected == nil && paste.action == nil)
        paste.select(0)
        let d = try #require(paste.draft)
        #expect(paste.action == .merge("saved"))
        #expect(d.action(for: try #require(d.matches?.last)) == .replaceKey("other"))
        let notes = d.settingNotes.joined(separator: "\n")
        for name in ["LAB_NOTE", "APP_PASSWORD", "Authorization", "X-Team"] { #expect(notes.contains(name)) }
        for value in ["fixture-only", "synthetic-value", "synthetic-header"] { #expect(!notes.contains(value)) }
        #expect(paste.answer?.notes?.first?.pasteText.contains("7") == true)
        #expect(d.notes?.first?.pasteText != ProviderImportNote(code: "unknown").pasteText)
        paste.select(1)
        #expect(paste.action == .add)
        #expect(hub.requests.count == 1)
    }

    @Test func existingProviderChangesOnlyTheRequestedModelsOrKeyAfterSave() async throws {
        let d = try JSON.decoder.decode(ProviderImportDraft.self, from: Data(draft.utf8))
        let existing = try JSON.decoder.decode(LauncherView.self, from: Data(#"""
        {"name":"saved","source":"persona","kind":"launcher","base_url":"https://lab.invalid","model":"original",
         "models":[{"id":"original"},{"id":"shared","context_window":64000}],"auth":"api_key","has_key":true,
         "has_env":true,"has_headers":true,"proxy":"https://proxy.invalid","effort":"high","opus_model":"original"}
        """#.utf8))
        for action in [ProviderPasteAction.merge("saved"), .replaceKey("saved")] {
            let hub = FakeHub()
            let path = "/api/v1/launchers/saved"
            hub.on("PUT", path, json: "{}")
            let form = ProviderWizardModel(existing: existing, deps: .init(api: { hub.client }))
            let before = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(form.input))
            form.applyPaste(d, action: action)
            #expect(hub.requests.isEmpty)
            #expect(await form.save())
            var updated = try requestBody(hub.sent("PUT", path).first)
            var original = try #require(before.object)
            switch action {
            case .merge:
                let models = try #require(updated.removeValue(forKey: "models")?.array)
                original.removeValue(forKey: "models")
                #expect(models.compactMap { $0["id"]?.string } == ["original", "shared", "from-default", "from-list"])
                #expect(models[1]["context_window"]?.int == 64000)
            case .replaceKey:
                #expect(updated.removeValue(forKey: "api_key")?.string == "fixture-only")
            case .add: break
            }
            #expect(updated == original)
        }
        var noDefault = existing
        noDefault.model = nil
        let partial = ProviderWizardModel(existing: noDefault, deps: .init(api: { nil }))
        partial.applyPaste(d, action: .merge("saved"))
        #expect(partial.defaultModel.isEmpty && partial.isPicked("from-default"))
        var noKey = d
        noKey.provider.apiKey = nil
        #expect(noKey.action(for: try #require(noKey.matches?.last)) == .merge("other"))
        let new = ProviderWizardModel(deps: .init(api: { nil }))
        new.applyPaste(d, action: .add)
        #expect(new.isNew && new.apiKey == "fixture-only" && new.env == "LAB_NOTE=synthetic-value")
        #expect(new.defaultModel == "from-default" && new.headers == "X-Team: synthetic-header")
    }

    @Test func eachParserNoteHasWordsAndUnknownCodesStillShowTheirLine() {
        let unknown = ProviderImportNote(code: "future-note", line: 12, name: "test-field").pasteText
        #expect(unknown.contains("12"))
        let codes = ["unrecognized", "invalid_json", "line_unused", "not_claude", "no_gateway", "url_shell",
                     "url_user", "url_query", "args_left_out", "header_unreadable", "key_shell", "no_key",
                     "key_helper", "curl_option", "curl_path", "cindy_kind", "cindy_auth", "cindy_endpoint",
                     "cindy_request_path", "too_many", "link_invalid", "link_version", "link_too_long", "link_field"]
        for code in codes {
            let text = ProviderImportNote(code: code, line: 12, name: "test-field").pasteText
            #expect(!text.isEmpty && text != unknown, "\(code) should explain this specific note")
        }
    }

    @Test func typingOneCharacterDebouncesAndPastingOverASelectionStartsAtOnce() {
        #expect(!ProviderPasteModel.insertedSeveralCharacters("A供B", replacing: "AB"))
        #expect(!ProviderPasteModel.insertedSeveralCharacters("AB", replacing: "A供B"))
        #expect(ProviderPasteModel.insertedSeveralCharacters("A123B", replacing: "AXYZB"))
        #expect(ProviderPasteModel.insertedSeveralCharacters("ANTHROPIC_MODEL=atlas", replacing: ""))
    }

    @Test func newerInputDiscardsAPreviousResponseAndFailureCanBeRetried() async throws {
        let hub = FakeHub()
        let (started, signal) = AsyncStream<Void>.makeStream()
        let (release, finish) = AsyncStream<Void>.makeStream()
        hub.onAsync("POST", route) { _ in
            signal.yield(())
            for await _ in release { break }
            return Data(#"{"format":"shell","drafts":[{"provider":{"display_name":"stale"}}]}"#.utf8)
        }
        let paste = ProviderPasteModel()
        paste.text = "initial fixture"
        let old = Task { await paste.read(using: hub.client) }
        for await _ in started { break }
        paste.text = "new fixture"
        finish.yield(())
        await old.value
        #expect(paste.answer == nil && paste.action == nil)
        await paste.read(using: nil)
        #expect(paste.failed && paste.text == "new fixture")
        paste.retry()
        let next = FakeHub()
        next.on("POST", route, json: #"{"format":"settings","drafts":[{"provider":{"display_name":"fresh"}}]}"#)
        await paste.read(using: next.client)
        #expect(paste.selected == 0 && paste.action == .add && paste.draft?.pasteTitle == "fresh")
        paste.text = " \n"
        await paste.read(using: next.client)
        #expect(paste.answer == nil && !paste.failed && next.requests.count == 1)
    }

    @Test func onlyUnimportedVisibleGatewayAliasesShowAndAnOnlineMachineWins() throws {
        let fixtures = #"""
        [{"name":"chosen","source":"zshrc","base_url":"https://lab.invalid","machines":["off","on"]},
         {"name":"offline","source":"zshrc","base_url":"https://lab.invalid","machines":["off"]},
         {"name":"already","source":"zshrc","base_url":"https://lab.invalid","machines":["off","on"]},
         {"name":"managed","source":"persona","origin":"zshrc:already@on"},
         {"name":"path","source":"path","base_url":"https://lab.invalid","machines":["on"]},
         {"name":"hidden","source":"zshrc","hidden":true,"base_url":"https://lab.invalid","machines":["on"]},
         {"name":"no-address","source":"zshrc","machines":["on"]},
         {"name":"no-machine","source":"zshrc","base_url":"https://lab.invalid"}]
        """#
        let list = try JSON.decoder.decode([LauncherView].self, from: Data(fixtures.utf8))
        let machines = [Machine(id: "off", name: "Offline", kind: "node", online: false),
                        Machine(id: "on", name: "Online", kind: "node", online: true)]
        let found = FoundProviderAlias.available(list, machines: machines)
        #expect(found.map(\.name) == ["chosen", "offline"])
        #expect(found.map(\.machine) == ["on", "off"] && found.map(\.online) == [true, false])
        #expect(found[0].computer == "Online")
    }

    @Test func oneAliasIsBoundToBothThePreviewAndConfirmedImportAndManualStillOmitsIt() async throws {
        for alias in [String?.some("lab_a"), nil] {
            let hub = FakeHub()
            let path = "/api/v1/launchers/import"
            hub.on("GET", path, json: #"{"machine":"test-node","providers":[{"name":"lab_a"}]}"#)
            hub.on("POST", path, json: #"{"created":["lab_a"]}"#)
            let model = LauncherImportModel(deps: .init(api: { hub.client.manager("assistant-a") }))
            model.machine = "test-node"
            model.alias = alias
            await model.load()
            let get = try #require(hub.sent("GET", path).first)
            #expect(get.query["machine"] == "test-node" && get.query["alias"] == alias)
            #expect(get.headers[APIClient.managerHeader] == "assistant-a" && hub.requests.count == 1)
            #expect(model.canImport && model.picked == ["lab_a"])
            await model.runImport()
            let post = try #require(hub.sent("POST", path).first)
            let body = try requestBody(post)
            #expect(body["machine"]?.string == "test-node" && body["alias"]?.string == alias)
            #expect(body["providers"]?.array?.compactMap(\.string) == ["lab_a"])
            #expect(post.headers[APIClient.managerHeader] == "assistant-a" && model.phase == .done)
            model.restart()
            #expect(model.alias == nil)
        }
    }
}
