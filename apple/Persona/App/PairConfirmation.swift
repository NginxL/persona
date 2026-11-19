import SwiftUI

/// Asking before a pairing link pairs (#9929; docs/protocol/deep-links.md "Not routes: action links"). A link opened from
/// anywhere (Safari, Messages, another app, the camera) or read by the pairing page's scanner used to add the hub it
/// names at once: a link must only ever open a page, so pairing waits for the user's Connect. The prompt names the hub's
/// address as the link gives it, and says when that hub is already paired.
nonisolated struct PairConfirmation: Identifiable, Equatable, Sendable {
    let config: ServerConfig
    /// The hub's address as the link gives it: its host, and its port when it is not the scheme's own.
    let host: String
    /// This address is paired already: connecting again refreshes the pairing, nothing new is added.
    let alreadyPaired: Bool
    var id: URL { config.url }

    init(_ config: ServerConfig, paired: [ServerConfig]) {
        self.config = config
        host = Self.address(config.url)
        alreadyPaired = paired.contains { $0.url == config.url }
    }

    /// `host[:port]`, an IPv6 host in brackets; the whole link when it has no host.
    static func address(_ url: URL) -> String {
        guard let host = url.host(percentEncoded: false), !host.isEmpty else { return url.absoluteString }
        let shown = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
        guard let port = url.port else { return shown }
        let usual = url.scheme?.lowercased() == "https" ? 443 : 80
        return port == usual ? shown : "\(shown):\(port)"
    }

    var title: String { String(localized: "Connect to \(host)?") }

    var message: String {
        alreadyPaired
            ? String(localized: "This hub is already paired. Connecting again refreshes this device's pairing.")
            : String(localized: "Only connect to a hub you set up yourself or trust. It will see this device and can send it notifications.")
    }
}

extension View {
    /// The pairing question while `item` is set: Connect runs `connect`, Cancel only closes it.
    func pairConfirmation(_ item: Binding<PairConfirmation?>, connect: @escaping (PairConfirmation) -> Void) -> some View {
        alert(item.wrappedValue?.title ?? "", isPresented: Binding(
            get: { item.wrappedValue != nil },
            set: { if !$0 { item.wrappedValue = nil } }
        ), presenting: item.wrappedValue) { c in
            Button("Connect [V N C Page a55741]") {
                item.wrappedValue = nil
                connect(c)
            }
            .accessibilityIdentifier("pair.confirm")
            Button("Cancel", role: .cancel) { item.wrappedValue = nil }
                .accessibilityIdentifier("pair.cancel")
        } message: { c in
            Text(c.message)
        }
    }
}

extension AppModel {
    /// A pairing link from outside the app: asks first (`PairConfirmation`), pairs on Connect.
    func askToPair(_ config: ServerConfig) {
        pairToConfirm = PairConfirmation(config, paired: registry.hubs.map(\.config))
    }

    /// Pairs with the hub a confirmed link names, saying how it went: connected, an expired code, or why it failed.
    func pairFromLink(_ cfg: ServerConfig) {
        Task {
            do {
                try await pair(cfg)
                let host = cfg.url.host() ?? String(localized: "Server")
                toast = Toast(String(localized: "Connected to \(host)"), style: .success)
            } catch {
                // An expired code says so (showPairing); anything else is said too, not through `show`, which keeps
                // quiet about a timeout or no network: a hub out of reach then said nothing after 30 s on the welcome
                // page (#4273).
                if PairError.isExpiredCode(error) {
                    showPairing(error)
                } else if !error.isCancellation {
                    toast = Toast(String(localized: "Couldn't connect: \(error.localizedDescription)"), style: .error)
                    Haptics.error()
                }
            }
        }
    }
}
