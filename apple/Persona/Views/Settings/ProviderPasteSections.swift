import SwiftUI

/// The Hub reads the text. Only the selected draft can continue to an editable provider.
struct ProviderPasteSections: View {
    @Bindable var paste: ProviderPasteModel
    var matchUnavailable: Bool
    @FocusState private var typing: Bool

    var body: some View {
        Section {
            TextField("Provider configuration", text: $paste.text, axis: .vertical)
                .lineLimit(3...8).font(Theme.Font.code)
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                .privacySensitive().focused($typing)
                .accessibilityIdentifier("wizard.paste.text")
        } footer: {
            Text("A shell alias or export lines, a .env file, Claude Code's settings.json, a curl command, a provider's JSON or an import link. It's read when you stop typing, and nothing is saved until you press Save.")
        }
        if paste.reading {
            Section { ProgressView("Reading…") }
        } else if paste.failed || matchUnavailable {
            Section {
                if paste.failed {
                    Text("Couldn't read the configuration. You can try again.")
                } else {
                    Text("Couldn't open that provider. Read the configuration again.")
                }
                Button("Try again") { paste.retry() }
            }
        }
        if let answer = paste.answer {
            let drafts = answer.drafts ?? []
            if !(answer.notes ?? []).isEmpty || drafts.isEmpty {
                Section("Notes about the text") {
                    if drafts.isEmpty { Text("Nothing in it can be added as a provider.") }
                    notes(answer.notes)
                }
            }
            if !drafts.isEmpty {
                Section("Providers in the text") {
                    ForEach(drafts.indices, id: \.self) { index in
                        let d = drafts[index]
                        let name = d.pasteTitle
                        CheckRow(on: paste.selected == index, title: name, id: "wizard.paste.draft.\(index)") { typing = false; paste.select(index) } content: {
                            Text(verbatim: name).font(Theme.Font.heading)
                            if let address = d.pasteAddress, address != name {
                                Text(verbatim: address).font(Theme.Font.label).foregroundStyle(.secondary)
                            }
                            if !d.modelsToAdd.isEmpty { Text("\(d.modelsToAdd.count) models").font(Theme.Font.label).foregroundStyle(.secondary) }
                        }
                        if paste.selected == index {
                            let details = paste.settingNotes
                            ForEach(details.indices, id: \.self) { i in Text(verbatim: details[i]) }
                        }
                        notes(d.notes)
                    }
                }
            }
            if let draft = paste.draft, let matches = draft.matches, !matches.isEmpty {
                Section("What to do with it") {
                    ForEach(matches) { match in
                        let action = draft.action(for: match)
                        let name = match.displayName ?? match.name
                        let label = action == .merge(match.name) ? String(localized: "Add its models to \(name)") : String(localized: "Replace \(name)'s key")
                        actionRow(action, label, id: "wizard.paste.match.\(match.name)")
                    }
                    actionRow(.add, String(localized: "Add as a new provider"), id: "wizard.paste.new")
                }
            }
        }
    }

    @ViewBuilder private func notes(_ notes: [ProviderImportNote]?) -> some View {
        if let notes {
            ForEach(notes.indices, id: \.self) { i in Text(verbatim: notes[i].pasteText) }
        }
    }

    private func actionRow(_ action: ProviderPasteAction, _ label: String, id: String) -> some View {
        CheckRow(on: paste.action == action, title: label, id: id) { paste.action = action } content: { Text(verbatim: label) }
    }
}

extension ProviderImportDraft {
    var pasteAddress: String? {
        guard let raw = provider.baseURL, let url = URLComponents(string: raw), url.host != nil,
              let scheme = url.scheme?.lowercased(), ["https", "http"].contains(scheme), url.user == nil, url.password == nil,
              url.query == nil, url.fragment == nil else { return nil }
        return raw
    }

    var pasteTitle: String {
        [provider.displayName, label, provider.name, pasteAddress]
            .compactMap { $0 }.first { !$0.isEmpty } ?? String(localized: "Provider")
    }

    /// Report only field names. The values stay in the draft for the editor.
    var settingNotes: [String] {
        var result: [String] = []
        if let names = envNames, !names.isEmpty {
            let names = names.joined(separator: ", ")
            result.append(String(localized: "Also carries over \(names)."))
        }
        if let headers = provider.headers {
            let kept = headers.split(whereSeparator: \.isNewline).compactMap { line -> String? in
                guard let end = line.firstIndex(of: ":") else { return nil }
                return String(line[..<end])
            }
            if !kept.isEmpty {
                let names = kept.joined(separator: ", ")
                result.append(String(localized: "Also sends the headers \(names)."))
            }
        }
        result.append(contentsOf: (skippedEnv ?? []).map { item in
            let name = item.name
            switch item.reason {
            case "credential": return String(localized: "Leaves out \(name), since keys and passwords aren't carried over.")
            case "shell": return String(localized: "Leaves out \(name), since only the shell can work out the value.")
            case "computer": return String(localized: "Leaves out \(name), since the value only fits that computer.")
            case "limit": return String(localized: "Leaves out \(name), since a provider keeps at most 50.")
            default: return String(localized: "Leaves out \(name).")
            }
        })
        result.append(contentsOf: (skippedHeaders ?? []).map { item in
            let name = item.name
            switch item.reason {
            case "credential": return String(localized: "Leaves out the header \(name), since keys and passwords aren't carried over.")
            case "protocol": return String(localized: "Leaves out the header \(name), since it's sent anyway.")
            case "unsupported": return String(localized: "Leaves out the header \(name), since this kind of provider sends no extra headers.")
            case "unsafe": return String(localized: "Leaves out the header \(name), since its value can't be kept as written.")
            default: return String(localized: "Leaves out the header \(name).")
            }
        })
        return result
    }
}

