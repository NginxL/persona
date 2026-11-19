import Foundation

/// A page of Persona the app has natively: a row of the route table every client follows (docs/protocol/deep-links.md,
/// #9926). Its paths are the hub's web app's (web/src/lib/router.ts), which the hub writes into what it sends
/// (server/internal/deeplink); the same path after `persona://open` is the page's app link.
nonisolated enum HubPage: Equatable, Hashable, Sendable {
    /// An agent's conversation (`/s/…`, `/t/…/<session>`); `item`: the message to land on.
    case agent(machine: String, session: String, item: String?)
    /// A Persona conversation (`/persona/<id>`).
    case conversation(id: String, item: String?)
    /// A task (`/todos/<id>`, `/todos?id=…`).
    case todo(id: String)
    /// Persona's own page (`/persona`, `/`).
    case persona
    /// A new conversation with Persona (`/persona/new`).
    case newConversation
    /// The Agents list (`/agents`).
    case agents
    /// The Inbox (`/inbox`).
    case inbox
    /// One Inbox item (`/inbox/<id>`); `manager`: the assistant whose item it is, nil for the current one.
    case inboxItem(id: String, manager: String?)
    /// The to-do list (`/todos`).
    case todos
    /// The knowledge base (`/kb`), at one kind of its entries (`/kb/<kind>`).
    case knowledge(kind: String?)
    /// One knowledge entry (`/kb/<kind>/<id>`); meetings and recordings are kinds.
    case knowledgeEntry(kind: String, id: String)
    /// The computers (`/machines`).
    case machines
    /// One computer (`/machines/<id>`).
    case machine(id: String)
    /// Settings (`/settings`), at a tab (`?tab=`) and its item (the tab's own key) when the link names them.
    case settings(tab: String?, item: String?)
    /// A page an extension draws (`/extension/<extension>/<ui>`), in `state` when the link gives one.
    case extensionPage(extension: String, ui: String, state: String?)
}

