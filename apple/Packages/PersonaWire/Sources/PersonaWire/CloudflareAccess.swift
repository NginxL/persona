import Foundation
import os

/// A Cloudflare Access service token for one paired hub (docs/operations/network.md "Behind Cloudflare Access"): the hub
/// is published through Cloudflare Tunnel with Access in front, which lets the apps through only with these two values as
/// headers. Kept with the pairing in the keychain, like the hub token; never printed (its descriptions redact the secret).
public nonisolated struct HubAccess: Codable, Hashable, Sendable {
    public var clientID: String
    public var clientSecret: String

    public enum CodingKeys: String, CodingKey { case clientID = "client_id", clientSecret = "client_secret" }

    /// The two values as typed, trimmed; nil when either is empty (no token).
    public init?(clientID: String, clientSecret: String) {
        let id = clientID.trimmingCharacters(in: .whitespacesAndNewlines)
        let secret = clientSecret.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !id.isEmpty, !secret.isEmpty else { return nil }
        self.clientID = id
        self.clientSecret = secret
    }
}

nonisolated extension HubAccess: CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public var description: String { "HubAccess(clientID: \(clientID), clientSecret: <redacted>)" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: ["clientID": clientID, "clientSecret": "<redacted>"]) }
}

/// Where the service token goes and what Access turning a request away looks like.
public nonisolated enum CloudflareAccess {
    public static let idHeader = "CF-Access-Client-Id"
    public static let secretHeader = "CF-Access-Client-Secret"

    /// A URL's origin: scheme (a WebSocket's as http / https), host and port.
    public static func origin(_ url: URL?) -> String? {
        guard let url, let scheme = url.scheme?.lowercased(),
              let host = url.host(percentEncoded: false)?.lowercased(), !host.isEmpty else { return nil }
        let s = scheme == "wss" ? "https" : scheme == "ws" ? "http" : scheme
        let port = url.port ?? (s == "https" ? 443 : s == "http" ? 80 : 0)
        return "\(s)://\(host):\(port)"
    }

    public static func sameOrigin(_ a: URL?, _ b: URL?) -> Bool {
        guard let a = origin(a) else { return false }
        return a == origin(b)
    }

    /// The token's headers for a request to `url`: only on `hub`'s own origin, never another hub, a mesh peer, a
    /// tunnel's loopback address or a third party.
    public static func headers(_ access: HubAccess?, for url: URL?, hub: URL) -> [String: String] {
        guard let access, sameOrigin(url, hub) else { return [:] }
        return [idHeader: access.clientID, secretHeader: access.clientSecret]
    }

    public static func apply(_ access: HubAccess?, to request: inout URLRequest, hub: URL) {
        for (name, value) in headers(access, for: request.url, hub: hub) { request.setValue(value, forHTTPHeaderField: name) }
    }

    /// Access's login (`<team>.cloudflareaccess.com`).
    public static func isLogin(_ url: URL?) -> Bool {
        guard let host = url?.host(percentEncoded: false)?.lowercased() else { return false }
        return host == "cloudflareaccess.com" || host.hasSuffix(".cloudflareaccess.com")
    }

    /// The redirect to follow, nil to stop at the redirect's own answer: never to Access's login page, and a request
    /// carrying the token never leaves its origin (the token would go along).
    public static func follow(_ next: URLRequest, from original: URLRequest?) -> URLRequest? {
        if isLogin(next.url) { return nil }
        if original?.value(forHTTPHeaderField: secretHeader) != nil, !sameOrigin(next.url, original?.url) { return nil }
        return next
    }

    /// Access turned the request away, not the hub: a redirect to its login, its login page, or its own HTML answer
    /// (the hub answers JSON, never a page naming Access).
    public static func blocked(_ response: URLResponse?, body: Data = Data()) -> Bool {
        guard let http = response as? HTTPURLResponse else { return false }
        if isLogin(http.url) { return true }
        if (300..<400).contains(http.statusCode), let location = http.value(forHTTPHeaderField: "Location"),
           isLogin(URL(string: location, relativeTo: http.url)) { return true }
        guard (http.value(forHTTPHeaderField: "Content-Type") ?? "").lowercased().contains("html") else { return false }
        // A WebSocket handshake has no body to read: an HTML 401 / 403 from Cloudflare's edge (the hub's own are JSON; a
        // reverse proxy or basic-auth gateway in front of a hub without Access is not Cloudflare).
        if body.isEmpty {
            let edge = http.value(forHTTPHeaderField: "CF-RAY") != nil
                || (http.value(forHTTPHeaderField: "Server") ?? "").lowercased().contains("cloudflare")
            return edge && [401, 403].contains(http.statusCode)
        }
        let text = String(decoding: body.prefix(64 << 10), as: UTF8.self).lowercased()
        return text.contains("cloudflareaccess.com") || text.contains("/cdn-cgi/access/")
    }
}

/// Cloudflare Access stopped a request to the hub: no service token set for it, or the one set was refused.
public nonisolated struct CloudflareAccessError: LocalizedError, Equatable, Sendable {
    /// A token went with the request.
    public var tokenSent: Bool

    public init(tokenSent: Bool) { self.tokenSent = tokenSent }

    public var errorDescription: String? {
        tokenSent ? String(localized: "Cloudflare Access rejected the service token")
                  : String(localized: "This hub is behind Cloudflare Access and needs a service token")
    }

    /// Throws when `response` is Access turning the request away.
    public static func check(_ response: URLResponse?, body: Data = Data(), access: HubAccess?) throws {
        if CloudflareAccess.blocked(response, body: body) { throw CloudflareAccessError(tokenSent: access != nil) }
    }
}

/// The task delegate of requests to a hub: redirects as `CloudflareAccess.follow` allows. A subclass adds its own
/// callbacks (upload and download progress).
open nonisolated class HubRedirects: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    public static let shared = HubRedirects()

    public override init() { super.init() }

    open func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(CloudflareAccess.follow(request, from: task.originalRequest))
    }

    /// Each finished request to the hub with its timings and sizes, for the app's on-device diagnostics (Settings ›
    /// About › Developing Persona, #9460). Nil, and nothing is kept, unless the app set it.
    public static var finished: (@Sendable (URLSessionTask, URLSessionTaskMetrics) -> Void)? {
        get { observer.withLock { $0.finished } }
        set { observer.withLock { $0.finished = newValue } }
    }

    /// The closure lives in a struct and is never the lock's whole state (#12624): `withLock` hands its state over
    /// `inout`, and a bare function type there is reabstracted into a temporary and written back wrapped in two more
    /// thunks, on reads too. Each request then deepened the next call until it overflowed the delegate queue's stack.
    private struct Observer: Sendable {
        var finished: (@Sendable (URLSessionTask, URLSessionTaskMetrics) -> Void)?
    }

    private static let observer = OSAllocatedUnfairLock(initialState: Observer())

    open func urlSession(_ session: URLSession, task: URLSessionTask, didFinishCollecting metrics: URLSessionTaskMetrics) {
        Self.finished?(task, metrics)
    }
}
