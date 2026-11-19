import Foundation
import Observation
import PersonaWire

// Approving a browser's sign-in from this iPhone (#10293; docs/protocol/protocol.md "Browser QR sign-in"): the web
// sign-in page shows a code, Settings › Devices › Scan Browser Sign-in Code reads it, this page says which browser asks,
// from which network and for what, and Allow makes the hub hand out a two-digit number that the user types in the
// browser (one try; a wrong number voids the request). Decline, the ✕ and Cancel Sign-in all decline.

extension APIClient {
    /// The hub matches a number (`browser_qr_match`): only then can this device approve; an Allow without
    /// `show_number` is refused there with `update_required`.
    static let browserQRMatchFeature = "browser_qr_match"

    /// A browser's sign-in request as the approving device sees it.
    nonisolated struct BrowserLoginRequest: Decodable, Equatable, Sendable {
        var name: String
        /// pending, scanned, confirm (allowed; waiting for the number), approved, denied, voided.
        var state: String
        /// lan, internet or unknown (forwarded: the real source can't be seen); nil from a hub before it.
        var network: String?
        var ageSeconds: Int?
        /// The number to show; only for the device that allowed it.
        var number: String?
        enum CodingKeys: String, CodingKey { case name, state, network, number, ageSeconds = "age_seconds" }
    }

    /// Looks at a request (`allow` nil, which marks it scanned) or decides it. Allow asks for the number to show.
    func browserLogin(id: String, hub: String, allow: Bool? = nil) async throws -> BrowserLoginRequest {
        struct Body: Encodable {
            var id: String
            var hub: String
            var allow: Bool?
            var showNumber: Bool?
            enum CodingKeys: String, CodingKey { case id, hub, allow, showNumber = "show_number" }
        }
        let body = Body(id: id, hub: hub, allow: allow, showNumber: allow == true ? true : nil)
        return try JSON.decoder.decode(BrowserLoginRequest.self,
                                       from: try await request("POST", "/api/v1/auth/browser-login/approve", body: body))
    }
}

/// The web sign-in page's code: `persona://browser-login?hub=<hub id>&request=<id>`. Only the app's own scanner reads
/// it; a link that reaches the app any other way approves nothing (`matches`, docs/protocol/deep-links.md).
nonisolated struct BrowserLoginLink: Equatable, Sendable {
    let id: String
    let hub: String

    init?(_ text: String) {
        guard let c = URLComponents(string: text.trimmingCharacters(in: .whitespacesAndNewlines)), Self.isOurs(c),
              c.user == nil, c.password == nil, c.port == nil, c.path.isEmpty, c.fragment == nil,
              let items = c.queryItems, items.count == 2, Set(items.map(\.name)) == ["hub", "request"],
              let id = items.first(where: { $0.name == "request" })?.value,
              let hub = items.first(where: { $0.name == "hub" })?.value,
              id.count == 43, id.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == "_") }),
              !hub.isEmpty, hub.count <= 80 else { return nil }
        self.id = id
        self.hub = hub
    }

    /// Whether a link that opened the app is this code (the Camera app read it): the app then says where to scan it.
    static func matches(_ url: URL) -> Bool {
        URLComponents(url: url, resolvingAgainstBaseURL: false).map(isOurs) ?? false
    }

    private static func isOurs(_ c: URLComponents) -> Bool {
        c.scheme?.lowercased() == "persona" && c.host?.lowercased() == "browser-login"
    }
}

/// The approval page's steps, kept apart from the view to test.
@MainActor @Observable final class BrowserLoginApprovalModel {
    struct Deps {
        var api: () -> APIClient?
        var now: () -> Date = { .now }
        /// How often the number page asks the hub whether the browser typed it.
        var pollInterval: Duration = .milliseconds(1500)
        static var live: Deps { .init(api: { AppModel.shared.api }) }
    }

    enum Network: Equatable { case lan, internet, unknown }

    /// What the page shows.
    enum Phase: Equatable {
        case loading
        /// The request, waiting for Allow or Decline; `allowing` while Allow is on its way.
        case asking(Request, allowing: Bool)
        /// Allowed: the number the browser must type.
        case number(String)
        case done(Outcome)
    }

    enum Outcome: Equatable {
        case signedIn(String)
        /// The browser typed another number: the request is void.
        case voided
        /// Declined (by another device, or this one before the number was typed).
        case declined
        case expired
        case failed(String)
    }

    /// The facts the page lists.
    struct Request: Equatable {
        var name: String
        var network: Network
        var requestedAt: Date
    }

    let link: BrowserLoginLink
    private(set) var phase: Phase = .loading
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var name = ""

