import Foundation
import Testing

/// `@AppStorage` redraws its view on any write to UserDefaults, whatever the key; on the Mac a window being resized
/// writes the split view's frames on every frame, so every view holding one recomputed each frame (settings ran its body
/// twice a frame, #1337). New views use `@StoredValue` (Persona/App/StoredValue.swift), which redraws only for its own
/// key. The files below still hold an `@AppStorage`: the list may only get shorter.
struct AppStorageRatchetTests {
    private static let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()

    /// Converting one of these: take it off the list.
    private static let remaining: Set<String> = [
        "Persona/App/AgentSync.swift",
        "Persona/App/PerfSignpost.swift",
        "Persona/App/StoredValue.swift", // its documentation
        "Persona/App/Translator.swift",
        "Persona/Views/Inbox/Lark/LarkChatView.swift",
        "Persona/Views/Mail/MailViews.swift",
        "Persona/Views/NewChat/NewChatView.swift",
        "Persona/Views/Session/FilesPane.swift",
        "Persona/Views/Session/SessionPanes.swift", // the terminal pane
        "Persona/Views/Session/Terminal/TerminalExtras.swift",
        "Persona/Views/Settings/TranslationSettingsView.swift",
    ]

    @Test func noNewAppStorage() throws {
        let apple = Self.here.deletingLastPathComponent()
        var holding: Set<String> = []
        for dir in ["Persona", "Shared"] {
            let files = FileManager.default.enumerator(at: apple.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" } ?? []
            for url in files where try String(contentsOf: url, encoding: .utf8).contains("@AppStorage") {
                holding.insert("\(dir)/\(url.path.components(separatedBy: "/\(dir)/").last!)")
            }
        }
        let added = holding.subtracting(Self.remaining)
        #expect(added.isEmpty, "use @StoredValue instead of @AppStorage in \(added.sorted())")
        let done = Self.remaining.subtracting(holding)
        #expect(done.isEmpty, "no @AppStorage left in \(done.sorted()): take them off AppStorageRatchetTests.remaining")
    }
}
