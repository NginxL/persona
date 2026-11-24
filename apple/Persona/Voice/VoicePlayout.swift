import Foundation

/// The voice's jitter buffer (#4430). The extension relays the upstream's audio as it comes, and a piece that reaches the
/// player after the one before it finished leaves a gap: scheduled the moment it arrived, the voice played a couple of
/// syllables, stopped, and went on. So when nothing is playing, the pieces are held until a lead of them has gathered,
/// or the first has waited long enough (a short reply, the tail of one), and go to the player together; while it plays,
/// each goes straight behind the last. A service that sends faster than it plays fills the lead almost at once.
///
/// The lead grows when the voice stalls (#12922). The measured service sends a reply in 384 ms pieces, about four times
/// faster than it plays, so `lead` costs nothing on a link that keeps up; but a single piece is more than the lead, so
/// on a link that carries a little less than the voice every piece played on its own with a gap after it. A player that
/// runs dry mid-reply (the next piece comes within `stallWindow` of the last one finishing) raises the lead for the rest
/// of the call, step by step (`raisedLeads`): a few longer pauses instead of one per piece, and the next replies start
/// with that much in hand. While a raised lead gathers, what is held plays once nothing more has come for `quiet`, so a
/// short reply or the tail of one does not wait out the whole lead.
///
/// Whether the player ran dry is read off its render position (`VoicePlayerClock`, #13963), never its completions:
/// `.dataPlayedBack` comes an output latency and a hop to the main actor after the player rendered a buffer, longer on a
/// phone than the 25–45 ms gap a link at 90–94 % of the voice leaves after each piece. Such a piece was taken for one
/// joining a voice still playing: it went straight on, the gap was heard, and nothing counted it, so the lead never
/// grew. Less than `floor` left counts as run dry too: the piece would reach the player too late. Each reply's arrival is
/// measured (`VoiceReply`), and one that came slower than it plays raises the lead for the next before it runs dry.
nonisolated struct VoicePlayout<Piece> {
    /// How much to gather before playing, until the voice stalls.
    static var lead: TimeInterval { 0.2 }
    /// The longest the first held piece waits for the rest of `lead`; a raised lead waits as much longer.
    static var wait: TimeInterval { 0.3 }
    /// The leads after the first stall, the second and the third or more.
    static var raisedLeads: [TimeInterval] { [0.6, 1.2, 2.0] }
    /// A piece that comes this soon after the player ran dry is the same reply arriving late, not the next one.
    static var stallWindow: TimeInterval { 1.5 }
    /// While a raised lead gathers, what is held plays once nothing more has come for this long.
    static var quiet: TimeInterval { 1.0 }
    /// Less than this left in the player as a piece comes counts as run dry: the render position it is read from is up to
    /// a render cycle old, and the piece still has to be scheduled.
    static var floor: TimeInterval { 0.05 }

    /// The voice's rate in frames per second.
    let rate: Int
    /// How often the voice stalled, at most the number of `raisedLeads`: the lead in use.
    private(set) var level: Int
    /// How often the voice stalled in all, and how long the player had been dry when the last stall's piece came (0 when
    /// it still had less than `floor` left): for the call's log.
    private(set) var stalls = 0
    private(set) var lastGap: TimeInterval = 0
    private(set) var held: [Piece] = []
    private var heldFrames = 0
    private var firstHeldAt: TimeInterval = 0
    private var lastHeldAt: TimeInterval = 0
    /// The reply arriving now (nil: none since the start or the last `clear`).
    private(set) var reply: VoiceReply?
    /// The reply that ended last, until `takeEnded` hands it on.
    private var ended: VoiceReply?

    /// A new buffer at `rate`, at `level` (a new player for the same call keeps how far its voice stalled).
    init(rate: Int, level: Int = 0) {
        self.rate = rate
        self.level = min(max(level, 0), Self.raisedLeads.count)
    }

    /// How much to gather before playing now.
    var leadNow: TimeInterval { level == 0 ? Self.lead : Self.raisedLeads[level - 1] }

    /// `leadNow` in frames at the voice's rate.
    var leadFrames: Int { max(1, Int(Double(rate) * leadNow)) }

    /// The longest the first held piece waits for the rest of `leadNow`.
    var waitNow: TimeInterval { level == 0 ? Self.wait : Self.wait * leadNow / Self.lead }

    /// Something is held, waiting for the lead or for `due`.
    var isHolding: Bool { !held.isEmpty }

    /// When what is held is to play, lead or not (nil: nothing is held): once the first has waited `waitNow`, and while a
    /// raised lead gathers, once nothing more has come for `quiet`.
    var due: TimeInterval? {
        guard !held.isEmpty else { return nil }
        let waited = firstHeldAt + waitNow
        return level == 0 ? waited : min(waited, lastHeldAt + Self.quiet)
    }

    /// A piece of the voice, `frames` long, arrived at `now`. `left` is what the player still had to play then, in seconds,
    /// read off its render position (`VoicePlayerClock`): negative once it had played everything out, as long ago as that;
    /// nil when it has played nothing since it started or was cleared. What to schedule now, in order (empty while it is
    /// held).
    mutating func add(_ piece: Piece, frames: Int, left: TimeInterval?, at now: TimeInterval) -> [Piece] {
        let seconds = Double(frames) / Double(rate)
        if held.isEmpty, let left, left >= Self.floor {
            arrived(seconds, at: now, left: left)
            return [piece]
        }
        if held.isEmpty {
            if reply != nil, let left, -left < Self.stallWindow {
                // The same reply, late: the player ran dry in the middle of it, or is about to.
                stalls += 1
                lastGap = max(0, -left)
                level = min(level + 1, Self.raisedLeads.count)
                reply?.stalls += 1
                arrived(seconds, at: now, left: left)
            } else {
                endReply()
                reply = VoiceReply(began: now, seconds: seconds)
            }
            firstHeldAt = now
        } else {
            arrived(seconds, at: now, left: nil)
        }
        held.append(piece)
        heldFrames += frames
        lastHeldAt = now
        return heldFrames >= leadFrames ? release() : []
    }

    /// What is held, to play now (`due` came); nothing is held afterwards.
    mutating func release() -> [Piece] {
        defer {
            held = []
            heldFrames = 0
        }
        return held
    }

    /// Drops what is held (`clear`, a new player): the reply arriving is cut off, and the next piece is not taken for one
    /// that came late.
    mutating func clear() {
        held = []
        heldFrames = 0
        endReply(cut: true)
    }

    /// The reply arriving is over (the next began, it was cut off, the call ends), kept for `takeEnded`. One that came
    /// slower than it plays (`VoiceReply.isSlow`) without running dry raises the lead for the next, as a stall would:
    /// at that speed the next one runs dry once its lead is used up.
    mutating func endReply(cut: Bool = false) {
        guard var reply else { return }
        self.reply = nil
        reply.cut = cut
        if reply.stalls == 0, reply.isSlow, level < Self.raisedLeads.count {
            level += 1
            reply.raisedLead = true
        }
        ended = reply
    }

    /// The reply that ended since the last call, once (for the call's log).
    mutating func takeEnded() -> VoiceReply? {
        defer { ended = nil }
        return ended
    }

    private mutating func arrived(_ seconds: TimeInterval, at now: TimeInterval, left: TimeInterval?) {
        if reply == nil {
            reply = VoiceReply(began: now, seconds: seconds)
        } else {
            reply?.add(seconds, at: now, left: left)
        }
    }
}

