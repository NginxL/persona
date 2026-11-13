import Foundation
import Testing
@testable import PersonaKit

/// A streamed reply revealed at an even pace (#6651): nothing lost, nothing early, everything at once when it ends.
struct StreamRevealTests {
    /// A real reply's sends (the markdown trace's first seconds as the node sent them before #6651): bursts 0.1–1.1 s apart.
    static let bursts: [(TimeInterval, Int)] = [(0, 4), (0.94, 38), (1.04, 52), (1.46, 96), (1.67, 101), (1.78, 140),
                                                 (2.41, 182), (2.77, 205), (3.13, 266), (3.94, 300)]
    static let reply = String((0..<300).map { i in Character(UnicodeScalar(0x4E00 + i)!) }) // 300 distinct characters

    /// Replays `bursts` at 60 frames a second; returns (time, shown) at every frame where the shown part changed.
    static func play(_ r: inout StreamReveal, until end: TimeInterval) -> [(TimeInterval, String)] {
        var out: [(TimeInterval, String)] = []
        var next = 0
        var t = 0.0
        while t <= end {
            while next < bursts.count, bursts[next].0 <= t {
                r.receive(String(reply.prefix(bursts[next].1)), at: bursts[next].0)
                next += 1
            }
            if r.advance(to: t) { out.append((t, String(r.shown))) }
            t += 1.0 / 60
        }
        return out
    }

    @Test func neverDropsSkipsOrRunsAhead() {
        var r = StreamReveal(String(Self.reply.prefix(4)))
        let frames = Self.play(&r, until: 6)
        var arrived = 4
        var previous = String(Self.reply.prefix(1))
        for (t, shown) in frames {
            arrived = Self.bursts.last { $0.0 <= t }?.1 ?? arrived
            #expect(Self.reply.hasPrefix(shown)) // in order, nothing skipped
            #expect(shown.count <= arrived) // nothing before it arrived
            #expect(shown.hasPrefix(previous) && shown.count > previous.count) // only grows
            previous = shown
        }
        #expect(r.shown == Self.reply[...]) // everything in the end
        #expect(r.caughtUp)
    }

    /// The point: a few characters at almost every frame instead of a jump per burst, and never far behind.
    @Test func revealsEvenlyAndKeepsUp() {
        var r = StreamReveal(String(Self.reply.prefix(4)))
        let frames = Self.play(&r, until: 6)
        let steps = zip(frames, frames.dropFirst()).map { $1.1.count - $0.1.count }
        let gaps = zip(frames, frames.dropFirst()).map { $1.0 - $0.0 }.sorted()
        #expect(steps.max()! <= 16) // the bursts brought up to 61 characters at once
        #expect(gaps[gaps.count / 2] < 1.5 / 60) // most changes one frame apart
        #expect(frames.count > 100) // 10 bursts became over a hundred small steps
        // Everything that arrived shows soon after: the last burst (3.94 s) is all shown within 1 s (maxLag 0.6 s, then minRate).
        let done = frames.first { $0.1.count == 300 }!.0
        #expect(done < 3.94 + 1.0)
    }

    @Test func finishShowsEverythingAtOnce() {
        var r = StreamReveal(String(Self.reply.prefix(4)))
        r.receive(Self.reply, at: 0.1)
        r.advance(to: 0.2)
        #expect(r.shown.count < 300)
        r.finish()
        #expect(r.shown == Self.reply[...])
        #expect(r.caughtUp)
        let moved = r.advance(to: 0.3)
        #expect(!moved)
    }

    @Test func aLongTextFirstSeenShowsAtOnce() {
        // A chat opened mid-stream: what was written before is not typed out again.
        let r = StreamReveal(Self.reply)
        #expect(r.shown == Self.reply[...])
        #expect(r.caughtUp)
        // A first burst is revealed from its first character.
        let short = StreamReveal("你好，今天")
        #expect(short.shown == "你")
    }

    @Test func aTextThatDoesNotContinueShowsAtOnce() {
        var r = StreamReveal("abc")
        r.receive("abcdefgh", at: 0.1)
        r.advance(to: 0.15)
        r.receive("xyz", at: 0.2)
        #expect(r.shown == "xyz")
        #expect(r.caughtUp)
    }

    @Test func aChangeAfterWhatIsShownKeepsItsPlace() {
        var r = StreamReveal("ab")
        r.receive("abcdefgh", at: 0.1)
        let before = String(r.shown)
        r.receive("abXYZ", at: 0.2) // only what was still to come changed
        #expect(r.shown == before[...])
        #expect(!r.caughtUp)
        var t = 0.2
        while !r.caughtUp, t < 2 { t += 1.0 / 60; r.advance(to: t) }
        #expect(r.shown == "abXYZ")
    }

    /// A pause in the frames (the app was busy, or the frames were stopped while caught up) does not make it jump.
    @Test func aStallResumesAtItsPace() {
        var r = StreamReveal("a")
        r.advance(to: 0)
        r.receive("a" + String(repeating: "b", count: 200), at: 5) // caught up until now: counts from 5, not 0
        r.advance(to: 5.016)
        #expect(r.shown.count < 20)
        r.advance(to: 9) // a 4 s frame counts as 0.1 s
        #expect(r.shown.count < 100)
    }

    /// Emoji and combining marks are whole characters: never cut in half.
    @Test func keepsGraphemesWhole() {
        var r = StreamReveal("👍🏽")
        r.receive("👍🏽e\u{301}👨‍👩‍👧日本", at: 0.1)
        var t = 0.1
        while !r.caughtUp {
            t += 1.0 / 60
            r.advance(to: t)
            #expect("👍🏽e\u{301}👨‍👩‍👧日本".hasPrefix(r.shown))
        }
    }
}
