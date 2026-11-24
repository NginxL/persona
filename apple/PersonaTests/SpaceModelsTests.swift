import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Domain spaces' models against a FakeHub (#13500, docs/apple/ios.md "Domain spaces"): the sidebar's store, a space's
/// home, the SPACE.md editor, the new space form, a conversation's categories and what an extension may read. Fixtures
/// are made-up spaces (qa-plain, qa-finance).
@MainActor
struct SpaceModelsTests {
    private func deps(_ hub: FakeHub, toasts: ToastLog? = nil) -> AssistantDeps {
        AssistantDeps(api: { hub.client }, report: { _ in }, toast: { t in if let toasts { toasts.list.append(t) } }, open: { _ in })
    }

    private static func body(_ req: LocalRequest) -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: req.body ?? Data())) as? [String: Any] ?? [:]
    }

    private static func session(_ id: String, space: String?, archived: Bool = false, updated: TimeInterval) -> Session {
        let spaceField = space.map { #","space":"\#($0)""# } ?? ""
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","status":"idle","state":"idle","assistant":true,
         "archived":\(archived)\(spaceField),"updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    private static let tracks = #"""
    {"tracks":[
      {"slug":"health","kind":"health","title":"Health","builtin":true,"revision":1,"config":{"sidebar":{"group":"spaces"}}},
      {"slug":"qa-finance","kind":"space","title":"QA Finance","icon":"wallet","revision":3,"config":{"sidebar":{"group":"spaces","order":10}}},
      {"slug":"qa-plain","kind":"space","title":"QA Plain","revision":2,"config":{"sidebar":{"group":"spaces","order":10}}}
    ]}
    """#

    // MARK: The store

    @Test func theStoreListsTheSpacesHealthLeftOut() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks", json: Self.tracks)
        let store = SpaceStore(api: { hub.client.showing("m-1") })
        await store.load(hub: "g")
        #expect(store.supported == true)
        #expect(store.sidebar.map(\.slug) == ["qa-finance", "qa-plain"])
        #expect(store.taken == ["health", "qa-finance", "qa-plain"])
        #expect(hub.requests.allSatisfy { $0.headers[APIClient.managerHeader] == nil }, "the default manager's, whichever is shown")
    }

    @Test func anAssistantWithoutTracksHidesTheSection() async {
        let hub = FakeHub() // no route: 404
        let store = SpaceStore(api: { hub.client })
        await store.load(hub: "g")
        #expect(store.supported == false && store.sidebar.isEmpty)
    }

    @Test func aFailedReadKeepsWhatWasKnownAndAnotherHubForgetsIt() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks", json: Self.tracks)
        let store = SpaceStore(api: { hub.client })
        await store.load(hub: "g")
        hub.on("GET", "/api/v1/assistant/tracks") { _ in throw APIError(status: 500, message: "down") }
        await store.load(hub: "g")
        #expect(store.sidebar.count == 2 && store.supported == true)
        await store.load(hub: "other")
        #expect(store.tracks == nil && store.supported == nil)
    }

    @Test func aNotedTrackShowsAtOnce() async {
        let store = SpaceStore(api: { nil })
        store.note(AssistantTrack(slug: "qa-new", kind: "space", title: "New one", config: TrackKindConfig(sidebar: TrackSidebar(group: "spaces"))))
        #expect(store.sidebar.map(\.slug) == ["qa-new"] && store.supported == true)
        store.note(AssistantTrack(slug: "qa-new", kind: "space", title: "New one", config: TrackKindConfig(sidebar: TrackSidebar(group: "spaces")), archived: true))
        #expect(store.sidebar.isEmpty && store.track("qa-new")?.archived == true)
    }

    // MARK: A space's home

    @Test func theHomeReadsItsSpacesConversationsAndPage() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { req in
            #expect(req.query["space"] == "qa-finance")
            #expect(req.headers[APIClient.managerHeader] == nil)
            return Data(#"{"machine_id":"m","launcher":"ccc","cwd":"/home/me/.persona/tracks/qa-finance","conversations":[{"session_id":"f1","title":"Taxes","status":"idle","state":"idle","assistant":true}]}"#.utf8)
        }
        hub.on("GET", "/api/v1/assistant/tracks/qa-finance/space-md", json: ##"{"content":"# QA Finance\n\nBudget.","digest":"d1","state":"Filed."}"##)
        let page = SpaceHomeModel(slug: "qa-finance", deps: deps(hub))
        await page.load()
        #expect(page.loaded && page.fetched.map(\.sessionID) == ["f1"])
        #expect(page.fetched.first?.machineID == "m" && page.fetched.first?.assistantSpace == "qa-finance")
        #expect(page.fetched.first?.isDomainSpace == true)
        #expect(page.spaceMd.value?.state == "Filed.")
        // what the card shows is worked out with the file and the name, not on each redraw
        #expect(page.written == "# QA Finance\n\nBudget.")
        page.name = "QA Finance"
        #expect(page.written == "Budget.")
        page.saved(SpaceMd(content: "# QA Finance\n\nSavings.", digest: "d2", state: ""))
        #expect(page.written == "Savings.")
    }

    @Test func aPageThatCantBeReadSaysSoPlainly() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"m","launcher":"","cwd":"","conversations":[]}"#)
        let page = SpaceHomeModel(slug: "qa-plain", deps: deps(hub))
        await page.load()
        guard case .failed(let why) = page.spaceMd else { Issue.record("expected a failure"); return }
        #expect(why == SpaceMdWords.unavailable)
    }

    @Test func pushesGoOverTheListAndOnlyThisSpacesCount() {
        let fetched = [Self.session("a", space: "qa-finance", updated: 1)]
        let live = [Self.session("a", space: "qa-finance", updated: 5), Self.session("b", space: "qa-finance", archived: true, updated: 6),
                    Self.session("p", space: "qa-plain", updated: 9), Self.session("h", space: "health", updated: 9)]
        #expect(SpaceHomeModel.merge(fetched, live: live, slug: "qa-finance").map(\.sessionID) == ["a"])
        #expect(SpaceHomeModel.entry(fetched, live: live, slug: "qa-finance") == .open(Self.session("a", space: "qa-finance", updated: 5).key))
        #expect(SpaceHomeModel.entry([], live: live, slug: "qa-other") == .new)
    }

    @Test func archivingSendsTheRevision() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/assistant/tracks/qa-plain") { req in
            let body = Self.body(req)
            #expect(body["revision"] as? Int == 2 && body["archived"] as? Bool == true && body["title"] == nil)
            return Data(#"{"slug":"qa-plain","kind":"space","title":"QA Plain","archived":true,"revision":3,"config":{"sidebar":{"group":"spaces"}}}"#.utf8)
        }
        let store = SpaceStore(api: { nil })
        let track = AssistantTrack(slug: "qa-plain", kind: "space", title: "QA Plain", config: TrackKindConfig(sidebar: TrackSidebar(group: "spaces")), revision: 2)
        store.note(track)
        let page = SpaceHomeModel(slug: "qa-plain", deps: deps(hub))
        #expect(await page.archive(track, store: store))
        #expect(store.sidebar.isEmpty && store.track("qa-plain")?.revision == 3)
    }

    @Test func theListsKeepEverySpaceApart() {
        let all = [Self.session("main", space: nil, updated: 1), Self.session("f", space: "qa-finance", updated: 2),
                   Self.session("h", space: "health", updated: 3), Self.session("fOld", space: "qa-finance", archived: true, updated: 4)]
        let lists = AssistantLists(all, group: "")
        #expect(lists.open.map(\.sessionID) == ["main"])
        #expect(lists.archived.isEmpty, "an archived space conversation is the space's, not Archived's")
        #expect(lists.health.map(\.sessionID) == ["h"])
        #expect(lists.spaces.map(\.sessionID) == ["f"])
    }

    // MARK: SPACE.md

    private static let md = #"{"content":"Budget.\n\n<!-- persona:managed:state:begin -->\nFiled.\n<!-- persona:managed:state:end -->\n","digest":"d1","state":"Filed."}"#

    @Test func savingNamesTheVersionItStartedFrom() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks/qa-finance/space-md", json: Self.md)
        hub.on("PUT", "/api/v1/assistant/tracks/qa-finance/space-md") { req in
            let body = Self.body(req)
            #expect(body["base_digest"] as? String == "d1")
            #expect(body["content"] as? String == "Budget and taxes.\n\n<!-- persona:managed:state:begin -->\nFiled.\n<!-- persona:managed:state:end -->\n")
            return Data(#"{"content":"x","digest":"d2","state":""}"#.utf8)
        }
        let editor = SpaceMdEditorModel(slug: "qa-finance", deps: deps(hub))
        await editor.load()
        #expect(editor.shape?.editable == [0] && !editor.changed)
        editor.edits[0] = "Budget and taxes.\n\n"
        #expect(editor.changed)
        let saved = await editor.save()
        #expect(saved?.digest == "d2" && editor.saveError == nil && editor.edits.isEmpty)
    }

    @Test func aStaleSaveKeepsWhatWasTyped() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks/qa-finance/space-md", json: Self.md)
        hub.on("PUT", "/api/v1/assistant/tracks/qa-finance/space-md") { _ in throw APIError(status: 409, message: "stale") }
        let editor = SpaceMdEditorModel(slug: "qa-finance", deps: deps(hub))
        await editor.load()
        editor.edits[0] = "Typed.\n\n"
        #expect(await editor.save() == nil)
        #expect(editor.saveError == .stale && editor.edits[0] == "Typed.\n\n")
        hub.on("PUT", "/api/v1/assistant/tracks/qa-finance/space-md") { _ in throw APIError(status: 503, message: "offline") }
        _ = await editor.save()
        #expect(editor.saveError == .offline)
    }

    @Test func aTypedMarkerIsRefusedBeforeItIsSent() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tracks/qa-finance/space-md", json: Self.md)
        let editor = SpaceMdEditorModel(slug: "qa-finance", deps: deps(hub))
        await editor.load()
        editor.edits[0] = "<!-- persona:managed:state:begin -->mine<!-- persona:managed:state:end -->\n"
        #expect(await editor.save() == nil)
        #expect(editor.saveError == .managed)
        #expect(hub.sent("PUT", "/api/v1/assistant/tracks/qa-finance/space-md").isEmpty)
    }

    // MARK: New space

    @Test func aNewSpaceTakesAKindOnOfferAndItsAddressFromTheName() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/track-kinds", json: #"{"kinds":[{"slug":"orchestration"},{"slug":"journal","title":"Journal"},{"slug":"later","disabled":"schema"}]}"#)
        hub.on("POST", "/api/v1/assistant/tracks") { req in
            let body = Self.body(req)
            #expect(body["slug"] as? String == "space-2" && body["kind"] as? String == "journal")
            #expect(body["title"] as? String == "家人") // i18n-ignore
            #expect(body["icon"] as? String == "users" && body["default_categories"] as? [String] == ["finance", "relationships"])
            return Data(#"{"slug":"space-2","kind":"journal","title":"家人","revision":1}"#.utf8) // i18n-ignore
        }
        let form = NewSpaceModel(deps: deps(hub))
        await form.loadKinds()
        #expect(form.kinds.map(\.slug) == ["journal"] && form.kind == "journal")
        #expect(!form.ready)
        form.title = "  家人 " // i18n-ignore
        form.icon = "users"
        form.toggle("relationships")
        form.toggle("finance")
        #expect(form.ready)
        let made = await form.create(taken: ["space"])
        #expect(made?.slug == "space-2" && form.error == nil)
    }

    @Test func anAddressTakenMeanwhileGetsTheNextOne() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/tracks") { req in
            if Self.body(req)["slug"] as? String == "qa-trip" { throw APIError(status: 409, message: "taken") }
            return Data(#"{"slug":"qa-trip-2","kind":"space","title":"QA Trip"}"#.utf8)
        }
        let form = NewSpaceModel(deps: deps(hub))
        form.title = "QA Trip"
        #expect(await form.create(taken: [])?.slug == "qa-trip-2")
        #expect(hub.sent("POST", "/api/v1/assistant/tracks").count == 2)
        #expect(Self.body(hub.sent("POST", "/api/v1/assistant/tracks")[0])["default_categories"] as? [String] == [],
                "no category chosen is sent as none, not left to the kind's own")
    }

    // MARK: Categories

    @Test func removingACategorySendsItAndTheSpacesOwnStay() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/conversations/f1/categories", json: #"{"categories":["finance","relationships"],"floor":["finance"],"removed":[]}"#)
        hub.on("PUT", "/api/v1/assistant/conversations/f1/categories") { req in
            let body = Self.body(req)
            #expect(body["remove"] as? [String] == ["relationships"] && body["add"] == nil)
            return Data(#"{"categories":["finance"],"floor":["finance"],"removed":["relationships"]}"#.utf8)
        }
        let toasts = ToastLog()
        let chips = ConversationCategoriesModel(sessionID: "f1", deps: deps(hub, toasts: toasts))
        await chips.load()
        #expect(chips.shows && !chips.removable("finance") && chips.removable("relationships"))
        await chips.remove("finance")
        #expect(hub.sent("PUT", "/api/v1/assistant/conversations/f1/categories").isEmpty, "the space's own one stays")
        await chips.remove("relationships")
        #expect(chips.categories?.removed == ["relationships"] && toasts.list.last?.style == .success)
    }

    @Test func aRefusedChangeIsPutBack() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/conversations/f1/categories", json: #"{"categories":["emotion"],"floor":[],"removed":[]}"#)
        hub.on("PUT", "/api/v1/assistant/conversations/f1/categories") { _ in throw APIError(status: 500, message: "no") }
        let toasts = ToastLog()
        let chips = ConversationCategoriesModel(sessionID: "f1", deps: deps(hub, toasts: toasts))
        await chips.load()
        await chips.remove("emotion")
        #expect(chips.categories?.categories == ["emotion"] && chips.busy == nil && toasts.list.last?.style == .error)
    }

    @Test func noCategoriesNoChips() async {
        let hub = FakeHub() // an older assistant: 404
        let chips = ConversationCategoriesModel(sessionID: "f1", deps: deps(hub))
        await chips.load()
        #expect(!chips.shows)
    }

    // MARK: Extension trust

    @Test func anExtensionWithoutARowReadsNoPersonalInformation() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/extension-trust", json: #"{"items":[{"extension":"qa-other","trust":"owner","categories":[]}]}"#)
        hub.on("PUT", "/api/v1/assistant/extension-trust/qa-memory") { req in
            let body = Self.body(req)
            #expect(body["trust"] as? String == "owner" && body["extension"] == nil)
            return Data(#"{"extension":"qa-memory","trust":"owner","categories":[]}"#.utf8)
        }
        let trust = ExtensionTrustModel(extensionID: "qa-memory", deps: deps(hub))
        await trust.load()
        #expect(trust.trust?.isOwner == false && trust.trust?.categories == [])
        await trust.setOwner(true)
        #expect(trust.trust?.isOwner == true && !trust.busy)
    }

    @Test func aRefusedGrantIsPutBackAndAnOlderAssistantShowsNothing() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/extension-trust", json: #"{"items":[]}"#)
        hub.on("PUT", "/api/v1/assistant/extension-trust/qa-mail") { _ in throw APIError(status: 500, message: "no") }
        let trust = ExtensionTrustModel(extensionID: "qa-mail", deps: deps(hub))
        await trust.load()
        await trust.allowCategory("finance", on: true)
        #expect(trust.trust?.categories == [])
        let old = ExtensionTrustModel(extensionID: "qa-mail", deps: deps(FakeHub()))
        await old.load()
        #expect(old.trust == nil)
        let noItems = FakeHub()
        noItems.on("GET", "/api/v1/assistant/extension-trust", json: #"{}"#)
        let none = ExtensionTrustModel(extensionID: "qa-mail", deps: deps(noItems))
        await none.load()
        #expect(none.trust == nil)
    }

    // MARK: Routes

    @Test func aSpaceIsAShadedPlace() {
        #expect(SidebarPageRoot.page(SidebarRoute.newSpace).place == .newSpace)
        #expect(SidebarPageRoot.page(SidebarRoute.space("qa-plain")).place == .space("qa-plain"))
        #expect(SidebarPageRoot.of(.newSpace) == .page(SidebarRoute.newSpace))
        #expect(SidebarPageRoot.of(.space("qa-plain")) == nil, "it opens its latest conversation or its home, as Health")
        #expect(!ShellPlaces.isPlace(.space("qa-plain")) && ShellPage.space("qa-plain").isSidebarPlace)
    }
}

/// The toasts a model showed.
@MainActor final class ToastLog {
    var list: [Toast] = []
}
