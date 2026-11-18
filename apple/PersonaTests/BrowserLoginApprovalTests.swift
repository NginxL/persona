import Foundation
import Testing
@testable import Persona

/// Approving a browser's sign-in from the iPhone (#10293, docs/protocol/protocol.md "Browser QR sign-in"): which codes
/// are read, what Allow sends, the number and how the page ends, and that leaving declines.
@MainActor
struct BrowserLoginApprovalTests {
    static let path = "/api/v1/auth/browser-login/approve"
    static let id = String(repeating: "a", count: 40) + "b-_"
    static let code = "persona://browser-login?hub=hub-1&request=\(id)"

    private func body(_ r: LocalRequest) -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: r.body ?? Data())) as? [String: Any] ?? [:]
    }

    private func rig(_ answer: @escaping FakeHub.Route) -> (BrowserLoginApprovalModel, FakeHub) {
        let hub = FakeHub()
        hub.on("POST", Self.path, answer)
        let api = hub.client
        let now = Date(timeIntervalSince1970: 1_000_000)
        let page = BrowserLoginApprovalModel(BrowserLoginLink(Self.code)!,
                                             deps: .init(api: { api }, now: { now }, pollInterval: .milliseconds(5)))
        return (page, hub)
    }

    private func waitUntil(_ what: () -> Bool) async {
        let deadline = Date.now + 5
        while !what(), Date.now < deadline { try? await Task.sleep(for: .milliseconds(5)) }
    }

    @Test func readsOnlyTheSignInPagesCode() {
        let link = BrowserLoginLink(Self.code)
        #expect(link?.id == Self.id && link?.hub == "hub-1")
        for bad in [
            "persona://pair?url=https://hub.example&token=jp_x", // another Persona link
            "https://example.com/?hub=hub-1&request=\(Self.id)",
            "persona://browser-login?hub=hub-1&request=short",
            "persona://browser-login?hub=hub-1&request=\(Self.id)&extra=1",
            "persona://browser-login?request=\(Self.id)",
            "persona://browser-login/path?hub=hub-1&request=\(Self.id)",
            "persona://user@browser-login?hub=hub-1&request=\(Self.id)",
        ] {
            #expect(BrowserLoginLink(bad) == nil, "\(bad)")
        }
        #expect(BrowserLoginLink.matches(URL(string: Self.code)!))
        #expect(!BrowserLoginLink.matches(URL(string: "persona://pair?url=x&token=y")!))
    }

    @Test func showsTheRequestThenTheNumberAfterAllow() async {
        let (page, hub) = rig { req in
            let b = (try? JSONSerialization.jsonObject(with: req.body ?? Data())) as? [String: Any] ?? [:]
            if b["allow"] as? Bool == true {
                return Data(#"{"name":"Chrome · Mac","state":"confirm","network":"lan","age_seconds":20,"number":"47"}"#.utf8)
            }
            return Data(#"{"name":"Chrome · Mac","state":"scanned","network":"internet","age_seconds":12}"#.utf8)
        }
        await page.load()
        guard case .asking(let r, allowing: false) = page.phase else { Issue.record("\(page.phase)"); return }
        #expect(r.name == "Chrome · Mac" && r.network == .internet)
        #expect(r.requestedAt == Date(timeIntervalSince1970: 1_000_000 - 12))
        #expect(body(hub.sent("POST", Self.path)[0]).keys.sorted() == ["hub", "id"], "looking allows nothing")
        await page.allow()
        #expect(page.phase == .number("47"))
        let allow = body(hub.sent("POST", Self.path)[1])
        #expect(allow["allow"] as? Bool == true && allow["show_number"] as? Bool == true, "Allow asks for the number")
        #expect(allow["id"] as? String == Self.id && allow["hub"] as? String == "hub-1")
    }

    /// The fake hub's memory between requests.
    final class Box {
        var allowed = false
        var looks = 0
    }

    /// Answers Allow with the number, and looks with `scanned` before it and `after(n)` for the n-th look after.
    private func hubThatAllows(_ box: Box, after: @escaping (Int) -> String) -> FakeHub.Route {
        { req in
            let b = (try? JSONSerialization.jsonObject(with: req.body ?? Data())) as? [String: Any] ?? [:]
            if b["allow"] as? Bool == true {
                box.allowed = true
                return Data(#"{"name":"Chrome · Mac","state":"confirm","number":"47"}"#.utf8)
            }
            guard box.allowed else { return Data(#"{"name":"Chrome · Mac","state":"scanned"}"#.utf8) }
            box.looks += 1
            return Data(#"{"name":"Chrome · Mac","state":"\#(after(box.looks))"}"#.utf8)
        }
    }

    @Test func endsWhenTheBrowserTypesTheNumber() async {
        let box = Box()
        let (page, _) = rig(hubThatAllows(box) { $0 < 3 ? "confirm" : "approved" })
        await page.load()
        await page.allow()
        #expect(page.phase == .number("47"))
        await page.watch()
        #expect(page.phase == .done(.signedIn("Chrome · Mac")))
        #expect(box.looks == 3, "asks until the browser has typed it")
    }

    @Test func aWrongNumberInTheBrowserVoidsIt() async {
        let (page, _) = rig(hubThatAllows(Box()) { _ in "voided" })
        await page.load()
        await page.allow()
        await page.watch()
        #expect(page.phase == .done(.voided))
    }

    @Test func comingBackToAnAllowedRequestShowsItsNumberAgain() async {
        let (page, _) = rig { _ in Data(#"{"name":"B","state":"confirm","number":"47"}"#.utf8) }
        await page.load()
        #expect(page.phase == .number("47"))
    }

    @Test func expiredAndOtherHubsCodesEnd() async {
        let (expired, _) = rig { _ in throw APIError(status: 410, message: "This sign-in has expired") }
        await expired.load()
        #expect(expired.phase == .done(.expired))
        let (other, _) = rig { _ in throw APIError(status: 400, message: "This code belongs to another Persona") }
        await other.load()
        #expect(other.phase == .done(.failed("This code belongs to another Persona")))
    }

    @Test func leavingDeclinesAtOnceAndTellsTheHub() async {
        let (page, hub) = rig { _ in Data(#"{"name":"B","state":"scanned","network":"lan"}"#.utf8) }
        await page.load()
        page.decline()
        #expect(page.phase == .done(.declined), "the page ends before the hub answers")
        await waitUntil { hub.sent("POST", Self.path).count == 2 }
        #expect(body(hub.sent("POST", Self.path)[1])["allow"] as? Bool == false)
        page.decline()
        try? await Task.sleep(for: .milliseconds(30))
        #expect(hub.sent("POST", Self.path).count == 2, "an ended page sends nothing more")
    }

    @Test func networkWords() {
        #expect(BrowserLoginApprovalModel.network("lan") == .lan)
        #expect(BrowserLoginApprovalModel.network("internet") == .internet)
        #expect(BrowserLoginApprovalModel.network("unknown") == .unknown)
        #expect(BrowserLoginApprovalModel.network(nil) == .unknown, "a hub that says nothing can't tell either")
    }
}
