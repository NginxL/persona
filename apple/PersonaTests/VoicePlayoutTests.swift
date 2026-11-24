import Foundation
import Testing
@testable import Persona

/// The voice's jitter buffer (#4430): nothing plays until the lead has gathered or the wait ran out, then everything held
/// goes at once; while the voice plays, each piece goes straight behind it; a player that ran dry gathers again, and a
/// player that ran dry mid-reply gathers more from then on (#12922). Whether it ran dry is read off the player's render
/// position, never its completions, which come an output latency late (#13963).
struct VoicePlayoutTests {
    /// 24 kHz: the lead is 4800 frames, a 40 ms piece 960.
    private let piece = 960
    /// The measured service's piece: 384 ms at 16 kHz.
    private let servicePiece = 6144

    @Test func theLeadIsTwoHundredMillisecondsAtTheVoicesRate() {
        #expect(VoicePlayout<Int>(rate: 24000).leadFrames == 4800)
        #expect(VoicePlayout<Int>(rate: 16000).leadFrames == 3200)
        #expect(VoicePlayout<Int>(rate: 16000).waitNow == VoicePlayout<Int>.wait)
        #expect(VoicePlayout<Int>.wait > VoicePlayout<Int>.lead, "a voice sent as fast as it plays fills the lead before the wait runs out")
        #expect(VoicePlayout<Int>.raisedLeads == VoicePlayout<Int>.raisedLeads.sorted())
        #expect(VoicePlayout<Int>.raisedLeads.first! > VoicePlayout<Int>.lead)
        #expect(VoicePlayout<Int>.floor < VoicePlayout<Int>.lead)
    }

    @Test func piecesAreHeldUntilTheLeadThenPlayTogetherInOrder() {
        var playout = VoicePlayout<Int>(rate: 24000)
        for n in 1...4 {
            #expect(playout.add(n, frames: piece, left: nil, at: 0).isEmpty)
            #expect(playout.isHolding)
        }
        #expect(playout.add(5, frames: piece, left: nil, at: 0) == [1, 2, 3, 4, 5])
        #expect(!playout.isHolding)
    }

    @Test func oneLongPieceFromAFastServicePlaysAtOnce() {
        var playout = VoicePlayout<Int>(rate: 24000)
        #expect(playout.add(1, frames: 24000, left: nil, at: 0) == [1])
        #expect(!playout.isHolding)
    }

    @Test func whileTheVoicePlaysEachPieceGoesStraightBehindIt() {
        var playout = VoicePlayout<Int>(rate: 24000)
        #expect(playout.add(1, frames: piece, left: 0.5, at: 0) == [1])
        #expect(playout.add(2, frames: piece, left: 0.54, at: 0) == [2])
        #expect(!playout.isHolding)
    }

    @Test func aPlayerThatRanDryGathersAgain() {
        var playout = VoicePlayout<Int>(rate: 24000)
        #expect(playout.add(1, frames: 4800, left: nil, at: 0) == [1])
        #expect(playout.add(2, frames: piece, left: 0.2, at: 0) == [2])
        // The player caught up with the stream: the next piece waits for a lead of its own.
        #expect(playout.add(3, frames: piece, left: nil, at: 0).isEmpty)
        #expect(playout.add(4, frames: 3840, left: nil, at: 0) == [3, 4])
    }

    @Test func aShortReplyPlaysWhenTheWaitRunsOut() {
        var playout = VoicePlayout<Int>(rate: 24000)
        #expect(playout.due == nil)
        #expect(playout.add(1, frames: piece, left: nil, at: 10).isEmpty)
        #expect(playout.add(2, frames: piece, left: nil, at: 10.1).isEmpty)
        #expect(playout.due == 10 + VoicePlayout<Int>.wait, "from the first held piece, at the first lead")
        #expect(playout.release() == [1, 2])
        #expect(!playout.isHolding)
        #expect(playout.due == nil)
        #expect(playout.release().isEmpty)
        // The count starts over: the next reply gathers a whole lead again.
        #expect(playout.add(3, frames: 3840, left: nil, at: 20).isEmpty)
    }

