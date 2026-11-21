import Foundation

/// Settings › Devices, "Primary Device" (docs/apple/ios.md, docs/apple/phone-tools.md): which
/// devices the choice lists, in which order, and which one is chosen. Pure, for the tests; the page only draws it.
nonisolated enum PhonePrimaryChoices {
    /// One row of the choice. `id` is the credential the hub is told; "" is Automatic (`device` is nil then).
    struct Option: Identifiable, Hashable, Sendable {
        var id: String
        var device: APIClient.DeviceToken?
        var isCurrent: Bool
    }

    /// The tag of Automatic, as the hub spells it (PUT /api/v1/phone/primary with "" goes back to it).
    static let automatic = ""

    /// Shown only when the hub speaks of it (an older hub has no `phone`) and there is something to choose: two or more
    /// devices that can answer phone calls, or a device already chosen (so it can be changed back).
    static func isShown(_ phone: APIClient.PhonePrimary?) -> Bool {
        guard let phone else { return false }
        return phone.candidates.count >= 2 || !phone.primary.isEmpty
    }

    /// Automatic, then every candidate in the hub's order. The chosen device is listed even when it is not a candidate
    /// now (its phone data is off, or it is offline), so the choice can always be shown. A credential the page has no
    /// device for (revoked since, or not in the list yet) cannot be named and is left out.
    static func options(phone: APIClient.PhonePrimary?, tokens: [APIClient.DeviceToken], current: String?) -> [Option] {
        guard let phone else { return [] }
        var ids = phone.candidates
        if !phone.primary.isEmpty, !ids.contains(phone.primary) { ids.append(phone.primary) }
        var out = [Option(id: automatic, device: nil, isCurrent: false)]
        var seen: Set<String> = []
        for id in ids where seen.insert(id).inserted {
            guard let device = tokens.first(where: { $0.id == id && $0.revokedAt == nil }) else { continue }
            out.append(Option(id: id, device: device, isCurrent: id == current))
        }
        return out
    }

    /// The tag the choice shows: the chosen device, or Automatic when none is chosen (or the chosen one cannot be named).
    static func selection(phone: APIClient.PhonePrimary?, options: [Option]) -> String {
        guard let primary = phone?.primary, options.contains(where: { $0.id == primary }) else { return automatic }
        return primary
    }

    /// `phone` with `id` chosen, before the hub has answered (the choice takes effect on screen at once). The chosen
    /// device that is not a candidate stays out of the candidates: the hub's answer decides that.
    static func choosing(_ id: String, in phone: APIClient.PhonePrimary?) -> APIClient.PhonePrimary? {
        guard var phone else { return nil }
        phone.primary = id
        return phone
    }
}
