import SwiftUI

// What "Always allow" opened in an assistant conversation, one revoke per row, in Session Info (#5867,
// docs/assistant/assistant.md「Always allow in this session」). The web's AlwaysPermissions.

/// The conversation's grants and what it asks the hub (MVVM, docs/apple/ios.md「Data flow」).
@Observable final class AlwaysGrantsModel {
    /// Nil until the hub answers.
    private(set) var grants: [APIClient.AlwaysGrant]?
    /// A hub without these routes, or a device that may not use them: nothing to show, nothing to fix.
    private(set) var unavailable = false
    private(set) var error: String?
    private(set) var revoking: String?
    private(set) var restartNeeded = false
    /// Conversations still running with the permission: the warning has to outlive this sheet, and only the user's
    /// restart can clear it, which the app can't see. The web keeps it for the tab's life, this for the app's.
    @ObservationIgnored private static var notReset: Set<String> = []

    func load(conversation: String, api: APIClient?) async {
        guard let api else { return }
        restartNeeded = Self.notReset.contains(conversation)
        grants = nil
        error = nil
        do {
            grants = try await api.alwaysGrants(conversation: conversation)
        } catch let refused as APIError where refused.status == 403 || refused.status == 404 {
            unavailable = true
        } catch {
            self.error = error.shown
        }
    }

    func revoke(_ grant: APIClient.AlwaysGrant, conversation: String, api: APIClient?) async {
        guard let api else { return }
        revoking = grant.keyHash
        error = nil
        defer { revoking = nil }
        do {
            let restart = try await api.revokeAlwaysGrant(hash: grant.keyHash, conversation: conversation)
            grants?.removeAll { $0.keyHash == grant.keyHash }
            Haptics.success()
            if restart {
                Self.notReset.insert(conversation)
                restartNeeded = true
            }
        } catch {
            self.error = error.shown
        }
    }
}

struct AlwaysGrantsSection: View {
    let page: AlwaysGrantsModel
    let conversation: String
    let api: APIClient?

    var body: some View {
        if !page.unavailable {
            Section {
                if page.restartNeeded {
                    Text("The saved permission was revoked, but the running conversation could not be reset. Restart this conversation before relying on the revocation.")
                        .font(Theme.Font.caption).foregroundStyle(Theme.danger)
                }
                if let grants = page.grants {
                    if grants.isEmpty {
                        Text("No saved permissions.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                    ForEach(grants) { row($0) }
                    if let error = page.error { failure(error) }
                } else if let error = page.error {
                    failure(error)
                    Button("Retry") { Task { await page.load(conversation: conversation, api: api) } }.lineLimit(1)
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
                #if os(macOS)
                scope.font(Theme.Font.caption).foregroundStyle(.secondary) // a Mac list's footer stops at one line
                #endif
            } header: {
                Text("Always allowed in this conversation")
            } footer: {
                #if !os(macOS)
                scope
                #endif
            }
        }
    }

    private var scope: Text {
        Text("These saved permissions apply only to this conversation. Records store the tool name, but not detailed directory or command scope.")
    }

    private func failure(_ text: String) -> some View {
        Label(text, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
    }

    private func row(_ grant: APIClient.AlwaysGrant) -> some View {
        AlwaysGrantRow(grant: grant, busy: page.revoking != nil) {
            await page.revoke(grant, conversation: conversation, api: api)
        }
    }
}

private struct AlwaysGrantRow: View {
    let grant: APIClient.AlwaysGrant
    let busy: Bool
    let revoke: () async -> Void
    @State private var confirming = false

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(grant.toolName.isEmpty ? String(localized: "Unnamed permission") : grant.toolName)
                    .font(Theme.Font.body).lineLimit(1)
                Text(Fmt.relative(grant.createdAt)).font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
            Button { confirming = true } label: { Text("Revoke").lineLimit(1) }
                .buttonStyle(.borderless)
                .tint(Theme.danger)
                .disabled(busy)
                .accessibilityIdentifier("always-grant-revoke")
                .accessibleConfirmationDialog("Revoke this permission?", isPresented: $confirming, titleVisibility: .visible) {
                    Button("Revoke", role: .destructive) { Task { await revoke() } }
                } message: {
                    Text("Revoking stops the current response to clear its permissions. Send another message to continue.")
                }
        }
    }
}