    @Test func clearDropsWhatIsHeld() {
        var playout = VoicePlayout<Int>(rate: 24000)
        #expect(playout.add(1, frames: 3840, left: nil, at: 0).isEmpty)
        playout.clear()
        #expect(!playout.isHolding)
        #expect(playout.add(2, frames: piece, left: nil, at: 0).isEmpty, "what was cleared does not count toward the lead")
    }

    // MARK: The lead grows when the voice stalls (#12922)

    @Test func aPlayerThatRanDryMidReplyGathersALongerLead() {
        var playout = VoicePlayout<Int>(rate: 16000)
        // One of the service's pieces is more than the first lead: it plays at once.
        #expect(playout.add(1, frames: servicePiece, left: nil, at: 0) == [1])
        // The next piece came 116 ms after the player ran dry: the link falls behind the voice.
        #expect(playout.add(2, frames: servicePiece, left: 0.384 - 0.5, at: 0.5).isEmpty, "held: one piece is less than the raised lead")
        #expect(playout.level == 1)
        #expect(playout.stalls == 1)
        #expect(abs(playout.lastGap - 0.116) < 0.000_1)
        #expect(playout.leadNow == VoicePlayout<Int>.raisedLeads[0])
        #expect(playout.leadFrames == Int(16000 * VoicePlayout<Int>.raisedLeads[0]))
        #expect(playout.add(3, frames: servicePiece, left: 0.384 - 0.9, at: 0.9) == [2, 3])
    }

    @Test func theNextReplyIsNotAStall() {
        var playout = VoicePlayout<Int>(rate: 16000)
        #expect(playout.add(1, frames: servicePiece, left: nil, at: 0) == [1])
        // The next reply came after the user spoke: the lead stays as it was.
        let now = 0.384 + VoicePlayout<Int>.stallWindow + 0.01
        #expect(playout.add(2, frames: servicePiece, left: 0.384 - now, at: now) == [2])
        #expect(playout.level == 0)
        #expect(playout.stalls == 0)
    }

    @Test func morePlayingForgetsThatThePlayerRanDry() {
        var playout = VoicePlayout<Int>(rate: 16000)
        #expect(playout.add(1, frames: servicePiece, left: nil, at: 0) == [1])
        // A cue went on behind it: the player still had some of that to play as the next piece came.
        #expect(playout.add(2, frames: servicePiece, left: 0.3, at: 0.4) == [2], "it still plays")
        #expect(playout.add(3, frames: servicePiece, left: 0.584, at: 0.5) == [3])
        #expect(playout.level == 0)
    }

    @Test func clearIsNotAStall() {
        var playout = VoicePlayout<Int>(rate: 16000)
        #expect(playout.add(1, frames: servicePiece, left: nil, at: 0) == [1])
        playout.clear() // the user cut in: the player starts again, and the next piece is the next reply
        #expect(playout.add(2, frames: servicePiece, left: nil, at: 0.5) == [2])
        #expect(playout.level == 0)
    }

    @Test func theLeadStopsGrowingAtTheLastStep() {
        var playout = VoicePlayout<Int>(rate: 16000)
        var now: TimeInterval = 0
        var left: TimeInterval?
        for _ in 0..<(VoicePlayout<Int>.raisedLeads.count + 2) {
            _ = playout.add(0, frames: 16000 * 3, left: left, at: now)
            now += 3.1
            left = -0.1 // three seconds played, then 100 ms of nothing before the next piece
        }
        #expect(playout.level == VoicePlayout<Int>.raisedLeads.count)
        #expect(playout.leadNow == VoicePlayout<Int>.raisedLeads.last)
        #expect(playout.stalls == VoicePlayout<Int>.raisedLeads.count + 1)
        #expect(VoicePlayout<Int>(rate: 16000, level: 99).level == VoicePlayout<Int>.raisedLeads.count)
        #expect(VoicePlayout<Int>(rate: 16000, level: -1).level == 0)
    }

