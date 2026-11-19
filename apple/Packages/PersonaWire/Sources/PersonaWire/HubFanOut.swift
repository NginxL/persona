import Foundation

/// Asking every group's hub the same thing (docs/protocol/multi-hub.md): all at once, so a hub out of reach (its requests wait
/// out their 30 s) holds back none of the others (#4705: Machines' numbers, Subscription Usage, the watch, the Live Activity and the
/// pins asked one hub after another).
public nonisolated enum HubFanOut {
    /// The groups worth asking on a poll: those whose link is up. With none up, all of them (nothing to hold back, and
    /// a hub may answer requests while its socket is still coming up).
    public static func asked(_ groups: [String], up: (String) -> Bool) -> [String] {
        let live = groups.filter(up)
        return live.isEmpty ? groups : live
    }

    /// `fetch` for each group at once; `each` gets every answer as it comes, on the main actor, in no fixed order. A
    /// failed group's answer is its error. Cancelling the caller cancels them all.
    ///
    /// On the main actor by name, not by the caller's `#isolation`: the answers came back on a pool thread that way,
    /// and the callers' AppModel reads stopped the app at launch (#4777).
    @MainActor
    public static func run<Scope: Sendable, T: Sendable>(_ groups: [Scope], fetch: @escaping @Sendable (Scope) async throws -> T,
                                                       each: @MainActor (Scope, Result<T, Error>) -> Void) async {
        await withTaskGroup(of: (Scope, Result<T, Error>).self, isolation: MainActor.shared) { tasks in
            for g in groups {
                tasks.addTask {
                    do { return (g, .success(try await fetch(g))) } catch { return (g, .failure(error)) }
                }
            }
            for await (g, result) in tasks { each(g, result) }
        }
    }
}
