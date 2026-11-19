import SwiftUI
import PersonaWire

/// The two values of a Cloudflare Access service token (docs/operations/network.md "Behind Cloudflare Access"): the
/// pairing sheet's and Settings › hub › Cloudflare Access's. The secret is masked and never offered to the password
/// manager (`SecretField`).
struct CloudflareAccessFields: View {
    @Binding var clientID: String
    @Binding var clientSecret: String
    /// The fields' accessibility identifiers start with it (`<prefix>.id`, `<prefix>.secret`).
    var idPrefix: String

    var body: some View {
        #if os(macOS)
        TextField(String(localized: "Client ID"), text: $clientID, prompt: Text("Required"))
            .autocorrectionDisabled()
            .accessibilityIdentifier(idPrefix + ".id")
        SecretField(String(localized: "Client Secret"), text: $clientSecret, identifier: idPrefix + ".secret")
        #else
        LabeledContent {
            TextField(String(localized: "Client ID"), text: $clientID, prompt: Text("Required"))
                .multilineTextAlignment(.trailing)
                .keyboardType(.asciiCapable)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier(idPrefix + ".id")
        } label: {
            Text("Client ID").lineLimit(1)
        }
        LabeledContent {
            SecretField(String(localized: "Required"), text: $clientSecret, identifier: idPrefix + ".secret")
                .multilineTextAlignment(.trailing)
        } label: {
            Text("Client Secret").lineLimit(1)
        }
        #endif
    }
}

/// Settings › hub › Cloudflare Access: one paired hub's service token, changed or cleared. Saved, the group's
/// connection starts again with it.
struct HubCloudflareAccessPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let hubID: String
    @State private var clientID = ""
    @State private var clientSecret = ""
    @State private var loaded = false
    @State private var clearing = false

    var body: some View {
        if let hub = model.registry.hubs.first(where: { $0.id == hubID }) {
            form(hub)
                .detailTitle(String(localized: "Cloudflare Access"))
                .onAppear {
                    guard !loaded else { return }
                    loaded = true
                    clientID = hub.config.access?.clientID ?? ""
                    clientSecret = hub.config.access?.clientSecret ?? ""
                }
        } else {
            EmptyStateView(symbol: Theme.Icon.cloudflareAccess, title: String(localized: "This Hub Is Disconnected"), message: String(localized: "Go back to Settings › Hub to pair again."))
        }
    }

    /// What the fields say: a token, or nil (both empty: none).
    private var draft: HubAccess? { HubAccess(clientID: clientID, clientSecret: clientSecret) }
    /// Both filled in, or both empty: something to save.
    private var complete: Bool {
        draft != nil || (clientID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && clientSecret.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    private func form(_ hub: PairedHub) -> some View {
        Form {
            if let problem = problem(hub) {
                Section {
                    Label {
                        Text(problem).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.danger)
                    }
                    .accessibilityIdentifier("access.problem")
                }
            }
            Section {
                CloudflareAccessFields(clientID: $clientID, clientSecret: $clientSecret, idPrefix: "access")
            } header: {
                SettingsHeading(String(localized: "Service Token"))
            } footer: {
                Text("For a hub published through Cloudflare Access: the app sends this service token with every request to \(hub.config.url.host() ?? hub.config.url.absoluteString), and only there. It's kept with this hub's pairing in the keychain.")
            }
            Section {
                Button { save(hub) } label: {
                    Text("Save").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
                .disabled(!complete || draft == hub.config.access)
                .accessibilityIdentifier("access.save")
                #if os(iOS) || os(visionOS)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
                #endif
            }
            if hub.config.access != nil {
                Section {
                    Button(role: .destructive) { clearing = true } label: {
                        SettingsRow(title: String(localized: "Remove Service Token"), symbol: Theme.Icon.delete, tint: Theme.danger)
                    }
                    .modifier(SettingsButtonLook())
                    .bareListRow()
                    .accessibilityIdentifier("access.clear")
                }
            }
        }
        .themedPage()
        #if os(macOS)
        .formStyle(.grouped)
        #endif
        .accessibleConfirmationDialog("Remove Service Token?", isPresented: $clearing, titleVisibility: .visible) {
            Button("Remove Service Token", role: .destructive) {
                model.setHubAccess(nil, hub: hubID)
                dismiss()
            }
        } message: {
            Text("The app stops sending it; a hub behind Cloudflare Access can't be reached without it.")
        }
    }

    private func save(_ hub: PairedHub) {
        guard complete else { return }
        model.setHubAccess(draft, hub: hubID)
        Haptics.success()
        model.toast = Toast(String(localized: "Saved"), style: .success)
    }

    /// The group's connection failing on Access: what it said, here where it can be fixed.
    private func problem(_ hub: PairedHub) -> String? {
        model.socket(hub.group)?.accessBlocked?.errorDescription
    }
}