/// One reply as its pieces arrived (#13963): how fast it came against how fast it plays, and how little the player had
/// left as they came. The call's log has a line for each; `VoicePlayout` raises the lead after a slow one.
nonisolated struct VoiceReply: Equatable, Sendable {
    /// Below this speed, measured over at least `measureSpan`, a reply came slower than it plays. Not 1: an upstream that
    /// sends at the voice's own pace measures a few percent either side of it, and its lead should not grow reply by reply.
    static let slow = 0.95
    /// A reply that came within this is too quick to measure: its speed would be its first pieces' burst.
    static let measureSpan: TimeInterval = 1.5

    /// When its first and its last piece came.
    var began: TimeInterval
    var last: TimeInterval
    /// The voice it brought, in seconds, and its last piece's length.
    var seconds: TimeInterval
    var lastPiece: TimeInterval
    /// The least the player had left as one of its pieces came while nothing was held (nil: none did); 0 when it had run
    /// dry.
    var lowest: TimeInterval?
    /// How often the player ran dry in it.
    var stalls = 0
    /// It was cut off (`clear`, a new player) rather than over.
    var cut = false
    /// It came so slowly that the lead was raised for the next one.
    var raisedLead = false

    init(began: TimeInterval, seconds: TimeInterval) {
        self.began = began
        last = began
        self.seconds = seconds
        lastPiece = seconds
    }

    /// How long its pieces took to come, first to last.
    var span: TimeInterval { last - began }

    /// How fast it came against how fast it plays (1: as fast; 4: four times faster), up to its last piece; nil when it
    /// came within `measureSpan`.
    var speed: Double? { span >= Self.measureSpan ? (seconds - lastPiece) / span : nil }

    /// It came slower than it plays (`slow`).
    var isSlow: Bool { speed.map { $0 < Self.slow } ?? false }

    /// One more piece, `piece` seconds long, at `now`, with `left` in the player (nil: not measured, as while held).
    mutating func add(_ piece: TimeInterval, at now: TimeInterval, left: TimeInterval?) {
        last = now
        seconds += piece
        lastPiece = piece
        if let left { lowest = min(lowest ?? .infinity, max(0, left)) }
    }

    /// Its line in the call's log, with the lead the next reply gathers: numbers only, nothing that was said.
    func logLine(lead: TimeInterval) -> String {
        func ms(_ t: TimeInterval) -> String { "\(Int((t * 1000).rounded())) ms" }
        let pace = speed.map { String(format: "%.2fx its pace", $0) } ?? "too quickly to measure"
        return "a reply of \(ms(seconds)) came in \(ms(span)) (\(pace)); the player's lowest cushion \(lowest.map(ms) ?? "not measured"), "
            + "ran dry \(stalls) times, \(cut ? "cut off" : "over"); the next gathers \(ms(lead))"
            + (raisedLead ? ", raised as it came slower than it plays" : "")
    }
}

/// Where what was scheduled on a player ends on the player's own timeline (#13963), so what it still has to play is read
/// off its render position as it renders, not an output latency later from its completions. The player's timeline starts
/// at zero when it starts playing and goes on through silence; a buffer scheduled while it has nothing left plays as soon
/// as it can. Every buffer counts, the app's own cues too: the voice plays behind them.
nonisolated struct VoicePlayerClock: Equatable, Sendable {
    /// The end of what was scheduled, in seconds on the player's timeline (nil: nothing since it started).
    private(set) var end: TimeInterval?

    /// What is left to play with the player rendered up to `rendered` (seconds on its timeline; nil: not yet): negative
    /// once it played everything out, as long ago as that; nil when nothing was scheduled since it started.
    func left(rendered: TimeInterval?) -> TimeInterval? {
        guard let end else { return nil }
        return end - (rendered ?? 0)
    }

    /// A buffer `seconds` long went onto the player, rendered up to `rendered`: it plays after what is left.
    mutating func scheduled(_ seconds: TimeInterval, rendered: TimeInterval?) {
        end = max(end ?? 0, rendered ?? 0) + seconds
    }

    /// The player stopped (`clear`, a new player): its timeline starts again at zero.
    mutating func restart() {
        end = nil
    }
}
