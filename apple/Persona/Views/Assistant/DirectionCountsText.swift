import Foundation

/// What a direction's row says in words (#4331, #6626): "N need you" while its to-dos wait on you, nothing otherwise.
/// What runs under it is the row's activity mark alone (`DirectionTree.Direction.running`), never "N working" as well:
/// one meaning, one expression.
nonisolated enum DirectionCountsText {
    /// The words, or nil when nothing waits on you.
    static func label(needsYou: Int) -> String? {
        needsYou > 0 ? String(localized: "\(needsYou) need you") : nil
    }
}
