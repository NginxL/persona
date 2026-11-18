import Foundation
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// A conversation an extension opened is marked with that extension's own icon (#10358, the web's
/// OriginMark.opener.test.tsx and opener-icons.test.ts): the extension is `opened_by`, else `origin_ref`; its icon is
/// one of the hub's icon routes, asked of its own manager, and is drawn only for the conversation's manager's
/// extension; the Lark extension keeps Lark's mark; the words say the extension's name only while its icon is drawn,
/// and every other case keeps the origin's mark and words. Made-up extensions, ids and pictures.
@MainActor
struct ConversationOpenerTests {
    private static let route = "/api/v1/extensions/chat-bridge/-/icon?v=1.2.0"
    private static let catalogRoute = "/api/v1/extensions/-/catalog/3f2a9c1d0b/chat-bridge/icon?sha256=" + String(repeating: "ab", count: 32)

    private func session(origin: String?, openedBy: String? = nil, originRef: String? = nil, manager: String? = nil) throws -> Session {
        var o: [String: Any] = ["machine_id": "server", "session_id": "s1", "status": "idle", "state": "idle", "title": "", "assistant": true]
        if let origin { o["origin"] = origin }
        if let openedBy { o["opened_by"] = openedBy }
        if let originRef { o["origin_ref"] = originRef }
        if let manager { o["manager"] = manager }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private static let bridge = OpenerListing(id: "chat-bridge", name: " Chat Bridge ", iconURL: route, brings: [])
    private static let lark = OpenerListing(id: "larkext", name: "Lark", iconURL: "/api/v1/extensions/larkext/-/icon?v=3.0.0", brings: ["lark"])

    // MARK: Which extension opened it

    @Test func theOpenerIsOpenedByElseOriginRef() throws {
        #expect(try session(origin: "lark_agent", openedBy: "chat-bridge", originRef: "other").originRef == "other")
        #expect(ConversationOpener.of(try session(origin: "lark_agent", openedBy: "chat-bridge", originRef: "other"))
                == ConversationOpener(id: "chat-bridge"))
        #expect(ConversationOpener.of(try session(origin: "extension", originRef: " docs-helper ", manager: "work"))
                == ConversationOpener(id: "docs-helper", manager: "work"))
        #expect(ConversationOpener.of(try session(origin: " lark_agent ", openedBy: "  ", originRef: "chat-bridge"))?.id == "chat-bridge")
        // A conversation nobody noted (the hub's backfill judged it Lark /agent): today's mark.
        #expect(ConversationOpener.of(try session(origin: "lark_agent")) == nil)
        #expect(ConversationOpener.of(try session(origin: "lark_agent", openedBy: "", originRef: "")) == nil)
        // Only the two origins an extension opens.
        for origin in [nil, "", "user", "channel", "inbox", "task", "spawned", "something_new"] {
            #expect(ConversationOpener.of(origin: origin, openedBy: "chat-bridge", originRef: "chat-bridge") == nil, "\(origin ?? "nil")")
        }
    }

    // MARK: Which icon is drawn

    @Test func onlyTheHubsOwnIconRoutes() {
        #expect(ConversationOpener.isHubIcon(Self.route))
        #expect(ConversationOpener.isHubIcon(Self.catalogRoute))
        #expect(ConversationOpener.isHubIcon("/api/v1/extensions/chat-bridge/-/icon?v=1.0.0%2Bbuild.7"))
        for odd in ["https://images.example.com/chat-bridge.png", "/api/v1/extensions/chat-bridge/-/icon",
                    "/api/v1/extensions/chat-bridge/-/icon?v=", "/api/v1/extensions/chat-bridge/-/icon?w=1",
                    "/api/v1/extensions/chat-bridge/-/icon?v=1.0#x", "/api/v1/extensions/chat-bridge/-/icon?v=1.0&x=1",
                    "/api/v1/extensions/../-/icon?v=1", "/api/v1/extensions/chat bridge/-/icon?v=1",
                    "/api/v1/extensions/chat-bridge/-/image/abc?v=1", "/api/v1/extensions/a/b/-/icon?v=1",
                    "api/v1/extensions/chat-bridge/-/icon?v=1", "/api/v2/extensions/chat-bridge/-/icon?v=1",
                    "/api/v1/extensions/" + String(repeating: "a", count: 65) + "/-/icon?v=1",
                    "/api/v1/extensions/-/catalog/m/chat-bridge/icon?sha256=abc",
                    "/api/v1/extensions/-/catalog/m/chat-bridge/icon?sha256=" + String(repeating: "g", count: 64),
                    "/api/v1/extensions/拓展/-/icon?v=1", ""] {
            #expect(!ConversationOpener.isHubIcon(odd), "\(odd)")
        }
    }

    @Test func picksTheConversationsManagersExtensionWithItsIconAndName() {
        let opener = ConversationOpener(id: "chat-bridge", manager: "work")
        let other = OpenerListing(id: "chat-bridge", name: "Home Bridge", manager: "home", iconURL: "/api/v1/extensions/chat-bridge/-/icon?v=0.9")
        let mine = OpenerListing(id: "chat-bridge", name: "Chat Bridge", manager: "work", iconURL: Self.route)
        let icon = opener.icon(in: [other, mine], brand: Theme.Brand.lark) { $0 == "work" }
        #expect(icon == OpenerIcon(route: Self.route, manager: "work", name: "Chat Bridge"))
        // Installed only under another manager: today's mark.
        #expect(opener.icon(in: [other], brand: Theme.Brand.lark) { $0 == "work" } == nil)
        // A catalog icon is one of the hub's routes too; the name is trimmed, and an empty one is none.
        let catalog = OpenerListing(id: "chat-bridge", name: "   ", iconURL: Self.catalogRoute)
        #expect(ConversationOpener(id: "chat-bridge").icon(in: [catalog], brand: nil) { _ in true }
                == OpenerIcon(route: Self.catalogRoute, manager: nil, name: nil))
        #expect(ConversationOpener(id: "chat-bridge").icon(in: [Self.bridge], brand: nil) { _ in true }?.name == "Chat Bridge")
    }

    @Test func everyOtherCaseKeepsTheOriginsMark() {
        let opener = ConversationOpener(id: "chat-bridge")
        let all: (String?) -> Bool = { _ in true }
        #expect(opener.icon(in: nil, brand: Theme.Brand.lark, sameManager: all) == nil, "the list is not read yet")
        #expect(opener.icon(in: [], brand: Theme.Brand.lark, sameManager: all) == nil, "not installed (any more)")
        #expect(opener.icon(in: [OpenerListing(id: "chat-bridge", name: "Chat Bridge")], brand: nil, sameManager: all) == nil, "no icon")
        #expect(opener.icon(in: [OpenerListing(id: "chat-bridge", iconURL: "https://images.example.com/a.png")], brand: nil, sameManager: all) == nil,
                "public artwork only")
        #expect(opener.icon(in: [OpenerListing(id: "chat-bridgex", iconURL: Self.route)], brand: nil, sameManager: all) == nil, "another id")
    }

    /// The extension Lark's mark already stands for (it brings Lark's connector or messaging source) keeps Lark's mark
    /// for its own /agent; the check reads its contributions, not its id. Another origin has no brand, so its icon is drawn.
    @Test func theLarkExtensionKeepsLarksMark() {
        let all: (String?) -> Bool = { _ in true }
        #expect(ConversationOpener(id: "larkext").icon(in: [Self.lark], brand: Theme.Brand.lark, sameManager: all) == nil)
        for brings in [["lark"], ["larkcli"], ["examplenotes", "lark"]] {
            let x = OpenerListing(id: "any-id", iconURL: Self.route, brings: brings)
            #expect(ConversationOpener(id: "any-id").icon(in: [x], brand: Theme.Brand.lark, sameManager: all) == nil, "\(brings)")
        }
        #expect(ConversationOpener(id: "larkext").icon(in: [Self.lark], brand: nil, sameManager: all) != nil)
        let telegram = OpenerListing(id: "tg", iconURL: Self.route, brings: ["telegram"])
        #expect(ConversationOpener(id: "tg").icon(in: [telegram], brand: Theme.Brand.lark, sameManager: all) != nil)
    }

    @Test func managersAsTheAppTellsThem() {
        let roster = ManagerRoster(managers: [HubManager(id: "assistant", name: "Main", online: true, isDefault: true, builtin: true),
                                              HubManager(id: "work", name: "Work", online: true)])
        let one = ManagerRoster(managers: [HubManager(id: "assistant", name: "Main", online: true, isDefault: true, builtin: true)])
        #expect(ConversationOpener.sameManager("home", "work", shown: nil, roster: nil), "a hub without a roster")
        #expect(ConversationOpener.sameManager("home", "work", shown: nil, roster: one), "one manager")
        #expect(ConversationOpener.sameManager("work", "work", shown: nil, roster: roster))
        #expect(ConversationOpener.sameManager("work", nil, shown: "work", roster: roster), "the shown assistant's conversation")
        #expect(ConversationOpener.sameManager(nil, "assistant", shown: "work", roster: roster), "unstamped is the default's")
        #expect(!ConversationOpener.sameManager("work", "assistant", shown: nil, roster: roster))
        #expect(!ConversationOpener.sameManager("assistant", nil, shown: "work", roster: roster))
    }

    @Test func readsTheListLeniently() throws {
        let json = #"""
        {"extensions": [
          {"id": "chat-bridge", "name": "Chat Bridge", "manager": "work", "icon_url": "/api/v1/extensions/chat-bridge/-/icon?v=1.2.0",
           "contributes": {"connectors": ["examplenotes", 3], "messaging": ["lark"], "channel": ["wechat"]}, "status": "running"},
          {"id": "odd", "name": 7, "icon_url": {"x": 1}, "contributes": ["not", "a", "map"]},
          {"name": "no id"}
        ], "can_install": true}
        """#
        let items = try JSON.decoder.decode(OpenerListings.self, from: Data(json.utf8)).items
        #expect(items == [OpenerListing(id: "chat-bridge", name: "Chat Bridge", manager: "work", iconURL: Self.route, brings: ["examplenotes", "lark"]),
                          OpenerListing(id: "odd")])
        let old = try JSON.decoder.decode(OpenerListings.self, from: Data(#"{"plugins": [{"id": "chat-bridge"}]}"#.utf8)).items
        #expect(old.map(\.id) == ["chat-bridge"])
    }

    // MARK: The words

    /// 「来自 <name>」 with the existing "From %@" (the tests run in zh-Hans); without a name, the origin's words.
    @Test func theWordsNameTheExtensionOnlyWithAName() {
        #expect(AutoConversationSource.larkAgent.from(opener: "Chat Bridge") == "来自 Chat Bridge")
        #expect(AutoConversationSource.larkAgent.markLabel(opener: "Chat Bridge") == "来自 Chat Bridge")
        #expect(AutoConversationSource.extensionSource.from(opener: "Docs Helper") == "来自 Docs Helper")
        #expect(AutoConversationSource.larkAgent.from(opener: nil) == "来自 飞书 /agent")
        #expect(AutoConversationSource.larkAgent.markLabel(opener: nil) == "来自 飞书 /agent")
        #expect(AutoConversationSource.extensionSource.markLabel(opener: nil) == "来自扩展")
        #expect(AutoConversationSource.inbox.markLabel(opener: nil) == "收件箱")
    }

    // MARK: The store

    nonisolated private final class Seen: @unchecked Sendable {
        private let lock = NSLock()
        private var all: [LocalRequest] = []
        func add(_ r: LocalRequest) { lock.withLock { all.append(r) } }
        var requests: [LocalRequest] { lock.withLock { all } }
    }

    private struct Hub {
        var icons: OpenerIcons
        var seen: Seen
    }

    /// A store on a fake hub that lists `list` (a JSON array of extensions) and answers icon routes with `picture`.
    private func hub(_ list: String, picture: Data?, features: Set<String> = ["extensions"], now: @escaping () -> Date = { .now },
                     roster: ManagerRoster? = nil, shown: String? = nil) throws -> Hub {
        let seen = Seen()
        let config = try #require(ServerConfig(pairURL: "persona://pair?url=https%3A%2F%2Fhub.example.com&token=fixture"))
        var api = APIClient(config: config, transport: { req in
            seen.add(req)
            if req.path == "/api/v1/extensions" { return Data(#"{"extensions": \#(list)}"#.utf8) }
            if req.path.hasSuffix("/icon"), let picture { return picture }
            throw APIError(status: 404, message: "not found")
        })
        api.extensionsAPI = true
        var deps = OpenerIcons.Deps.live
        deps.api = { api }
        deps.features = { features }
        deps.now = now
        deps.sameManager = { ConversationOpener.sameManager($0, $1.manager, shown: shown, roster: roster) }
        return Hub(icons: OpenerIcons(deps: deps), seen: seen)
    }

    private static func png(side: Int = 128) -> Data {
        UIGraphicsImageRenderer(size: CGSize(width: side, height: side), format: {
            let f = UIGraphicsImageRendererFormat()
            f.scale = 1
            return f
        }()).pngData { ctx in
            UIColor.systemTeal.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: side, height: side))
        }
    }

    /// Waits (3 s at most) until `done`: the store's reads got as far as the case needs.
    private func until(_ what: String, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async throws {
        for _ in 0..<300 {
            if done() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(done(), "timed out waiting: \(what)", sourceLocation: sourceLocation)
    }

    private static let bridgeJSON = #"[{"id": "chat-bridge", "name": "Chat Bridge", "manager": "work", "icon_url": "/api/v1/extensions/chat-bridge/-/icon?v=1.2.0"}]"#

    /// The icon is asked of the extension's own manager, on its route; the name comes only once its picture is drawn,
    /// decoded at about the size asked.
    @Test func drawsAndNamesTheIconOnlyOnceItIsIn() async throws {
        let h = try hub(Self.bridgeJSON, picture: Self.png())
        let opener = ConversationOpener(id: "chat-bridge", manager: "work")
        #expect(h.icons.name(for: opener, source: .larkAgent) == nil)
        h.icons.ensure()
        try await until("the list") { h.icons.list != nil }
        let icon = try #require(h.icons.icon(for: opener, brand: Theme.Brand.lark))
        #expect(h.icons.name(for: opener, source: .larkAgent) == nil, "not drawn yet: the origin's words")
        h.icons.load(icon, pixels: 42)
        h.icons.load(icon, pixels: 42)
        try await until("the picture") { h.icons.drawn(icon).image != nil }
        #expect(h.icons.name(for: opener, source: .larkAgent) == "Chat Bridge")
        #expect(h.icons.name(for: nil, source: .larkAgent) == nil)
        #expect(h.icons.name(for: opener, source: nil) == nil)
        #expect((h.icons.drawn(icon).image?.width ?? 0) <= 42)
        let asked = h.seen.requests.filter { $0.path.hasSuffix("/icon") }
        #expect(asked.count == 1, "one read for marks that ask at once")
        #expect(asked.first?.path == "/api/v1/extensions/chat-bridge/-/icon")
        #expect(asked.first?.query == ["v": "1.2.0"])
        #expect(asked.first?.headers[APIClient.managerHeader] == "work")
        let list = try #require(h.seen.requests.first { $0.path == "/api/v1/extensions" })
        #expect(list.headers[APIClient.managerHeader] == nil, "every manager's list")
    }

    /// An icon that does not load, or is not a PNG, keeps the origin's mark and words, and is not asked again until the
    /// list is read again.
    @Test func aFailedIconKeepsTheOriginsMarkAndWords() async throws {
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        let h = try hub(Self.bridgeJSON, picture: Data("<html>not a picture</html>".utf8), now: { clock })
        let opener = ConversationOpener(id: "chat-bridge", manager: "work")
        h.icons.ensure()
        try await until("the list") { h.icons.list != nil }
        let icon = try #require(h.icons.icon(for: opener, brand: Theme.Brand.lark))
        h.icons.load(icon, pixels: 42)
        try await until("the failed read") { !h.seen.requests.filter { $0.path.hasSuffix("/icon") }.isEmpty }
        try await Task.sleep(for: .milliseconds(100))
        #expect(h.icons.drawn(icon).image == nil)
        #expect(h.icons.name(for: opener, source: .larkAgent) == nil)
        h.icons.load(icon, pixels: 42)
        try await Task.sleep(for: .milliseconds(100))
        #expect(h.seen.requests.filter { $0.path.hasSuffix("/icon") }.count == 1, "not asked again yet")
        clock += OpenerIcons.fresh
        h.icons.ensure()
        try await until("the list again") { h.seen.requests.filter { $0.path == "/api/v1/extensions" }.count == 2 }
        try await Task.sleep(for: .milliseconds(50))
        h.icons.load(icon, pixels: 42)
        try await until("asked again") { h.seen.requests.filter { $0.path.hasSuffix("/icon") }.count == 2 }
    }

    /// The Lark extension's own /agent: Lark's mark and words, its icon never asked for. Another manager's copy of an
    /// extension, or none installed, the same.
    @Test func noIconIsAskedForWhatKeepsTheOriginsMark() async throws {
        let json = #"[{"id": "larkext", "name": "Lark", "icon_url": "/api/v1/extensions/larkext/-/icon?v=3.0.0", "contributes": {"messaging": ["lark"]}},"#
            + #"{"id": "chat-bridge", "name": "Chat Bridge", "manager": "home", "icon_url": "/api/v1/extensions/chat-bridge/-/icon?v=1.2.0"}]"#
        let roster = ManagerRoster(managers: [HubManager(id: "work", name: "Work", online: true, isDefault: true),
                                              HubManager(id: "home", name: "Home", online: true)])
        let h = try hub(json, picture: Self.png(), roster: roster)
        h.icons.ensure()
        try await until("the list") { h.icons.list != nil }
        #expect(h.icons.icon(for: ConversationOpener(id: "larkext"), brand: Theme.Brand.lark) == nil)
        #expect(h.icons.icon(for: ConversationOpener(id: "chat-bridge", manager: "work"), brand: Theme.Brand.lark) == nil)
        #expect(h.icons.icon(for: ConversationOpener(id: "chat-bridge", manager: "home"), brand: Theme.Brand.lark) != nil)
        #expect(h.icons.icon(for: ConversationOpener(id: "gone"), brand: nil) == nil)
        #expect(h.icons.name(for: ConversationOpener(id: "larkext"), source: .larkAgent) == nil)
    }

    /// A hub without extensions is not asked; the list is read once, again a minute later, and dropped for another hub.
    @Test func readsTheListOnlyWhenItCan() async throws {
        let none = try hub(Self.bridgeJSON, picture: nil, features: [])
        none.icons.ensure()
        try await Task.sleep(for: .milliseconds(50))
        #expect(none.seen.requests.isEmpty)
        #expect(!none.icons.hubListsExtensions)

        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        let h = try hub(Self.bridgeJSON, picture: nil, features: ["plugins"], now: { clock })
        #expect(h.icons.hubListsExtensions)
        h.icons.ensure()
        h.icons.ensure()
        try await until("the list") { h.icons.list != nil }
        h.icons.ensure()
        try await Task.sleep(for: .milliseconds(50))
        #expect(h.seen.requests.count == 1, "read once")
        clock += OpenerIcons.fresh - 1
        h.icons.ensure()
        try await Task.sleep(for: .milliseconds(50))
        #expect(h.seen.requests.count == 1, "still fresh")
        h.icons.stale()
        h.icons.ensure()
        try await until("read again after Settings read its list") { h.seen.requests.count == 2 }
    }

    @Test func iconRequestsAndPictures() throws {
        let r = OpenerIcon(route: "/api/v1/extensions/chat-bridge/-/icon?v=1.0.0%2Bbuild.7", manager: nil, name: nil).request
        #expect(r.path == "/api/v1/extensions/chat-bridge/-/icon")
        #expect(r.query == ["v": "1.0.0+build.7"])
        #expect(OpenerIcon.picture(Data("GIF89a".utf8), pixels: 40) == nil)
        #expect(OpenerIcon.picture(Data(), pixels: 40) == nil)
        let big = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]) + Data(count: OpenerIcon.maxBytes)
        #expect(OpenerIcon.picture(big, pixels: 40) == nil)
        let image = try #require(OpenerIcon.picture(Self.png(side: 256), pixels: 40)?.image)
        #expect(max(image.width, image.height) <= 40)
    }
}
