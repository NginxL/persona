import Foundation
import Testing
@testable import Persona

/// The hub's own links opening their page in the app (#2696, HubPageLink): which links are the hub's, and which page.
struct HubPageLinkTests {
    static let hub = URL(string: "http://hub.example.com:18660")!
    static let hubs = [hub, URL(string: "https://[2001:db8::1]:18443")!]

    func page(_ s: String, hubs: [URL] = Self.hubs) -> HubPage? { HubPageLink.page(URL(string: s)!, hubs: hubs) }

    @Test func agentSessions() {
        #expect(page("http://hub.example.com:18660/s/box1/06f51a1a-88cf-41e6-8c84-13de3620fd4e")
            == .agent(machine: "box1", session: "06f51a1a-88cf-41e6-8c84-13de3620fd4e", item: nil))
        // agent_read's link to one message (#2531): the item id is query-escaped (Claude ids hold '#').
        #expect(page("http://hub.example.com:18660/s/box1/abc?at=msg_1%23tool")
            == .agent(machine: "box1", session: "abc", item: "msg_1#tool"))
        #expect(page("http://hub.example.com:18660/s/box1/abc?view=term&at=x") == .agent(machine: "box1", session: "abc", item: "x"))
        #expect(page("http://hub.example.com:18660/s/box1/abc/") == .agent(machine: "box1", session: "abc", item: nil))
        #expect(page("http://hub.example.com:18660/s/box1/abc?at=") == .agent(machine: "box1", session: "abc", item: nil))
        // A fragment is the web page's own, not a message.
        #expect(page("http://hub.example.com:18660/s/box1/abc#bottom") == .agent(machine: "box1", session: "abc", item: nil))
        // Escaped path segments come back as written.
        #expect(page("http://hub.example.com:18660/s/my%20box/abc") == .agent(machine: "my box", session: "abc", item: nil))
        // A herdr tab with its session; without one it is a place, not a chat.
        #expect(page("http://hub.example.com:18660/t/box1/w1/t2/abc") == .agent(machine: "box1", session: "abc", item: nil))
        #expect(page("http://hub.example.com:18660/t/box1/w1/t2") == nil)
        #expect(page("http://hub.example.com:18660/s/box1") == nil)
        #expect(page("http://hub.example.com:18660/s/box1/abc/more") == nil)
    }

    @Test func conversations() {
        #expect(page("http://hub.example.com:18660/persona/c-123") == .conversation(id: "c-123", item: nil))
        #expect(page("http://hub.example.com:18660/persona/c-123?at=u1") == .conversation(id: "c-123", item: "u1"))
        #expect(page("http://hub.example.com:18660/persona/new") == .newConversation)
        #expect(page("http://hub.example.com:18660/persona") == .persona)
        #expect(page("http://hub.example.com:18660/" + LegacyNames.conversationPath + "/c-123") == .conversation(id: "c-123", item: nil))
    }

    @Test func todos() {
        #expect(page("http://hub.example.com:18660/todos?id=todo_abc") == .todo(id: "todo_abc"))
        #expect(page("http://hub.example.com:18660/todos/todo_abc") == .todo(id: "todo_abc"))
        #expect(page("http://hub.example.com:18660/todos") == .todos) // the list (#9926)
        #expect(page("http://hub.example.com:18660/todos?id=") == .todos)
    }

    @Test func unknownPathsStayWebPages() {
        for path in ["/admin", "/api/v1/sessions", "/S/box1/abc", "/share/abc", "/overview", "/inbox/x/y", "/kb/spells/x"] {
            #expect(page("http://hub.example.com:18660" + path) == nil, "\(path)")
        }
    }

    @Test func onlyThisHubsAddresses() {
        #expect(page("http://other.example.com:18660/s/box1/abc") == nil)
        #expect(page("http://hub.example.com:18661/s/box1/abc") == nil) // another port, another service
        #expect(page("http://hub.example.com/s/box1/abc") == nil) // port 80 is not 18660
        #expect(page("http://evil.example.com/?u=http://hub.example.com:18660/s/box1/abc") == nil)
        #expect(page("http://hub.example.com.evil.example:18660/s/box1/abc") == nil)
        #expect(page("persona://hub.example.com:18660/s/box1/abc") == nil)
        #expect(page("http://hub.example.com:18660/s/box1/abc", hubs: []) == nil)
        // Host case, IPv6 brackets, the scheme of the hub's TLS port.
        #expect(page("http://HUB.Example.com:18660/s/box1/abc") != nil)
        #expect(page("https://[2001:DB8::1]:18443/todos?id=t") == .todo(id: "t"))
        #expect(page("http://hub.example.com:18660/s/box1/abc", hubs: [URL(string: "https://hub.example.com:18660")!]) != nil)
        // Default ports written or not.
        #expect(page("https://hub.example.com/persona/c", hubs: [URL(string: "https://hub.example.com:443")!]) == .conversation(id: "c", item: nil))
    }

    @Test func hubUnderASubPath() {
        let hubs = [URL(string: "https://example.com/persona")!]
        #expect(page("https://example.com/persona/s/box1/abc", hubs: hubs) == .agent(machine: "box1", session: "abc", item: nil))
        #expect(page("https://example.com/persona/todos?id=t", hubs: hubs) == .todo(id: "t"))
        #expect(page("https://example.com/s/box1/abc", hubs: hubs) == nil) // outside the hub's prefix
    }

    @Test func helloCarriesPublicURL() {
        guard case .hello(let info)? = ServerEvent.decode(Data(#"{"type":"hello","features":[],"public_url":"http://hub.example.com:18660"}"#.utf8)) else {
            Issue.record("hello"); return
        }
        #expect(info.publicURL == Self.hub)
        guard case .hello(let old)? = ServerEvent.decode(Data(#"{"type":"hello","features":[]}"#.utf8)) else {
            Issue.record("old hello"); return
        }
        #expect(old.publicURL == nil)
    }

    // MARK: The route table every client follows (docs/protocol/deep-links.md, #9926)

    @Test func routeTablePages() {
        #expect(page("http://hub.example.com:18660/") == .persona)
        #expect(page("http://hub.example.com:18660/inbox/i-1?manager=m-2") == .inboxItem(id: "i-1", manager: "m-2"))
        #expect(page("http://hub.example.com:18660/machines/box1") == .machine(id: "box1"))
        #expect(page("http://hub.example.com:18660/settings?tab=channels&channel=c1") == .settings(tab: "channels", item: "c1"))
        #expect(page("http://hub.example.com:18660/settings?tab=sent") == .settings(tab: "connectors", item: HubPageLink.sentSettings))
        #expect(page("http://hub.example.com:18660/settings?tab=hub&extension=x") == .settings(tab: "hub", item: nil))
        #expect(page("http://hub.example.com:18660/kb/docs/a%2Fb") == .knowledgeEntry(kind: "docs", id: "a/b"))
        #expect(page("http://hub.example.com:18660/extension/w/today?state=s") == .extensionPage(extension: "w", ui: "today", state: "s"))
        #expect(page("http://user:pw@hub.example.com:18660/s/box1/abc") == nil) // a user name and password: never the hub
    }

    @Test func appLinks() {
        func app(_ s: String) -> HubPage? { HubPageLink.appPage(URL(string: s)!)?.page }
        #expect(app("persona://open/s/box1/abc?at=m1") == .agent(machine: "box1", session: "abc", item: "m1"))
        #expect(HubPageLink.appPage(URL(string: "persona://open/todos/t1?hub=h1")!)?.hub == "h1")
        #expect(app("persona://open?route=%2Fpersona%2Fc1") == .conversation(id: "c1", item: nil))
        #expect(app("persona://open?route=%2F%2Fevil.example%2Fx") == nil)
        #expect(app("persona://open?route=https%3A%2F%2Fevil.example%2F") == nil)
        #expect(app("persona://open") == nil) // only brings the app forward
        #expect(app("persona://open//evil.example/x") == nil)
        #expect(app("persona://open:8080/s/box1/abc") == nil)
        #expect(app("persona://task/t1") == .todo(id: "t1"))
        #expect(app("persona://kb/docs/a/b") == .knowledgeEntry(kind: "docs", id: "a/b"))
        #expect(app("persona://pair?url=x&token=y") == nil)
        #expect(app("https://hub.example.com:18660/s/box1/abc") == nil) // a web link is `page(_:hubs:)`'s
    }

    /// The cases every client checks (spec/fixtures/deep-links/cases.json): the same link opens the same page as on the
    /// web and Android. The iPhone keeps no view state (`view`, `range`, `mode`, `date`) and finds a conversation by its
    /// id in every list, so a conversation's `machine` and `manager` are not compared.
    @Test func sharedCases() throws {
        let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let data = try Data(contentsOf: repo.appending(path: "spec/fixtures/deep-links/cases.json"))
        let fixture = try JSONDecoder().decode(DeepLinkFixture.self, from: data)
        #expect(fixture.version == 1)
        #expect(fixture.parse.count >= 60)
        for c in fixture.parse {
            guard let url = URL(string: c.url) else { Issue.record("\(c.name): not a URL"); continue }
            #expect(Self.parsed(url, hubs: c.hubs.compactMap { fixture.hubs[$0] }) == c.page.map(Self.kept), "\(c.name)")
            #expect((c.page == nil) == (c.reason != nil), "\(c.name): a case without a page says why")
        }
    }

    private struct DeepLinkFixture: Decodable {
        struct Hub: Decodable { var id: String; var addresses: [URL] }
        struct Case: Decodable { var name: String; var url: String; var hubs: [String]; var page: [String: String]?; var reason: String? }
        var version: Int
        var hubs: [String: Hub]
        var parse: [Case]
    }

    /// What the app opens for a link with these paired hubs, as the fixture writes a page: an app link first, else the
    /// first hub (in order) whose address it is.
    private static func parsed(_ url: URL, hubs: [DeepLinkFixture.Hub]) -> [String: String]? {
        if let (page, hub) = HubPageLink.appPage(url) { return fields(page).merging(hub.map { ["hub": $0] } ?? [:]) { a, _ in a } }
        for h in hubs {
            if let page = HubPageLink.page(url, hubs: h.addresses) { return fields(page).merging(["hub": h.id]) { a, _ in a } }
        }
        return nil
    }

    private static func kept(_ page: [String: String]) -> [String: String] {
        var p = page.filter { !["view", "range", "mode", "date"].contains($0.key) }
        if p["page"] == "conversation" { p["machine"] = nil; p["manager"] = nil }
        return p
    }

    private static func fields(_ page: HubPage) -> [String: String] {
        func with(_ name: String, _ values: [String: String?]) -> [String: String] {
            values.compactMapValues { $0 }.merging(["page": name]) { a, _ in a }
        }
        switch page {
        case .agent(let m, let s, let at): return with("session", ["machine": m, "session": s, "at": at])
        case .conversation(let id, let at): return with("conversation", ["id": id, "at": at])
        case .todo(let id): return with("todo", ["id": id])
        case .persona: return with("persona", [:])
        case .newConversation: return with("persona-new", [:])
        case .agents: return with("agents", [:])
        case .inbox: return with("inbox", [:])
        case .inboxItem(let id, let manager): return with("inbox-item", ["id": id, "manager": manager])
        case .todos: return with("todos", [:])
        case .knowledge(let kind): return with("knowledge", ["kind": kind])
        case .knowledgeEntry(let kind, let id): return with("knowledge-entry", ["kind": kind, "id": id])
        case .machines: return with("machines", [:])
        case .machine(let id): return with("machine", ["id": id])
        case .settings(let tab, let item): return with("settings", ["tab": tab, "item": item])
        case .extensionPage(let ext, let ui, let state): return with("extension", ["extension": ext, "ui": ui, "state": state])
        }
    }
}
