import SwiftUI

/// The hub supplies the extension's recently requested lists and the providers its custom list may call.
struct ExtensionModelCandidatesView: View {
    @Bindable var page: ModelConfigurationModel
    let policy: ExtensionModelCandidates
    @Environment(\.locale) private var locale

    private func title(_ mode: String) -> String? {
        switch mode {
        case "extension": String(localized: "Extension lists")
        case "off": String(localized: "Purpose model only")
        case "custom": String(localized: "My list")
        default: nil
        }
    }

    var body: some View {
        Section {
            Picker("Use models from", selection: $page.candidateMode) {
                ForEach(policy.modes, id: \.self) { mode in
                    if let label = title(mode) { Text(label).tag(mode) }
                }
            }
            .accessibilityIdentifier("model-candidates-mode")
        } header: { Text("Candidate models") }
        .disabled(page.loading || page.saving || page.conflict)

        if page.candidateMode == "custom" {
            Section {
                ForEach($page.candidateRows) { $row in
                    if let index = page.candidateRows.firstIndex(where: { $0.id == row.id }) {
                        customRow($row, at: index)
                    }
                }
                Button {
                    page.addCandidate()
                } label: { Label("Add candidate", systemImage: Theme.Icon.add) }
                .disabled(page.candidateRows.count >= policy.most || page.candidateProviders.isEmpty)
                .accessibilityIdentifier("model-candidates-add")
            } header: { Text("My model list") } footer: {
                if page.candidateProviders.isEmpty {
                    Text("No providers can be called directly for this purpose.")
                } else if page.hasDuplicateCandidates {
                    Text("Each provider and model combination must be different.")
                } else {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Up to \(policy.most) candidates.")
                        Text("Models are tried in order. Leave a model ID empty to use the provider’s default.")
                    }
                }
            }
            .disabled(page.loading || page.saving || page.conflict)
        }

        Section {
            if policy.requested.isEmpty {
                Text("This extension hasn’t requested any model lists yet.").foregroundStyle(.secondary)
            }
            ForEach(policy.requested) { request in
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    Text("Last used \(request.lastUsed.formatted(.dateTime.month(.abbreviated).day().year().hour().minute().locale(locale)))")
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                    ForEach(request.models, id: \.model) { entry in
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(entry.model).font(Theme.Font.body)
                            Text(resolution(entry)).font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
        } header: { Text("Recently requested lists") }
    }

    private func resolution(_ entry: ExtensionModelCandidates.Entry) -> String {
        if !entry.available { return entry.problem ?? String(localized: "Unavailable model") }
        return entry.providerName ?? page.providers.first { $0.id == entry.provider }?.name ?? entry.provider ?? String(localized: "Available model")
    }

    private func customRow(_ row: Binding<ModelConfigurationModel.CandidateRow>, at index: Int) -> some View {
        let id = row.wrappedValue.id
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack {
                Text("Candidate \(index + 1)").font(Theme.Font.labelStrong)
                Spacer()
                Menu {
                    Button("Move Up", systemImage: Theme.Icon.up) { page.moveCandidate(id, by: -1) }.disabled(index == 0)
                    Button("Move Down", systemImage: Theme.Icon.down) { page.moveCandidate(id, by: 1) }
                        .disabled(index + 1 >= page.candidateRows.count)
                    Button("Delete", systemImage: Theme.Icon.delete, role: .destructive) {
                        page.candidateRows.removeAll { $0.id == id }
                    }
                } label: { Label("Edit candidate", systemImage: Theme.Icon.moreMenu).labelStyle(.iconOnly) }
                .accessibilityIdentifier("model-candidate-actions-\(index)")
            }
            Picker("Provider", selection: row.choice.provider) {
                ForEach(page.candidateProviders) { provider in Text(provider.name).tag(provider.id) }
                let selected = row.wrappedValue.choice.provider
                if !page.candidateProviders.contains(where: { $0.id == selected }) {
                    Text(page.providers.first { $0.id == selected }?.name ?? selected).tag(selected).disabled(true)
                }
            }
            .accessibilityIdentifier("model-candidate-provider-\(index)")
            TextField("Model ID", text: row.choice.model, prompt: Text("Provider default"))
                .font(Theme.Font.body.monospaced())
                #if os(iOS)
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                #endif
                .accessibilityIdentifier("model-candidate-model-\(index)")
        }
        .padding(.vertical, Theme.Space.xs)
    }
}