    @Test func aRaisedLeadPlaysWhatIsHeldOnceTheStreamIsQuiet() {
        var playout = VoicePlayout<Int>(rate: 16000, level: VoicePlayout<Int>.raisedLeads.count)
        let wait = VoicePlayout<Int>.wait * VoicePlayout<Int>.raisedLeads.last! / VoicePlayout<Int>.lead
        #expect(playout.waitNow == wait)
        #expect(playout.add(1, frames: piece, left: nil, at: 0).isEmpty)
        #expect(playout.due == VoicePlayout<Int>.quiet, "a short reply waits for the stream to go quiet, not the whole lead")
        #expect(playout.add(2, frames: piece, left: nil, at: 0.5).isEmpty)
        #expect(playout.due == 0.5 + VoicePlayout<Int>.quiet)
        // Pieces that keep coming are gathered for the lead, but no longer than the wait from the first.
        var now = 0.5
        while now + 0.9 < wait {
            now += 0.9
            #expect(playout.add(3, frames: piece, left: nil, at: now).isEmpty)
        }
        #expect(playout.due == min(wait, now + VoicePlayout<Int>.quiet))
    }

    @Test func aNewPlayerForTheSameCallKeepsTheRaisedLead() {
        let playout = VoicePlayout<Int>(rate: 24000, level: 2)
        #expect(playout.leadNow == VoicePlayout<Int>.raisedLeads[1])
        #expect(playout.leadFrames == Int(24000 * VoicePlayout<Int>.raisedLeads[1]))
        #expect(!playout.isHolding)
    }

    // MARK: Ran dry, by the render position (#13963)

    /// A piece that reaches a player with less than `floor` left would come too late: the player is as good as dry.
    @Test func lessThanTheFloorLeftIsAStall() {
        var playout = VoicePlayout<Int>(rate: 16000)
        #expect(playout.add(1, frames: servicePiece, left: nil, at: 0) == [1])
        #expect(playout.add(2, frames: servicePiece, left: 0.03, at: 0.354).isEmpty, "held: 30 ms left is no cushion")
        #expect(playout.stalls == 1)
        #expect(playout.lastGap == 0, "it had not gone silent yet")
        #expect(playout.level == 1)
    }

    @Test func aCueBeforeTheFirstPieceIsNoStall() {
        var playout = VoicePlayout<Int>(rate: 24000)
        // The ready tone is nearly over as the voice's first piece comes: a new reply, held for the lead.
        #expect(playout.add(1, frames: piece, left: 0.02, at: 0).isEmpty)
        #expect(playout.stalls == 0)
        #expect(playout.level == 0)
    }

    @Test func theClockReadsWhatIsLeftOffTheRenderPosition() {
        var clock = VoicePlayerClock()
        #expect(clock.left(rendered: 0.5) == nil, "nothing scheduled since the player started")
        clock.scheduled(0.4, rendered: nil) // before its first render cycle: from the start of its timeline
        #expect(clock.left(rendered: nil) == 0.4)
        #expect(abs(clock.left(rendered: 0.1)! - 0.3) < 1e-9)
        clock.scheduled(0.4, rendered: 0.1) // behind what is left
        #expect(abs(clock.end! - 0.8) < 1e-9)
        #expect(abs(clock.left(rendered: 1.0)! + 0.2) < 1e-9, "played out 200 ms ago")
        clock.scheduled(0.4, rendered: 1.0) // nothing left: as soon as it can
        #expect(abs(clock.end! - 1.4) < 1e-9)
        clock.restart()
        #expect(clock.left(rendered: 0) == nil)
    }

