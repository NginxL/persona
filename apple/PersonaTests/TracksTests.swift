import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The domain spaces' wire types and rules (#13500, apple/Shared/Tracks.swift, the web's lib/tracks.ts): decoding keeps
/// going through fields a hub leaves out or a newer one adds, and the Spaces section, the slugs and the kinds follow
/// the web's rules. Fixtures are made-up spaces (qa-plain, qa-finance).
struct TracksTests {
    private static func track(_ slug: String, title: String, group: String = "spaces", order: Int = 0,
                              archived: Bool = false) -> AssistantTrack {
        AssistantTrack(slug: slug, kind: "space", title: title,
                       config: TrackKindConfig(sidebar: TrackSidebar(group: group, order: order)), archived: archived)
    }

    @Test func aTrackDecodesWithUnknownAndMissingFields() throws {
        let json = #"""
        {"tracks":[
          {"slug":"qa-finance","kind":"space","title":"QA Finance","icon":"wallet","description":"","default_categories":["finance"],
           "template":"space","builtin":false,"archived":false,"revision":7,"future_field":{"x":1},
           "config":{"schema":1,"template":"space","context":"","default_categories":["finance"],"tag_focus":[],"rhythms":[],
                     "attention":{"push_cap":"","deep_default":"","enter_mode":""},"supervision":"","usage":[],"assembly":"",
                     "capture_target":false,"channels":[],"sidebar":{"group":"spaces","order":10,"icon":"layers"},"later":true}},
          {"slug":"qa-plain"}
        ]}
        """#
        struct List: Decodable { var tracks: [AssistantTrack] }
        let list = try JSON.decoder.decode(List.self, from: Data(json.utf8)).tracks
        #expect(list.count == 2)
        #expect(list[0].revision == 7 && list[0].defaultCategories == ["finance"] && list[0].config.sidebar.order == 10)
        #expect(list[0].config.sidebar.group == Tracks.spacesGroup)
        #expect(list[1].title.isEmpty && list[1].config.sidebar.group.isEmpty && !list[1].archived)
        #expect(list[1].name == "qa-plain", "a track without a title goes by its slug")
    }

    @Test func aKindSaysWhyItIsOff() throws {
        let json = #"{"kinds":[{"slug":"space","title":"Space","builtin":true,"revision":1},{"slug":"later","disabled":"schema","warnings":["x"]}]}"#
        struct List: Decodable { var kinds: [AssistantTrackKind] }
        let kinds = try JSON.decoder.decode(List.self, from: Data(json.utf8)).kinds
        #expect(kinds[0].disabled.isEmpty && kinds[0].builtin)
        #expect(kinds[1].disabled == "schema" && kinds[1].warnings == ["x"])
    }

    @Test func anAnswerWithoutTracksIsAnAssistantWithout() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks", json: #"{"ok":true}"#)
        await #expect(throws: TracksUnsupported.self) { try await hub.client.tracks() }
    }

    @Test func theTrackRoutesNameNoManager() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks", json: #"{"tracks":[]}"#)
        hub.on("GET", "/api/v1/assistant/extension-trust", json: #"{"items":[]}"#)
        _ = try await hub.client.showing("m-1").tracks()
        _ = try await hub.client.manager("m-1").extensionTrust()
        for req in hub.requests { #expect(req.headers[APIClient.managerHeader] == nil, "\(req.path) is the default manager's") }
    }

    @Test func theSpacesSectionListsTheSpacesInOrder() {
        let list = Tracks.sidebarSpaces([
            Self.track("qa-plain", title: "Plain", order: 20),
            Self.track("qa-finance", title: "Finance", order: 10),
            Self.track("qa-b", title: "Beta", order: 20),
            Self.track("health", title: "Health"),
            Self.track("qa-old", title: "Old", archived: true),
            Self.track("main-chat", title: "Main", group: ""),
        ])
        #expect(list.map(\.slug) == ["qa-finance", "qa-b", "qa-plain"])
    }

    @Test func slugsFollowTheServersRule() {
        #expect(Tracks.validSlug("qa-finance"))
        #expect(!Tracks.validSlug("q"), "too short")
        #expect(!Tracks.validSlug("1abc") && !Tracks.validSlug("Abc") && !Tracks.validSlug("a_b"))
        #expect(!Tracks.validSlug("health") && !Tracks.validSlug("tracks"))
        #expect(Tracks.validSlug("new") && !Tracks.formSlug("new"), "the server takes it; the clients keep it for a page")
        #expect(!Tracks.validSlug(String(repeating: "a", count: 33)))
    }

    @Test func aSlugIsSuggestedFromTheName() {
        #expect(Tracks.suggestSlug(title: "QA Finance") == "qa-finance")
        #expect(Tracks.suggestSlug(title: "Café Notes!") == "cafe-notes")
        #expect(Tracks.suggestSlug(title: "2026 Trip") == "trip")
        #expect(Tracks.suggestSlug(title: "家人") == "space", "no Latin letters: space") // i18n-ignore
        #expect(Tracks.suggestSlug(title: "家人", taken: ["space"]) == "space-2") // i18n-ignore
        #expect(Tracks.suggestSlug(title: "New") == "new-2", "a word the clients keep gets a number")
        #expect(Tracks.suggestSlug(title: "Health") == "health-2")
    }

    @Test func aNewSpaceIsOfAKindOnOffer() {
        let kinds = Tracks.spaceKinds([AssistantTrackKind(slug: "orchestration"), AssistantTrackKind(slug: "later", disabled: "schema"),
                                       AssistantTrackKind(slug: "journal")])
        #expect(kinds.map(\.slug) == ["journal"])
        #expect(Tracks.reconciledKind("space", in: kinds) == "journal", "the default kind missing: the first on offer")
        #expect(Tracks.reconciledKind("space", in: [AssistantTrackKind(slug: "space")] + kinds) == "space")
        #expect(Tracks.reconciledKind("space", in: []) == "space", "none read: kept")
    }

    @Test func anExtensionWithoutARowIsAThirdPartyWithoutGrants() {
        let items = [ExtensionTrust(extension: "qa-memory", trust: "owner"), ExtensionTrust(extension: "qa-mail", categories: ["finance"])]
        #expect(Tracks.trustOf(items, id: "qa-memory").isOwner)
        #expect(Tracks.trustOf(items, id: "qa-mail").categories == ["finance"])
        let none = Tracks.trustOf(items, id: "qa-other")
        #expect(!none.isOwner && none.categories.isEmpty)
    }

    @Test func aSpaceMdFailureHasPlainWords() {
        #expect(SpaceMdError(status: 409) == .stale && SpaceMdError(status: 400) == .managed)
        #expect(SpaceMdError(status: 404) == .missing && SpaceMdError(status: 503) == .offline && SpaceMdError(status: 500) == .other)
        #expect(SpaceMdError(URLError(.timedOut)) == .other)
    }

    @Test func everySpaceIconHasASymbolAndAnUnknownOneIsLayers() {
        #expect(SpaceIcon.tokens.count == 16)
        #expect(Set(SpaceIcon.tokens.map { SpaceIcon.symbol($0) }).count == 16, "one symbol per icon")
        #expect(SpaceIcon.symbol("rocket") == Theme.Icon.domainSpace)
        #expect(SpaceIcon.symbol(of: AssistantTrack(slug: "qa", icon: "wallet")) == Theme.Icon.pluginWallet)
        #expect(SpaceIcon.symbol(of: AssistantTrack(slug: "qa", icon: "#ff0000")) == Theme.Icon.domainSpace, "never a color or a raw name")
        #expect(SpaceIcon.symbol(of: nil) == Theme.Icon.domainSpace)
    }
}