    init(_ link: BrowserLoginLink, deps: Deps = .live) {
        self.link = link
        self.deps = deps
    }

    #if DEBUG
    /// A page with sample data and no hub (`-showBrowserLoginSample`, screenshots).
    init(sample phase: Phase) {
        link = BrowserLoginLink("persona://browser-login?hub=sample&request=" + String(repeating: "x", count: 43))!
        deps = .init(api: { nil })
        self.phase = phase
    }
    #endif

    static func network(_ word: String?) -> Network {
        switch word {
        case "lan": .lan
        case "internet": .internet
        default: .unknown
        }
    }

    /// Reads the request (the hub marks it scanned, and the browser says so).
    func load() async {
        guard case .loading = phase else { return }
        do {
            let r = try await call(allow: nil)
            name = r.name
            switch r.state {
            case "pending", "scanned":
                phase = .asking(Request(name: r.name, network: Self.network(r.network),
                                        requestedAt: deps.now().addingTimeInterval(-Double(r.ageSeconds ?? 0))), allowing: false)
            case "confirm":
                // Allowed already: from this device (its page was closed meanwhile) the number comes again.
                phase = r.number.map(Phase.number) ?? .done(.failed(String(localized: "This sign-in has already been answered")))
            default:
                phase = outcome(r.state).map(Phase.done) ?? .done(.failed(String(localized: "This sign-in has already been answered")))
            }
        } catch {
            phase = .done(failure(error))
        }
    }

    /// Allow: the hub answers the number to show.
    func allow() async {
        guard case .asking(let request, false) = phase else { return }
        phase = .asking(request, allowing: true)
        do {
            let r = try await call(allow: true)
            if let n = r.number, r.state == "confirm" {
                phase = .number(n)
            } else {
                phase = .done(outcome(r.state) ?? .failed(String(localized: "This sign-in has already been answered")))
            }
        } catch {
            phase = .done(failure(error))
        }
    }

    /// While the number shows: asks the hub until the browser has typed it (or the request ends). Stops with the page.
    func watch() async {
        while case .number = phase, !Task.isCancelled {
            try? await Task.sleep(for: deps.pollInterval)
            guard !Task.isCancelled, case .number = phase else { return }
            do {
                let r = try await call(allow: nil)
                if let o = outcome(r.state) { phase = .done(o) }
            } catch let e as APIError where e.status == 410 {
                phase = .done(.expired)
            } catch {
                continue // a lost reply: ask again
            }
        }
    }

    /// Decline, the ✕ and Cancel Sign-in: the page closes at once (the caller); the hub hears it after.
    func decline() {
        switch phase {
        case .asking, .number, .loading: break
        case .done: return
        }
        phase = .done(.declined)
        guard let api = deps.api() else { return }
        let link = link
        Task { _ = try? await api.browserLogin(id: link.id, hub: link.hub, allow: false) }
    }

    private func call(allow: Bool?) async throws -> APIClient.BrowserLoginRequest {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
        return try await api.browserLogin(id: link.id, hub: link.hub, allow: allow)
    }

    /// A request's end, nil while it goes on.
    private func outcome(_ state: String) -> Outcome? {
        switch state {
        case "approved": .signedIn(name)
        case "voided": .voided
        case "denied": .declined
        default: nil
        }
    }

    private func failure(_ error: Error) -> Outcome {
        guard let e = error as? APIError else { return .failed(error.localizedDescription) }
        if e.status == 410 { return .expired }
        return .failed(e.localizedDescription)
    }
}

#if DEBUG
/// `-showBrowserLoginSample <step>`: the approval sheet at that step with sample data (screenshots).
enum BrowserLoginSample {
    /// The step `-showBrowserLoginSample` asks for, read once; nil without it (or for `none`: Devices only).
    static let current = UserDefaults.standard.string(forKey: "showBrowserLoginSample").flatMap { phase($0) }

    static func phase(_ step: String) -> BrowserLoginApprovalModel.Phase? {
        let name = "Chrome · Mac"
        func request(_ n: BrowserLoginApprovalModel.Network) -> BrowserLoginApprovalModel.Request {
            .init(name: name, network: n, requestedAt: .now.addingTimeInterval(-12))
        }
        switch step {
        case "lan", "YES": return .asking(request(.lan), allowing: false)
        case "internet": return .asking(request(.internet), allowing: false)
        case "unknown": return .asking(request(.unknown), allowing: false)
        case "number": return .number("47")
        case "signedIn": return .done(.signedIn(name))
        case "voided": return .done(.voided)
        case "expired": return .done(.expired)
        default: return nil
        }
    }
}
#endif