    // MARK: Each reply as it arrived (#13963)

    @Test func eachReplyIsMeasuredAsItArrives() {
        var playout = VoicePlayout<Int>(rate: 16000)
        let length = Double(servicePiece) / 16000
        let step = length / 0.8
        var end = 0.0
        var now = 0.0
        // Six pieces at 80 % of the voice's pace, each scheduled straight behind the last while the player has some left.
        for n in 0..<6 {
            let left: TimeInterval? = n == 0 ? nil : end - now
            let ready = playout.add(n, frames: servicePiece, left: left, at: now)
            if !ready.isEmpty { end = max(end, now) + Double(ready.count) * length }
            now += step
        }
        #expect(playout.takeEnded() == nil, "it is still arriving")
        let reply = playout.reply!
        #expect(abs(reply.seconds - 6 * length) < 1e-9)
        #expect(abs(reply.span - 5 * step) < 1e-9)
        #expect(abs(reply.speed! - 0.8) < 1e-9)
        #expect(reply.isSlow)
        #expect(reply.stalls == playout.stalls)
        #expect(reply.lowest == 0, "it ran dry")
        playout.clear()
        let ended = playout.takeEnded()!
        #expect(ended.cut)
        #expect(!ended.raisedLead, "it ran dry, which already raised the lead")
        #expect(playout.takeEnded() == nil, "once")
        let line = ended.logLine(lead: playout.leadNow)
        #expect(line.contains("0.80x its pace"))
        #expect(line.contains("cut off"))
    }

    @Test func aReplyTooQuickToMeasureHasNoSpeed() {
        var reply = VoiceReply(began: 0, seconds: 0.384)
        reply.add(0.384, at: 0.1, left: 0.6)
        #expect(reply.speed == nil)
        #expect(!reply.isSlow)
        #expect(reply.lowest == 0.6)
        #expect(reply.logLine(lead: 0.2).contains("too quickly to measure"))
    }

    // MARK: Replays of a link that falls behind the voice

    /// A link that carries 94 % of the voice's rate, from the first piece: with the fixed lead every piece played on its
    /// own with a gap after it (the reported "a syllable or two at a time"); with the growing lead, a couple of pauses.
    /// The player's output latency (50 ms) does not hide the gaps from the render position.
    @Test func aLinkSlightlySlowerThanTheVoicePausesTwiceNotOncePerPiece() {
        let step: TimeInterval = Double(servicePiece) / 16000 / 0.94
        let arrivals: [TimeInterval] = (0..<40).map { Double($0) * step }
        let fixed = replay(arrivals, frames: servicePiece, rate: 16000, signal: .none)
        #expect(fixed.gaps.count == arrivals.count - 1)
        let growing = replay(arrivals, frames: servicePiece, rate: 16000)
        #expect(growing.gaps.count <= 2)
        #expect(growing.level == 2)
    }

    /// The first second and a half comes at once, then the link carries 60 % of the voice: smooth at first, then a gap
    /// after every piece; with the growing lead, a few longer pauses.
    @Test func aLinkThatFallsFarBehindAfterTheFirstBurstPausesAFewTimes() {
        let step: TimeInterval = Double(servicePiece) / 16000 / 0.6
        let burst: [TimeInterval] = (0..<5).map { Double($0) * 0.06 }
        let behind: [TimeInterval] = (1...30).map { (n: Int) -> TimeInterval in step * Double(n) + 0.3 }
        let arrivals = burst + behind
        let fixed = replay(arrivals, frames: servicePiece, rate: 16000, signal: .none)
        #expect(fixed.gaps.count >= 25)
        let growing = replay(arrivals, frames: servicePiece, rate: 16000)
        #expect(growing.gaps.count <= 4)
        #expect(growing.level == VoicePlayout<Int>.raisedLeads.count)
    }

