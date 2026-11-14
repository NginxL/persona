import Foundation
import Testing

/// Both production call sites keep their existing IDs but use the same visibility-scoped task as `MeetingPage`.
/// `TabActiveTaskTests` hosts generic cancellation and restart; these pins avoid refactoring private page methods.
struct RecordingMinutesPollingSourceTests {
    private static let recording = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent()
        .deletingLastPathComponent()
        .appending(path: "Persona/Recording")

    private func modifiers(_ file: String) throws -> [String] {
        try String(contentsOf: Self.recording.appending(path: file), encoding: .utf8)
            .split(separator: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
    }

    @Test func recordingPageUsesTabActiveMinutesFollow() throws {
        #expect(try modifiers("RecordingsSection.swift").contains(
            #".tabActiveTask(id: "\(library.entry(id)?.revision ?? 0)|\(minutes.round)") { await followMinutes() }"#))
    }

    @Test func wrapUpUsesTabActiveMinutesFollow() throws {
        #expect(try modifiers("RecordingWrapUp.swift").contains(
            #".tabActiveTask(id: "\(entry.revision)|\(minutes.round)|\(entry.transcribedAll)") { await followMinutes() }"#))
    }
}
