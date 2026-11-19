import Foundation

/// The mock's assistants, for the UI tests that need the MacBook's agents (#8749, #10164, #10253). With the default
/// mock's several assistants the MacBook is another assistant's, and the app lists only the current one's computers
/// and sessions (#5587). Such a test runs on `MOCK_MANAGERS=1` and stops at launch on a hub with several, saying how
/// to start the mock, instead of failing later on a page that lacks them.
enum MockHubRoster {
    /// Throws when `hub` has more than one installed assistant; `lacks` says what the test would then miss. No roster
    /// (an older hub) is one assistant too.
    static func requireOneAssistant(_ hub: String, lacks: String) throws {
        guard let url = URL(string: hub + "/api/v1/managers"), let data = try? Data(contentsOf: url),
              let managers = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["managers"] as? [[String: Any]]
        else { return }
        let installed = managers.filter { $0["installing"] == nil }.count // one still installing isn't one yet
        guard installed <= 1 else {
            throw NSError(domain: "MockHubRoster", code: 1, userInfo: [NSLocalizedDescriptionKey:
                "the hub has \(installed) assistants and \(lacks): "
                + "start scripts/mock/mock_hub.py with MOCK_MANAGERS=1 (docs/operations/testing.md)"])
        }
    }
}