    /// The measured service sends about four times faster than it plays: no gap, and the lead never grows.
    @Test func aLinkThatKeepsUpNeverGrowsTheLead() {
        let step: TimeInterval = Double(servicePiece) / 16000 / 4
        let arrivals: [TimeInterval] = (0..<40).map { Double($0) * step }
        let replayed = replay(arrivals, frames: servicePiece, rate: 16000)
        #expect(replayed.gaps.isEmpty)
        #expect(replayed.level == 0)
        #expect(abs((replayed.replies.first?.speed ?? 0) - 4) < 0.01)
    }

    /// #12923 judged "the player ran dry" by its completions. They come an output latency after the player rendered the
    /// buffer, longer than the 24.5 ms gap a 94 % link leaves after each piece: every piece came while the last one's
    /// completion was still on its way, went straight on behind a player already silent, and no gap was counted.
    @Test func completionsComeTooLateToSeeTheGaps() {
        let step: TimeInterval = Double(servicePiece) / 16000 / 0.94
        let arrivals: [TimeInterval] = (0..<40).map { Double($0) * step }
        let atOnce = replay(arrivals, frames: servicePiece, rate: 16000, signal: .completions, latency: 0)
        #expect(atOnce.gaps.count <= 2, "with no output latency they were in time")
        let late = replay(arrivals, frames: servicePiece, rate: 16000, signal: .completions, latency: 0.05)
        #expect(late.gaps.count == arrivals.count - 1)
        #expect(late.stalls == 0)
        #expect(late.level == 0)
    }

    /// The render position sees each gap however late the completions come: at 94 % and at 90 % (a 30 s reply), with 50 to
    /// 200 ms of output latency, the lead grows and the voice pauses a few times instead of after every piece.
    @Test func theRenderPositionSeesTheGapsWhateverTheOutputLatency() {
        let step94: TimeInterval = Double(servicePiece) / 16000 / 0.94
        let arrivals94: [TimeInterval] = (0..<40).map { Double($0) * step94 }
        let step90: TimeInterval = Double(servicePiece) / 16000 / 0.9
        let arrivals90: [TimeInterval] = (0..<78).map { Double($0) * step90 }
        for latency: TimeInterval in [0.05, 0.1, 0.2] {
            let at94 = replay(arrivals94, frames: servicePiece, rate: 16000, latency: latency)
            #expect(at94.gaps.count <= 2)
            #expect(at94.level == 2)
            let blind = replay(arrivals90, frames: servicePiece, rate: 16000, signal: .completions, latency: latency)
            #expect(blind.gaps.count == arrivals90.count - 1)
            let at90 = replay(arrivals90, frames: servicePiece, rate: 16000, latency: latency)
            #expect(at90.gaps.count <= 3)
            #expect(at90.level == VoicePlayout<Int>.raisedLeads.count)
        }
    }

    /// A reply that came at 92 % of the voice's pace in short pieces, with a raised lead in hand: it never ran dry, but the
    /// next one would once its lead is used up, so it starts with a longer lead.
    @Test func aReplyThatCameSlowerThanItPlaysRaisesTheLeadForTheNext() {
        let frames = 320 // 20 ms at 16 kHz
        let step: TimeInterval = Double(frames) / 16000 / 0.92
        let first: [TimeInterval] = (0..<200).map { Double($0) * step }
        let second: [TimeInterval] = (0..<50).map { first.last! + 3 + Double($0) * step }
        let replayed = replay(first + second, frames: frames, rate: 16000, level: 1)
        #expect(replayed.stalls == 0)
        #expect(replayed.replies.count == 2)
        #expect(abs((replayed.replies[0].speed ?? 0) - 0.92) < 0.01)
        #expect(replayed.replies[0].raisedLead)
        #expect(replayed.level == 2, "the second reply gathered the next lead")
    }