extension ProviderImportNote {
    /// The parser reports a code, a line or a field name. It never includes an omitted value.
    var pasteText: String {
        switch code {
        case "unrecognized": return String(localized: "This doesn't look like a provider's settings. Paste an alias, export lines, a settings.json, a curl command or a provider's JSON.")
        case "invalid_json": return String(localized: "This looks like JSON but couldn't be read. Check that all of it was pasted.")
        case "args_left_out": return String(localized: "The words after claude aren't carried over, since they may hold a key. Add them under Advanced if you need them.")
        case "header_unreadable": return String(localized: "A header with an unusual name isn't carried over. Add it under Advanced if you need it.")
        case "key_shell": return String(localized: "The key comes from the shell, so it's left out. Paste the key itself on the next step.")
        case "no_key": return String(localized: "No key was found. Type it on the next step.")
        case "key_helper": return String(localized: "This settings.json gets its key by running a command, which can't be read here. Paste the key itself on the next step.")
        case "cindy_kind": return String(localized: "This is one of the built-in providers, not a custom one. Pick it from the list instead and paste its key.")
        case "cindy_request_path": return String(localized: "Its endpoint uses a request path of its own, which Persona can't set, so it works only if the gateway also answers at the usual path.")
        case "link_too_long": return String(localized: "This import link is longer than an import link can be.")
        case "not_claude":
            if let name { return String(localized: "The alias \(name) doesn't start Claude Code, so it isn't used.") }
            if let line { return String(localized: "Line \(line) doesn't start Claude Code, so it isn't used.") }
            return String(localized: "This command doesn't start Claude Code, so it isn't used.")
        case "no_gateway":
            if let name { return String(localized: "\(name) has no gateway address, so Claude Code signs in on its own there and there's nothing to add.") }
            if let line { return String(localized: "Line \(line) has no gateway address, so Claude Code signs in on its own there and there's nothing to add.") }
            return String(localized: "There's no gateway address in it, so Claude Code would sign in on its own and there's nothing to add.")
        case "url_shell":
            if let name { return String(localized: "\(name)'s address comes from the shell, so it can't be read here. Paste the address itself.") }
            if let line { return String(localized: "The address on line \(line) comes from the shell, so it can't be read here. Paste the address itself.") }
            return String(localized: "The address comes from the shell, so it can't be read here. Paste the address itself.")
        case "url_user":
            if let name { return String(localized: "\(name)'s address includes a user name or password, so it isn't used. Paste it without them.") }
            if let line { return String(localized: "The address on line \(line) includes a user name or password, so it isn't used. Paste it without them.") }
            return String(localized: "The address includes a user name or password, so it isn't used. Paste it without them.")
        case "url_query":
            if let name { return String(localized: "\(name)'s address has a part after ? or #, which can hold a key, so it isn't used. Paste the address without it.") }
            if let line { return String(localized: "The address on line \(line) has a part after ? or #, which can hold a key, so it isn't used. Paste the address without it.") }
            return String(localized: "The address has a part after ? or #, which can hold a key, so it isn't used. Paste the address without it.")
        case "curl_option":
            if let name { return String(localized: "The curl option \(name) isn't used.") }
        case "curl_path":
            if let name { return String(localized: "\(name) isn't a path Persona knows, so the whole URL is used as the address. Check it on the next step.") }
        case "line_unused":
            if let line { return String(localized: "Line \(line) isn't an alias, a variable or a claude command, so it isn't used.") }
        case "cindy_endpoint":
            if let name { return String(localized: "Its \(name) address isn't added, since a provider here has one address.") }
            return String(localized: "One of its addresses isn't added, since Persona has no such kind of provider.")
        case "link_invalid":
            if let name { return String(localized: "This import link is damaged or incomplete (at \(name)). Ask whoever sent it for a new one.") }
            return String(localized: "This import link is damaged or incomplete. Ask whoever sent it for a new one.")
        case "link_version":
            if let name { return String(localized: "This import link uses format version \(name), which Persona can't read yet.") }
            return String(localized: "This import link uses a format version Persona can't read yet.")
        case "link_field":
            if let name { return String(localized: "This import link has a field Persona doesn't know (\(name)), so nothing from it was used.") }
            return String(localized: "This import link has a field Persona doesn't know, so nothing from it was used.")
        case "cindy_auth":
            if name == "oauth" { return String(localized: "This provider signs in with an account, which can't be added by pasting.") }
            if name == "none" { return String(localized: "This provider doesn't use a key.") }
            if let name { return String(localized: "This provider signs in with \(name), which can't be added by pasting.") }
            return String(localized: "This provider signs in a way that can't be added by pasting.")
        case "too_many": return String(localized: "Only the first 50 were read. The rest were left out.")
        default: break
        }
        if let line { return String(localized: "Something on line \(line) couldn't be used.") }
        return String(localized: "Part of it couldn't be used.")
    }
}
