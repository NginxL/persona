import Foundation
import Observation
import PersonaWire

nonisolated struct ProviderImportMatch: Decodable, Identifiable, Sendable {
    var name: String
    var displayName: String?
    var sameKey: Bool
    var id: String { name }
    enum CodingKeys: String, CodingKey {
        case name, displayName = "display_name", sameKey = "same_key"
    }
}

nonisolated struct ProviderImportSkip: Decodable, Sendable {
    var name: String
    var reason: String
}

enum ProviderPasteAction: Hashable {
    case add
    case merge(String)
    case replaceKey(String)
}

extension ProviderImportDraft {
    var defaultAction: ProviderPasteAction {
        matches?.first(where: \.sameKey).map { .merge($0.name) } ?? .add
    }

    func action(for match: ProviderImportMatch) -> ProviderPasteAction {
        match.sameKey || (provider.apiKey ?? "").isEmpty ? .merge(match.name) : .replaceKey(match.name)
    }

    var modelsToAdd: [ProviderModel] {
        var models = provider.models ?? []
        if let id = provider.model, !id.isEmpty, !models.contains(where: { $0.id == id }) { models.insert(ProviderModel(id: id), at: 0) }
        return models
    }
}

/// Only importable shell aliases from the provider list. The selected computer is part of both import requests.
struct FoundProviderAlias: Identifiable {
    let name: String
    let host: String?
    let machine: String
    let computer: String
    let online: Bool
    var id: String { name }

    static func available(_ launchers: [LauncherView], machines: [Machine]) -> [Self] {
        let origins = Set(launchers.filter(\.managed).compactMap(\.origin))
        let computers = Dictionary(machines.map { ($0.id, $0) }, uniquingKeysWith: { first, next in first.online ? first : next })
        return launchers.compactMap { launcher in
            guard launcher.source == "zshrc", launcher.hidden != true, let url = launcher.baseURL, !url.isEmpty,
                  let ids = launcher.machines, let first = ids.first,
                  !ids.contains(where: { origins.contains("zshrc:\(launcher.name)@\($0)") }) else { return nil }
            let id = ids.first { computers[$0]?.online == true } ?? first
            let machine = computers[id]
            return Self(name: launcher.name, host: URLComponents(string: url)?.host, machine: id,
                        computer: machine.flatMap { $0.name.isEmpty ? nil : $0.name } ?? id, online: machine?.online == true)
        }
    }
}

/// Temporary input for one presentation of the provider wizard. Reading a draft never saves a provider.
@Observable final class ProviderPasteModel {
    static let feature = "launcher_import_parse"
    var text = "" {
        didSet {
            guard text != oldValue else { return }
            immediate = Self.insertedSeveralCharacters(text, replacing: oldValue)
            retry()
        }
    }
    private(set) var revision = 0
    private(set) var reading = false
    private(set) var failed = false
    private(set) var answer: ProviderImportResult?
    private(set) var selected: Int?
    private(set) var settingNotes: [String] = []
    var action: ProviderPasteAction?
    @ObservationIgnored private var immediate = false

    var draft: ProviderImportDraft? {
        guard let selected, let drafts = answer?.drafts, drafts.indices.contains(selected) else { return nil }
        return drafts[selected]
    }

    func select(_ index: Int) {
        selected = index
        action = draft?.defaultAction
        settingNotes = draft?.settingNotes ?? []
    }

    func retry() {
        revision += 1
        answer = nil
        selected = nil
        settingNotes = []
        action = nil
        failed = false
        reading = false
    }

    /// A text field can paste over a selection without changing its total length. Compare the changed range,
    /// so an ordinary keystroke still debounces and inserting a whole configuration starts at once.
    static func insertedSeveralCharacters(_ text: String, replacing old: String) -> Bool {
        var newPart = text[...], oldPart = old[...]
        while let first = newPart.first, first == oldPart.first {
            newPart = newPart.dropFirst()
            oldPart = oldPart.dropFirst()
        }
        while let last = newPart.last, last == oldPart.last {
            newPart = newPart.dropLast()
            oldPart = oldPart.dropLast()
        }
        return !newPart.dropFirst().isEmpty
    }

    func read(using api: APIClient?) async {
        guard answer == nil, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let request = revision
        do {
            if !immediate { try await Task.sleep(for: .milliseconds(700)) }
            try Task.checkCancellation()
            guard revision == request else { return }
            guard let api else { failed = true; return }
            reading = true
            let result = try await api.parseProviderImport(text: text)
            guard revision == request, !Task.isCancelled else { return }
            answer = result
            reading = false
            if result.drafts?.count == 1 { select(0) }
        } catch {
            guard revision == request, !Task.isCancelled else { return }
            reading = false
            // A transport error can contain the configuration. Show fixed text and keep the input available for retry.
            failed = true
        }
    }
}
