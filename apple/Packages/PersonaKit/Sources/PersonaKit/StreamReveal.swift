import Foundation

/// How much of a streamed reply to show at each frame (#6651). The text arrives in bursts: the model's output comes a
/// few dozen characters at a time about every 350 ms (up to 2 s apart), and drawing each burst as it came made the
/// reply jump. This reveals what has arrived at an even pace instead: a little slower than the stream's average so far
/// (`paceShare`, so the reveal is still going when the next burst comes), faster when more is waiting (characters
/// waiting drain at waiting / `maxLag` a second), and never slower than `minRate`. Replayed on recorded replies, it left
/// about a fifth of the pauses over 100 ms, each character shown about 0.5 s after it arrived (p95 1 s; #6651).
///
/// It never shows text that has not arrived, never skips or reorders characters, and `finish()` shows everything at
/// once. A text that does not continue what is shown (the stream was replaced) shows at once too.
public nonisolated struct StreamReveal: Sendable, Equatable {
    /// Characters a second at the least.
    public static let minRate = 20.0
    /// The share of the stream's average rate the reveal keeps to.
    public static let paceShare = 0.8
    /// The waiting characters drain at least at waiting / maxLag a second.
    public static let maxLag: TimeInterval = 0.6
    /// The average counts once the stream has run this long.
    public static let averageAfter: TimeInterval = 0.5
    /// A frame counts for at most this long: after a stall the reveal resumes at its pace instead of jumping.
    public static let maxStep: TimeInterval = 0.1
    /// A first text up to this long is revealed from its start; a longer one (a chat opened mid-stream) shows at once.
    public static let revealFirst = 80

    /// Everything that has arrived.
    public private(set) var text: String
    /// Where the shown part ends, in UTF-8 bytes of `text` (an offset stays valid when `text` grows).
    private var shownBytes: Int
    /// When the stream's first growth came, and the characters that came since.
    private var firstArrival: TimeInterval?
    private var arrived = 0
    private var lastFrame: TimeInterval?
    /// Characters owed to the next frames (fractions of one carry over).
    private var budget = 0.0

    public init(_ text: String = "") {
        self.text = text
        // utf8 first: a long reply is never counted (a view may build this at every redraw).
        let short = text.utf8.count <= Self.revealFirst * 4 && text.count <= Self.revealFirst
        let start = short ? text.index(text.startIndex, offsetBy: 1, limitedBy: text.endIndex) : nil
        shownBytes = text.utf8.distance(from: text.startIndex, to: start ?? text.endIndex)
    }

    private var shownEnd: String.Index { text.utf8.index(text.startIndex, offsetBy: shownBytes) }

    /// What to draw now.
    public var shown: Substring { text[..<shownEnd] }

    /// Everything that arrived is shown: no frames are needed until more comes.
    public var caughtUp: Bool { shownBytes >= text.utf8.count }

    /// The stream has a new text (the whole reply so far) at time `t` (seconds, the frames' clock).
    public mutating func receive(_ new: String, at t: TimeInterval) {
        guard new != text else { return }
        if caughtUp { lastFrame = t } // the frames were paused: the next one counts from now
        let shownPart = shown
        if new.hasPrefix(text) {
            if firstArrival == nil {
                firstArrival = t
            } else {
                arrived += new[new.utf8.index(new.startIndex, offsetBy: text.utf8.count)...].count
            }
            text = new
        } else if new.hasPrefix(shownPart) {
            text = new // what is shown stays; only what was still to come changed
        } else {
            text = new // not a continuation: no reveal of something already read
            finish()
        }
    }

    /// The frame at time `t`: moves the shown part on. True when what is shown changed.
    @discardableResult
    public mutating func advance(to t: TimeInterval) -> Bool {
        defer { lastFrame = t }
        guard !caughtUp else { budget = 0; return false }
        let dt = min(Self.maxStep, max(0, t - (lastFrame ?? t)))
        let end = shownEnd
        let waiting = text[end...].count
        let pace = firstArrival.map { t - $0 > Self.averageAfter ? Double(arrived) / (t - $0) * Self.paceShare : 0 } ?? 0
        budget += max(Self.minRate, pace, Double(waiting) / Self.maxLag) * dt
        let n = min(Int(budget), waiting)
        guard n > 0 else { return false }
        budget -= Double(n)
        let next = text.index(end, offsetBy: n)
        shownBytes = text.utf8.distance(from: text.startIndex, to: next)
        if caughtUp { budget = 0 }
        return true
    }

    /// The stream is over: everything at once.
    public mutating func finish() {
        shownBytes = text.utf8.count
        budget = 0
    }
}
