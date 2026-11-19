#if DEBUG && os(iOS)
import ActivityKit
import Foundation

/// UI tests end the app with SIGTERM, between tests and when a run's test runner exits. A Live Activity it had up (a
/// meeting still recording) outlives the process, and the next run's install stops it: liveactivitiesd then wakes the
/// app in the background to deliver that, without the test's launch arguments, a few milliseconds before the test's own
/// launch, which lands in that process: a page in the system language, nothing the test asked for (#10109,
/// docs/operations/testing.md "Live Activities a run leaves"). So in a Debug build SIGTERM first ends every Live Activity,
/// then exits; a launch that isn't recording ends a recording's activity it finds anyway (`RecordingActivity`), so only
/// this window needed it. Release builds keep the system's default.
@MainActor
enum TerminationActivities {
    /// The signal source, kept for the life of the process.
    private static var source: DispatchSourceSignal?
    /// How long ending may hold the exit: liveactivitiesd answers in milliseconds, and the system escalates a process
    /// that ignores SIGTERM for long.
    private nonisolated static let wait: Duration = .seconds(1)

    static func install() {
        guard source == nil else { return }
        signal(SIGTERM, SIG_IGN) // the source below takes it instead of the default exit
        let s = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
        s.setEventHandler {
            Task { @MainActor in
                await endAll()
                exit(0)
            }
        }
        s.resume()
        source = s
    }

    /// Ends every Live Activity this app has, or gives up after `wait`.
    private static func endAll() async {
        var ends: [Task<Void, Never>] = []
        for a in Activity<RecordingActivityAttributes>.activities { ends.append(end(a)) }
        for a in Activity<PersonaActivityAttributes>.activities { ends.append(end(a)) }
        for a in Activity<LegacyFleetActivityAttributes>.activities { ends.append(end(a)) }
        guard !ends.isEmpty else { return }
        let all = ends
        await withTaskGroup(of: Void.self) { group in
            group.addTask { for e in all { await e.value } }
            group.addTask { try? await Task.sleep(for: wait) }
            await group.next()
            group.cancelAll()
        }
    }

    private static func end<A: ActivityAttributes>(_ activity: Activity<A>) -> Task<Void, Never> {
        nonisolated(unsafe) let a = activity
        return Task { await a.end(nil, dismissalPolicy: .immediate) }
    }
}
#endif