nonisolated enum HubPageLink {
    /// The page `url` is when it points at one of `hubs` (a group's addresses: paired, added, current, mesh endpoints, its
    /// `public_url`); nil for another site, another hub, a link with a user name or password, or a path the route table
    /// does not have.
    static func page(_ url: URL, hubs: [URL]) -> HubPage? {
        guard let comps = URLComponents(url: url, resolvingAgainstBaseURL: false), comps.user == nil, comps.password == nil,
              let base = hubs.first(where: { sameOrigin(url, $0) }) else { return nil }
        var parts = segments(comps.percentEncodedPath)
        // A hub behind a reverse proxy's sub path (https://<host>/<prefix>): its pages are under that prefix.
        let prefix = segments(URLComponents(url: base, resolvingAgainstBaseURL: false)?.percentEncodedPath ?? "")
        if !prefix.isEmpty {
            guard parts.starts(with: prefix) else { return nil }
            parts.removeFirst(prefix.count)
        }
        guard let decoded = decode(parts) else { return nil }
        return page(path: decoded, query: comps.queryItems ?? [])
    }

    /// The page an app link opens, with the hub it names (`hub=<hub id>`, nil: the one on screen): `persona://open<path>`,
    /// the desktop app's `persona://open?route=<path>`, and the older `persona://session|task|kb|settings|extension/…`
    /// forms. Nil for any other link, and for an app link that is malformed (a port, a route that would leave the hub).
    static func appPage(_ url: URL) -> (page: HubPage, hub: String?)? {
        guard HubNaming.accepts(scheme: url.scheme), let comps = URLComponents(url: url, resolvingAgainstBaseURL: false),
              comps.user == nil, comps.password == nil, comps.port == nil, let host = comps.host?.lowercased() else { return nil }
        let query = comps.queryItems ?? []
        let hub = value("hub", in: query)
        guard let parts = decode(segments(comps.percentEncodedPath)) else { return nil }
        let found: HubPage?
        switch host {
        case "open":
            if comps.percentEncodedPath.hasPrefix("//") { return nil } // persona://open//<host>: not a path of the hub
            if !parts.isEmpty {
                found = page(path: parts, query: query)
            } else if let route = value("route", in: query) {
                found = routePage(route)
            } else if let m = value("machine", in: query), let s = value("session", in: query) {
                found = .agent(machine: m, session: s, item: nil) // the Live Activity's rows (FleetLink)
            } else {
                return nil // `persona://open` alone only brings the app forward
            }
        case "session" where parts.count == 2:
            found = .agent(machine: parts[0], session: parts[1], item: value("at", in: query))
        case "task" where parts.count == 1:
            found = .todo(id: parts[0])
        case "kb" where parts.count >= 2 && KBKind(rawValue: parts[0]) != nil:
            found = .knowledgeEntry(kind: parts[0], id: parts.dropFirst().joined(separator: "/"))
        case "settings" where parts == ["sent"]:
            found = .settings(tab: "connectors", item: sentSettings)
        case "settings" where parts.count == 2 && parts[0] == "extensions":
            found = .settings(tab: "extensions", item: parts[1])
        case "extension" where parts.count == 2:
            found = .extensionPage(extension: parts[0], ui: parts[1], state: value("state", in: query))
        default:
            found = nil
        }
        return found.map { ($0, hub) }
    }

    /// The page of a route the desktop app's first form carries (`route=<escaped path and query>`): one leading "/",
    /// never "//", no backslash or control character.
    private static func routePage(_ route: String) -> HubPage? {
        guard route.hasPrefix("/"), !route.hasPrefix("//"), !route.contains("\\"),
              !route.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }),
              let comps = URLComponents(string: route), comps.host == nil, comps.scheme == nil,
              let parts = decode(segments(comps.percentEncodedPath)) else { return nil }
        return page(path: parts, query: comps.queryItems ?? [])
    }

    /// The Sent records' item of the connectors tab (web/src/views/settings/nav.ts `SENT_SUB`).
    static let sentSettings = "-sent"

    /// Settings tabs and the key that names an item of the ones that have one (web/src/views/settings/nav.ts).
    static let settingsTabs: [String: String?] = [
        "general": nil, "persona": nil, "prompts": nil, "notifications": nil, "models": "purpose", "agents": nil,
        "channels": "channel", "connectors": "connector", "sources": nil, "extensions": "extension", "shortcuts": nil,
        "devices": nil, "hub": nil, "managers": nil, "knowledge": nil, "avatar": nil,
    ]

    /// The page for a path's segments (already decoded) and its query; the hub is not checked here. Keys the route
    /// table does not list for the page are not read.
    static func page(path p: [String], query: [URLQueryItem]) -> HubPage? {
        let at = value("at", in: query)
        switch p.first {
        case nil:
            return .persona
        case "s" where p.count == 3 && !p[1].isEmpty && !p[2].isEmpty:
            return .agent(machine: p[1], session: p[2], item: at)
        case "t" where p.count == 5 && !p[1].isEmpty && !p[4].isEmpty:
            // /t/<machine>/<space>/<slot>/<session>: the tab's agent. Without the session it names a place, not a chat.
            return .agent(machine: p[1], session: p[4], item: at)
        case "persona", LegacyNames.conversationPath:
            switch p.count {
            case 1: return .persona
            case 2 where p[1] == "new": return .newConversation
            case 2 where !p[1].isEmpty: return .conversation(id: p[1], item: at)
            default: return nil
            }
        case "agents" where p.count == 1, "a" where p.count == 1:
            return .agents
        case "inbox" where p.count == 1:
            return .inbox
        case "inbox" where p.count == 2:
            return .inboxItem(id: p[1], manager: value("manager", in: query))
        case "todos":
            if p.count == 2, !p[1].isEmpty { return .todo(id: p[1]) }
            if p.count == 1 { return value("id", in: query).map { .todo(id: $0) } ?? .todos }
            return nil
        case "kb":
            guard p.count == 1 || KBKind(rawValue: p[1]) != nil else { return nil }
            switch p.count {
            case 1: return .knowledge(kind: nil)
            case 2: return .knowledge(kind: p[1])
            case 3: return .knowledgeEntry(kind: p[1], id: p[2])
            default: return nil
            }
        case "machines" where p.count == 1:
            return .machines
        case "machines" where p.count == 2:
            return .machine(id: p[1])
        case "settings" where p == ["settings", "sent"]:
            return .settings(tab: "connectors", item: sentSettings)
        case "settings" where p.count == 1:
            return settings(query)
        case "extension" where p.count == 3, "plugin" where p.count == 3:
            return .extensionPage(extension: p[1], ui: p[2], state: value("state", in: query))
        default:
            return nil
        }
    }

    /// Settings at the tab and item a query names; an unknown tab is Settings itself.
    private static func settings(_ query: [URLQueryItem]) -> HubPage {
        guard let tab = value("tab", in: query) else { return .settings(tab: nil, item: nil) }
        if tab == "sent" { return .settings(tab: "connectors", item: sentSettings) } // the older tab of Sent records
        guard let key = settingsTabs[tab] else { return .settings(tab: nil, item: nil) }
        return .settings(tab: tab, item: key.flatMap { value($0, in: query) })
    }

    private static func value(_ name: String, in query: [URLQueryItem]) -> String? {
        query.first { $0.name == name }?.value.flatMap { $0.isEmpty ? nil : $0 }
    }

    /// Same scheme-less origin: host (case-insensitive, IPv6 without brackets) and port (the scheme's default when not
    /// written). The scheme itself may differ: the hub's TLS and plain ports are both its own.
    static func sameOrigin(_ a: URL, _ b: URL) -> Bool {
        guard let x = origin(a), let y = origin(b) else { return false }
        return x == y
    }

    private static func origin(_ url: URL) -> String? {
        guard let comps = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let scheme = comps.scheme?.lowercased(), scheme == "http" || scheme == "https",
              let host = comps.host?.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "[]")), !host.isEmpty
        else { return nil }
        let port = comps.port ?? (scheme == "https" ? 443 : 80)
        return "\(host):\(port)"
    }

    private static func segments(_ path: String) -> [String] {
        path.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
    }

    /// Each segment unescaped; nil when one is malformed.
    private static func decode(_ parts: [String]) -> [String]? {
        let decoded = parts.map { $0.removingPercentEncoding }
        return decoded.contains(nil) ? nil : decoded.compactMap { $0 }
    }
}