    /// An upstream that sends at the voice's own pace, a little early or late: no reply counts as slow, and the lead stays.
    @Test func aReplyAtTheVoicesOwnPaceKeepsTheLead() {
        let frames = 640 // 40 ms at 16 kHz
        let jitter: [TimeInterval] = [0, 0.03, 0.01, 0.025, 0.005]
        var arrivals: [TimeInterval] = []
        var start: TimeInterval = 0
        for _ in 0..<4 {
            let reply: [TimeInterval] = (0..<100).map { (n: Int) -> TimeInterval in start + Double(n) * 0.04 + jitter[n % jitter.count] }
            arrivals += reply
            start = reply.last! + 3
        }
        let replayed = replay(arrivals, frames: frames, rate: 16000)
        #expect(replayed.stalls == 0)
        #expect(replayed.level == 0)
        #expect(replayed.replies.count == 4)
        #expect(replayed.replies.allSatisfy { !$0.isSlow && !$0.raisedLead })
    }

    /// What tells the playout how much the player has left.
    private enum Signal {
        /// Never when it ran dry: the playout before #12922.
        case none
        /// The player's completions (#12923): a buffer counts as played out once its completion comes, `latency` after
        /// the player rendered it.
        case completions
        /// The player's render position (#13963), read off its last render cycle (`io`), as `VoicePlayerClock` does.
        case renderPosition
    }

    private struct Replay {
        var gaps: [TimeInterval]
        var level: Int
        var stalls: Int
        var replies: [VoiceReply]
    }

    /// Plays pieces of `frames` arriving at `arrivals` through a playout and a player that plays what it is given back to
    /// back, its timeline starting at 0: the gaps where the player had run dry, the playout's level and stalls at the end,
    /// and each reply as it arrived. The playout learns how much the player has left by `signal`; the player's output
    /// latency delays its completions, and its render position is read once per `io`.
    private func replay(_ arrivals: [TimeInterval], frames: Int, rate: Int, signal: Signal = .renderPosition,
                        latency: TimeInterval = 0.05, io: TimeInterval = 0.02, level: Int = 0) -> Replay {
        var playout = VoicePlayout<Int>(rate: rate, level: level)
        var clock = VoicePlayerClock()
        let length = Double(frames) / Double(rate)
        var playEnd: TimeInterval?
        var gaps: [TimeInterval] = []
        var replies: [VoiceReply] = []
        var completions: [TimeInterval] = []
        var lastDone: TimeInterval?
        func rendered(_ now: TimeInterval) -> TimeInterval { (now / io + 1e-9).rounded(.down) * io }
        func schedule(_ pieces: [Int], at now: TimeInterval) {
            guard !pieces.isEmpty else { return }
            if let end = playEnd, end < now { gaps.append(now - end) }
            for _ in pieces {
                clock.scheduled(length, rendered: rendered(now))
                let end = max(now, playEnd ?? now) + length
                playEnd = end
                completions.append(end + latency)
            }
        }
        func left(at now: TimeInterval) -> TimeInterval? {
            switch signal {
            case .none:
                return (playEnd ?? -1) > now ? 1 : nil
            case .completions:
                if let done = completions.filter({ $0 <= now }).max() { lastDone = done }
                completions.removeAll { $0 <= now }
                return completions.isEmpty ? lastDone.map { $0 - now } : 1
            case .renderPosition:
                return clock.left(rendered: rendered(now))
            }
        }
        for (n, now) in arrivals.enumerated() {
            if let due = playout.due, due <= now { schedule(playout.release(), at: due) }
            schedule(playout.add(n, frames: frames, left: left(at: now), at: now), at: now)
            if let reply = playout.takeEnded() { replies.append(reply) }
        }
        if let due = playout.due { schedule(playout.release(), at: due) }
        let atEnd = playout.level
        playout.endReply()
        if let reply = playout.takeEnded() { replies.append(reply) }
        return Replay(gaps: gaps, level: atEnd, stalls: playout.stalls, replies: replies)
    }
}
