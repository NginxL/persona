import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct ExtensionModelCandidateTests {
    private static let id = "extension.editor-fixture"
    private static let endpoint = "/api/v1/model-config/purposes/" + id
    private static let providers = #"[{"id":"launcher.first","name":"First gateway","kind":"api","api":"responses","has_key":true},{"id":"launcher.second","name":"Second gateway","kind":"launcher","api":"messages","has_key":true},{"id":"launcher.cli","name":"Terminal","kind":"launcher"},{"id":"launcher.inferred","name":"Implicit Messages","kind":"launcher","has_key":true},{"id":"launcher.responses-cli","name":"Responses CLI","kind":"launcher","api":"responses","has_key":true},{"id":"launcher.no-key","name":"No credential","kind":"api","has_key":false},{"id":"launcher.hidden","name":"Hidden","kind":"api","hidden":true,"has_key":true},{"id":"legacy.ai","name":"Legacy","kind":"api","has_key":true}]"#
    private static let policy = #"{"mode":"extension","custom":[],"requested":[{"last_used":"2026-11-19T07:08:09.123456789Z","models":[{"model":"text.small","provider":"launcher.first","provider_name":"First gateway","available":true},{"model":"text.absent","available":false,"problem":"No provider offers this model"}]}],"modes":["extension","off","custom"],"most":2,"direct_provider_ids":["launcher.first","launcher.second","launcher.other"]}"#
    private static func detail(_ policy: String? = ExtensionModelCandidateTests.policy, id: String = ExtensionModelCandidateTests.id, revision: Int = 5) -> String {
        let extra = policy.map { #","candidates":\#($0)"# } ?? ""
        return #"{"purpose":{"id":"\#(id)","name":"Reading helper","kind":"api","group":"Extensions","parent":"kb","category_id":"kb","override":{},"effective":{"provider":"launcher.first","model":"text.standard"},"revision":\#(revision),"fields":["provider","model","effort"],"provider_ids":["launcher.first","launcher.second","launcher.cli","launcher.inferred","launcher.responses-cli","launcher.no-key","launcher.hidden","legacy.ai"]\#(extra)},"providers":\#(providers)}"#
    }
    private func page(_ hub: FakeHub, _ policy: String? = ExtensionModelCandidateTests.policy) async -> ModelConfigurationModel {
        hub.on("GET", Self.endpoint, json: Self.detail(policy))
        let page = ModelConfigurationModel(deps: .init(api: { hub.client }))
        await page.load(purposeID: Self.id)
        return page
    }

    @Test func purposeRetainsCandidateHistoryResolutionAndLastUse() throws {
        let detail = try JSON.decoder.decode(ModelPurposeDetail.self, from: Data(Self.detail().utf8))
        let roundTrip = try JSONSerialization.jsonObject(with: JSONEncoder().encode(detail.purpose)) as? [String: Any]
        let candidates = try #require(roundTrip?["candidates"] as? [String: Any])
        let chain = try #require((candidates["requested"] as? [[String: Any]])?.first)
        let models = try #require(chain["models"] as? [[String: Any]])
        #expect(chain["last_used"] != nil && models.count == 2)
        #expect(models[0]["provider_name"] as? String == "First gateway")
        #expect(models[1]["problem"] as? String == "No provider offers this model")
    }

    @Test func anExtensionBindingEditSendsOnlyTheChangedFieldAndRevision() async throws {
        let hub = FakeHub()
        let page = await page(hub)
        hub.on("PATCH", Self.endpoint, json: Self.detail(revision: 6))
        page.draft.model = "text.new"
        #expect(await page.save())
        let data = try #require(hub.sent("PATCH", Self.endpoint).first?.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(Set(body.keys) == ["revision", "model"])
        #expect(body["revision"] as? Int == 5 && body["model"] as? String == "text.new")
    }

    private func patch(_ hub: FakeHub) throws -> [String: Any] {
        let request = try #require(hub.sent("PATCH", Self.endpoint).last?.body)
        return try #require(JSONSerialization.jsonObject(with: request) as? [String: Any])
    }

    private func policy(mode: String = "extension", custom: [[String: Any]] = [], direct: [String]? = ["launcher.first", "launcher.second", "launcher.other"]) throws -> String {
        var value = try #require(JSONSerialization.jsonObject(with: Data(Self.policy.utf8)) as? [String: Any])
        value["mode"] = mode
        value["custom"] = custom
        value["direct_provider_ids"] = direct
        return String(decoding: try JSONSerialization.data(withJSONObject: value), as: UTF8.self)
    }

    @Test func switchingOffAndRestoringTheExtensionOnlyChangeCandidates() async throws {
        let hub = FakeHub()
        let page = await page(hub)
        let off = try policy(mode: "off")
        hub.on("PATCH", Self.endpoint, json: Self.detail(off, revision: 6))
        page.candidateMode = "off"
        #expect(await page.save())
        let first = try patch(hub)
        #expect(Set(first.keys) == ["revision", "candidates"])
        #expect(first["revision"] as? Int == 5)
        #expect(first["candidates"] as? [String: String] == ["mode":"off"])
        #expect(!page.dirty)
        page.candidateMode = "extension"
        hub.on("PATCH", Self.endpoint, json: Self.detail(revision: 7))
        #expect(await page.save())
        let restored = try patch(hub)
        #expect(Set(restored.keys) == ["revision","candidates"])
        #expect(restored["candidates"] is NSNull)
        #expect(restored["revision"] as? Int == 6)
    }

    @Test func customEditsPreserveOrderRawProviderDefaultsAndTheLimit() async throws {
        let custom: [[String: Any]] = [
            ["provider":"launcher.first","model":"resolved.standard","configured_model":"","available":true],
            ["provider":"launcher.second","model":"legacy.model","available":true]
        ]
        let hub = FakeHub()
        let page = await page(hub, try policy(mode: "custom", custom: custom))
        #expect(page.candidateRows.map(\.choice.model) == ["", "legacy.model"])
        #expect(!page.dirty)
        page.addCandidate()
        #expect(page.candidateRows.count == 2)
        let id = page.candidateRows[1].id
        page.moveCandidate(id, by: -1)
        page.moveCandidate(id, by: -1)
        #expect(page.candidateRows.first?.id == id)
        page.candidateRows[0].choice.model = " compact.new "
        hub.on("PATCH", Self.endpoint, json: Self.detail(revision: 6))
        #expect(await page.save())
        let body = try patch(hub)
        #expect(Set(body.keys) == ["revision", "candidates"])
        let saved = try #require(body["candidates"] as? [String: Any])
        #expect(saved["mode"] as? String == "custom")
        #expect(saved["custom"] as? [[String: String]] == [
            ["provider":"launcher.second","model":"compact.new"],
            ["provider":"launcher.first","model":""]
        ])
    }

    @Test func authoritativelyEmptyAndLegacyProviderListsStayDistinct() async throws {
        let explicit = await page(FakeHub(), try policy(direct: []))
        #expect(explicit.candidateProviders.isEmpty)
        explicit.candidateMode = "custom"
        explicit.addCandidate()
        #expect(explicit.candidateRows.isEmpty)
        #expect(!explicit.validCandidateChange)
        let narrowed = await page(FakeHub(), try policy(direct: ["launcher.other","launcher.second"]))
        #expect(narrowed.candidateProviders.map(\.id) == ["launcher.second"])
        let legacy = await page(FakeHub(), try policy(direct: nil))
        #expect(legacy.candidateProviders.map(\.id) == ["launcher.first","launcher.second","launcher.inferred"])
    }

    @Test func customRequiresOneOrMoreDistinctAllowedPairsBeforeSaving() async throws {
        let hub = FakeHub()
        let page = await page(hub)
        page.candidateMode = "custom"
        #expect(!page.validCandidateChange)
        page.addCandidate()
        page.addCandidate()
        #expect(page.candidateRows.map(\.choice.provider) == ["launcher.first","launcher.second"])
        #expect(page.validCandidateChange)
        page.candidateRows[1].choice = page.candidateRows[0].choice
        #expect(!page.validCandidateChange)
        page.candidateRows[1].choice.provider = "launcher.cli"
        #expect(!page.validCandidateChange)
        #expect(await page.save() == false)
        #expect(hub.sent("PATCH", Self.endpoint).isEmpty)
        page.candidateRows.removeLast()
        #expect(page.validCandidateChange)
    }

    @Test func conflictKeepsCustomRowsAndBindingUntilUserReloads() async throws {
        let hub = FakeHub()
        let page = await page(hub)
        page.candidateMode = "custom"
        page.addCandidate()
        page.candidateRows[0].choice.model = "edited"
        let draft = page.candidateRows[0].id
        page.draft.model = "purpose-edited"
        hub.on("PATCH", Self.endpoint) { _ in throw APIError(status: 409, message: "Changed elsewhere") }
        #expect(await page.save() == false)
        #expect(page.conflict && page.dirty)
        #expect(page.candidateRows.first?.id == draft)
        #expect(page.candidateRows.first?.choice.model == "edited")
        #expect(page.draft.model == "purpose-edited")
        #expect(await page.save() == false)
        #expect(hub.sent("PATCH", Self.endpoint).count == 1)
        hub.on("GET", Self.endpoint, json: Self.detail(try policy(mode: "off"), revision: 9))
        await page.load(purposeID: Self.id)
        #expect(!page.conflict && !page.dirty)
        #expect(page.candidateMode == "off" && page.candidateRows.isEmpty)
        #expect(page.purpose?.revision == 9)
    }

    @Test func latestListsAndTheirFractionalTimestampsKeepHubOrder() async throws {
        let previous = #"{"last_used":"2026-11-18T12:00:00Z","models":[{"model":"older","available":false}]}"#
        let two = Self.policy.replacingOccurrences(of: #"}]}],"modes""#, with: #"}]},\#(previous)],"modes""#)
        let page = await page(FakeHub(), two)
        let requests = try #require(page.purpose?.extensionCandidates?.requested)
        #expect(requests.map(\.id) == [["text.small","text.absent"],["older"]])
        let seconds = try Date.ISO8601FormatStyle().parse("2026-11-19T07:08:09Z")
        #expect(abs(requests[0].lastUsed.timeIntervalSince(seconds) - 0.123456789) < 0.001)
    }

    @Test func unavailableStoredChoicesDoNotPreventAnUnrelatedBindingEdit() async throws {
        let stale: [[String: Any]] = [["provider":"launcher.missing","model":"old","available":false,"problem":"Provider removed"]]
        let hub = FakeHub()
        let value = try policy(mode: "custom", custom: stale)
        let page = await page(hub, value)
        page.draft.model = "choose-later"
        #expect(page.validCandidateChange)
        hub.on("PATCH", Self.endpoint, json: Self.detail(value, revision: 6))
        #expect(await page.save())
        let body = try patch(hub)
        #expect(Set(body.keys) == ["revision","model"])
        page.candidateRows[0].choice.model = "edited"
        #expect(!page.validCandidateChange)
    }

    @Test func customCannotBeSavedWhenTheHubDoesNotOfferTheMode() async {
        let restricted = Self.policy.replacingOccurrences(of: #"["extension","off","custom"]"#, with: #"["extension","off"]"#)
        let page = await page(FakeHub(), restricted)
        page.candidateMode = "custom"
        page.addCandidate()
        #expect(!page.validCandidateChange)
        #expect(await page.save() == false)
    }

    @Test func simultaneousBindingAndPolicyChangeShareOneRevision() async throws {
        let hub = FakeHub()
        let detail = Self.detail().replacingOccurrences(of: #""override":{}"#, with: #""override":{"model":"old","provider":"launcher.first","effort":"low"}"#)
        hub.on("GET", Self.endpoint, json: detail)
        hub.on("PATCH", Self.endpoint, json: Self.detail(revision: 6))
        let page = ModelConfigurationModel(deps: .init(api: { hub.client }))
        await page.load(purposeID: Self.id)
        page.candidateMode = "off"
        page.draft.model = nil
        #expect(await page.save())
        let body = try patch(hub)
        #expect(hub.sent("PATCH", Self.endpoint).count == 1)
        #expect(Set(body.keys) == ["model","revision","candidates"])
        #expect(body["model"] is NSNull && body["revision"] as? Int == 5)
        #expect(body["candidates"] as? [String: String] == ["mode":"off"])
    }

    @Test func olderHubsAndOtherPurposesHaveNoCandidateEditor() async throws {
        let hub = FakeHub()
        let old = await page(hub, nil)
        #expect(old.purpose?.extensionCandidates == nil)
        old.draft.model = "only.model"
        hub.on("PATCH", Self.endpoint, json: Self.detail(nil, revision: 6))
        #expect(await old.save())
        let body = try patch(hub)
        #expect(Set(body.keys) == ["revision", "model"])
        let other = try JSON.decoder.decode(ModelPurposeDetail.self, from: Data(Self.detail(id: "kb").utf8))
        #expect(other.purpose.extensionCandidates == nil)
    }
}
