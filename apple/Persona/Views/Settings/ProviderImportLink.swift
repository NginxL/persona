import Foundation
import PersonaWire

/// A provider import link handed to the app (#5428): `persona://provider/import?v=1&data=…`, the same cindy v1 payload
/// as the web's `#import=` link and cindy's own scheme. The app never reads the payload itself — it sends the link text
/// to the hub's parse route and opens the provider wizard with the draft, so the link reads the same on every client
/// (`spec/fixtures/provider-import-link/cases.json`). The link may carry a key, so it is never shown, logged or kept
/// past the wizard.
enum ProviderImportLink {
    /// The link text to send the hub when `url` is a provider import link, else nil. The payload stays unchecked here:
    /// the hub refuses a malformed link with a plain-words note the wizard shows.
    static func text(_ url: URL) -> String? {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false),
              c.scheme == "persona", c.host == "provider", c.path == "/import" else { return nil }
        return url.absoluteString
    }
}

/// One external import and the providers route that owns it. A repeated link is a new request, not the old page's request.
nonisolated struct ProviderImportRequest: Hashable, Sendable {
    var id = UUID()
    var link: String
}

/// A provider as an import link's draft carries it (the hub's `pastedProvider`): the fields the wizard pre-fills.
/// Unlike `LauncherView`, a draft may carry `api_key` — whoever opens a link with a key in it saves it as their own.
nonisolated struct ProviderImportProvider: Decodable, Sendable {
    var kind: String?
    var agent: String?
    var api: String?
    var name: String?
    var displayName: String?
    var baseURL: String?
    var auth: String?
    var apiKey: String?
    var model: String?
    var models: [ProviderModel]?
    var args: [String]?
    var customModel: String?
    var customModelName: String?
    var customModelDescription: String?
    var opusModel: String?
    var sonnetModel: String?
    var haikuModel: String?
    var fableModel: String?
    var smallModel: String?
    var subagentModel: String?
    var subagentForce: Bool?
    var effort: String?
    var compactWindow: Int?
    var maxContext: Int?
    var timeoutMS: Int?
    var proxy: String?
    var noProxy: String?
    var headers: String?
    var env: String?

    enum CodingKeys: String, CodingKey {
        case kind, agent, api, name, auth, model, models, args, effort, proxy, headers, env
        case displayName = "display_name"
        case baseURL = "base_url"
        case apiKey = "api_key"
        case customModel = "custom_model"
        case customModelName = "custom_model_name"
        case customModelDescription = "custom_model_description"
        case opusModel = "opus_model"
        case sonnetModel = "sonnet_model"
        case haikuModel = "haiku_model"
        case fableModel = "fable_model"
        case smallModel = "small_model"
        case subagentModel = "subagent_model"
        case subagentForce = "subagent_force"
        case compactWindow = "compact_window"
        case maxContext = "max_context"
        case timeoutMS = "timeout_ms"
        case noProxy = "no_proxy"
    }
}

nonisolated struct ProviderImportDraft: Decodable, Sendable {
    var label: String?
    var provider: ProviderImportProvider
}

nonisolated struct ProviderImportNote: Decodable, Sendable {
    var code: String
    var line: Int?
    var name: String?

    /// A refused link's reason in plain words (#5428): what was wrong with the link, never its value (which may be a key).
    var text: String {
        switch code {
        case "link_invalid": return String(localized: "This import link isn't valid.")
        case "link_version": return String(localized: "This import link is a version Persona doesn't know yet.")
        case "link_too_long": return String(localized: "This import link is too long.")
        case "link_field": return String(localized: "This import link has a field Persona doesn't know.")
        default: return String(localized: "This import link couldn't be read.")
        }
    }
}

/// `POST /api/v1/launchers/import/parse`'s answer (docs/integrations/launchers.md「Import links」): the drafts a link
/// reads into, or the notes why it was refused (never a draft then).
nonisolated struct ProviderImportResult: Decodable, Sendable {
    var format: String?
    var drafts: [ProviderImportDraft]?
    var notes: [ProviderImportNote]?

    /// Older hubs can answer the shared paste route with another format; only a link answer may open an import draft.
    var isProviderLink: Bool { format == "link" }
}

extension APIClient {
    /// Reads an import link (or a pasted configuration) into provider drafts. Saves nothing and reads no computer.
    func parseProviderImport(text: String) async throws -> ProviderImportResult {
        struct Body: Encodable { var text: String }
        return try JSON.decoder.decode(ProviderImportResult.self, from: await request("POST", "/api/v1/launchers/import/parse", body: Body(text: text)))
    }
}
