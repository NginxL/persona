import QuartzCore
import SwiftUI
import Testing
import UIKit
@testable import Persona

@MainActor @Suite(.serialized) struct PersonaFaceMotionTests {
    private func face(_ state: PersonaState, size: CGFloat = 46) -> PersonaFace {
        PersonaFace(size: size, design: .mochi, state: state, moving: true)
    }

    @Test func onlyMovingStatesWithMotionAndVisibilityEnterTheHost() {
        for state in PersonaState.allCases {
            let movingState = [.thinking, .working, .needsYou].contains(state)
            #expect(AssistantAvatar.moves(state: state, still: false, reduceMotion: false, eligible: true) == movingState)
            #expect(!AssistantAvatar.moves(state: state, still: true, reduceMotion: false, eligible: true), "\(state): still")
            #expect(!AssistantAvatar.moves(state: state, still: false, reduceMotion: true, eligible: true), "\(state): Reduce Motion")
            #expect(!AssistantAvatar.moves(state: state, still: true, reduceMotion: true, eligible: true), "\(state): held")
            #expect(!AssistantAvatar.moves(state: state, still: false, reduceMotion: false, eligible: false), "\(state): ineligible")
        }
    }

    @Test func stillScheduleEmitsOnlyItsInitialDate() {
        let start = Date(timeIntervalSinceReferenceDate: 1234)
        var entries = AssistantAvatar.stillSchedule.entries(from: start, mode: .normal)
        #expect(entries.next() == start)
        #expect(entries.next() == nil)
    }

    @Test func animationsUseTheSharedFunctionsAndWallPhase() throws {
        let working = face(.working), needsYou = face(.needsYou), thinking = face(.thinking)
        let wall: TimeInterval = 123.25

        let arc = PersonaMotionAnimations.arc(face: working, wallTime: wall)
        #expect(arc.keyPath == "transform.rotation.z")
        #expect(arc.duration == PersonaMotionAnimations.arcPeriod)
        #expect((arc.fromValue as? NSNumber)?.doubleValue == 0)
        #expect((arc.toValue as? NSNumber)?.doubleValue == 2 * Double.pi)
        #expect(abs(arc.beginTime - -(wall.truncatingRemainder(dividingBy: arc.duration))) < 0.000_001)
        #expect(arc.repeatCount == .infinity)
        #expect(arc.preferredFrameRateRange.preferred == 15)

        let eyes = PersonaMotionAnimations.eyes(face: working, wallTime: wall)
        #expect(eyes.keyPath == "transform.translation.x")
        #expect(eyes.duration == PersonaMotionAnimations.eyesPeriod)
        let bob = PersonaMotionAnimations.bob(face: working, wallTime: wall)
        #expect(bob.keyPath == "transform.translation.y")
        #expect(bob.duration == PersonaMotionAnimations.bobPeriod)
        let rock = PersonaMotionAnimations.rock(face: needsYou, wallTime: wall)
        #expect(rock.keyPath == "transform.rotation.z")
        #expect(rock.duration == PersonaMotionAnimations.rockPeriod)
        let ring = PersonaMotionAnimations.ring(face: needsYou, wallTime: wall)
        #expect(ring.keyPath == "opacity")
        #expect(ring.duration == PersonaMotionAnimations.ringPeriod)

        for animation in [eyes, bob, rock, ring] {
            let values = try #require(animation.values as? [NSNumber])
            let keyTimes = try #require(animation.keyTimes)
            #expect(values.count == keyTimes.count)
            #expect(keyTimes.first?.doubleValue == 0 && keyTimes.last?.doubleValue == 1)
            #expect(animation.calculationMode == .linear)
            #expect(animation.repeatCount == .infinity)
            #expect(animation.preferredFrameRateRange.preferred == 15)
            #expect(abs(animation.beginTime - -(wall.truncatingRemainder(dividingBy: animation.duration))) < 0.000_001)
        }
        // Every sampled point is the actual drawing function, not a substitute eased curve.
        for (animation, value) in [
            (eyes, { Double(working.mochiEyeLook($0).width - working.mochiEyeLook(0).width) }),
            (bob, { Double(working.mochiBob($0)) }),
            (rock, { needsYou.mochiRock($0) * .pi / 180 }),
            (ring, { needsYou.needsYouPulse($0) }),
        ] {
            let values = try #require(animation.values as? [NSNumber])
            let times = try #require(animation.keyTimes)
            for (actual, keyTime) in zip(values, times) {
                #expect(abs(actual.doubleValue - value(keyTime.doubleValue * animation.duration)) < 0.000_001)
            }
        }
        #expect(try #require(bob.values as? [NSNumber]).contains { abs($0.doubleValue + Double(working.size) * 0.02) < 0.000_001 },
                "the bob's deepest point is sampled")
        #expect(try #require(bob.values as? [NSNumber]).contains { abs($0.doubleValue) < 0.000_001 }, "the bob's cusp is sampled")

        for index in 0..<3 {
            let line = PersonaMotionAnimations.line(index, face: working, wallTime: wall)
            let dot = PersonaMotionAnimations.dot(index, face: thinking, wallTime: wall)
            let grayDot = PersonaMotionAnimations.grayDot(index, face: thinking, wallTime: wall)
            let phases = [0.0, 1.0 / 3, 2.0 / 3, 1]
            let expectedLineValues = phases.map {
                NSNumber(value: working.workingLineLit($0 * PersonaMotionAnimations.linePeriod) == index ? 1 : 0)
            }
            #expect(line.values as? [NSNumber] == expectedLineValues)
            let dotPhases = [0.0, 0.25, 0.5, 0.75, 1]
            let expectedDotValues = dotPhases.map { phase -> NSNumber in
                let lit = thinking.thinkingLit(phase * PersonaMotionAnimations.dotPeriod)
                return NSNumber(value: lit == index || lit == 3 ? 1 : 0)
            }
            let expectedGrayValues = dotPhases.map { phase -> NSNumber in
                let lit = thinking.thinkingLit(phase * PersonaMotionAnimations.dotPeriod)
                return NSNumber(value: lit == index || lit == 3 ? 0 : 1)
            }
            #expect(dot.values as? [NSNumber] == expectedDotValues)
            #expect(grayDot.values as? [NSNumber] == expectedGrayValues)
            #expect(expectedDotValues[3].intValue == 1 && expectedGrayValues[3].intValue == 0,
                    "the fourth phase draws all three orange dots")
            for animation in [line, dot, grayDot] {
                #expect(animation.keyPath == "opacity")
                #expect(animation.calculationMode == .discrete)
                #expect(animation.repeatCount == .infinity)
                #expect(animation.preferredFrameRateRange.preferred == 15)
                #expect(abs(animation.beginTime - -(wall.truncatingRemainder(dividingBy: animation.duration))) < 0.000_001)
            }
            #expect(line.duration == PersonaMotionAnimations.linePeriod)
            #expect(dot.duration == PersonaMotionAnimations.dotPeriod && grayDot.duration == dot.duration)
            #expect(line.keyTimes?.map(\.doubleValue) == phases)
            #expect(dot.keyTimes?.map(\.doubleValue) == dotPhases)
            #expect(grayDot.keyTimes?.map(\.doubleValue) == dotPhases)
        }
    }

    @Test func negativeWallTimeStartsAtThePositivePhase() {
        let animation = PersonaMotionAnimations.ring(face: face(.needsYou), wallTime: -0.25)
        #expect(abs(animation.beginTime - -(PersonaMotionAnimations.ringPeriod - 0.25)) < 0.000_001)
    }

    @Test func attachmentUpdatesStateSwapsAndDismantleOwnTheAnimations() throws {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 160, height: 160))
        let view = PersonaFaceMotionView(frame: CGRect(x: 10, y: 20, width: 46, height: 46))
        view.configure(face: face(.working), scheme: .light, displayScale: 3, animating: true, time: 0)
        #expect(animationKeys(in: view).isEmpty, "an unattached view cannot animate")
        window.addSubview(view)
        view.layoutIfNeeded()
        let arc = try #require(view.arc.layer.animation(forKey: PersonaMotionAnimations.arcKey))
        let bob = try #require(view.body.layer.animation(forKey: PersonaMotionAnimations.bobKey))
        let eyes = try #require(view.eyes.layer.animation(forKey: PersonaMotionAnimations.eyesKey))
        #expect(view.body.layer.anchorPoint == CGPoint(x: 0.5, y: 0.5))
        #expect(view.workingBodies.enumerated().allSatisfy {
            $1.layer.animation(forKey: PersonaMotionAnimations.lineKey($0)) != nil
        })
        #expect(view.hitTest(.zero, with: nil) == nil)

        view.configure(face: face(.working), scheme: .dark, displayScale: 2, animating: true, time: 0)
        view.frame.origin = CGPoint(x: 15, y: 25)
        view.layoutIfNeeded()
        #expect(view.arc.layer.animation(forKey: PersonaMotionAnimations.arcKey)?.beginTime == arc.beginTime)
        #expect(view.body.layer.animation(forKey: PersonaMotionAnimations.bobKey)?.beginTime == bob.beginTime)
        #expect(view.eyes.layer.animation(forKey: PersonaMotionAnimations.eyesKey)?.beginTime == eyes.beginTime)
        #expect(animationKeys(in: view).count == 8, "appearance and layout add no implicit loops")

        view.frame.size = .zero
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).isEmpty, "zero-sized content cannot animate")
        view.frame.size = CGSize(width: 46, height: 46)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 8, "layout back to a real size resumes at the current wall phase")

        view.configure(face: face(.thinking), scheme: .dark, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 6)
        #expect(view.dots.enumerated().allSatisfy { $1.layer.animation(forKey: PersonaMotionAnimations.dotKey($0)) != nil })
        #expect(view.grayDots.enumerated().allSatisfy { $1.layer.animation(forKey: PersonaMotionAnimations.grayDotKey($0)) != nil })
        #expect(view.arc.layer.animationKeys() == nil && view.body.layer.animationKeys() == nil)

        view.configure(face: face(.needsYou), scheme: .dark, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 2)
        #expect(view.body.layer.anchorPoint == Mochi.pivot)
        #expect(view.body.layer.animation(forKey: PersonaMotionAnimations.rockKey) != nil)
        #expect(view.ring.layer.animation(forKey: PersonaMotionAnimations.ringKey) != nil)

        view.configure(face: face(.idle), scheme: .dark, displayScale: 2, animating: false, time: 0)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).isEmpty)
        view.configure(face: face(.needsYou), scheme: .light, displayScale: 3, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 2)
        view.removeFromSuperview()
        #expect(animationKeys(in: view).isEmpty, "detaching stops every piece")
        window.addSubview(view)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 2, "reattachment resumes at the current wall phase")
        PersonaFaceMotion.dismantleUIView(view, coordinator: ())
        #expect(animationKeys(in: view).isEmpty)
        view.removeFromSuperview()
        window.addSubview(view)
        #expect(animationKeys(in: view).isEmpty, "dismantling cannot restart a retained view")
        view.removeFromSuperview()
    }

    @Test func compactAndShutEyesOmitOnlyTheirOwnAnimations() throws {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 100, height: 100))
        let view = PersonaFaceMotionView(frame: CGRect(x: 0, y: 0, width: 32, height: 32))
        window.addSubview(view)
        defer { view.removeFromSuperview() }
        view.configure(face: face(.working, size: 32), scheme: .light, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 3, "arc, bob and eyes only below the compact cutoff")
        view.frame.size = CGSize(width: 36, height: 36)
        view.configure(face: face(.working, size: 36), scheme: .light, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(animationKeys(in: view).count == 8, "exactly 36 is not compact")
        let arcBefore = try #require(view.arc.layer.animation(forKey: PersonaMotionAnimations.arcKey)).beginTime
        let bobBefore = try #require(view.body.layer.animation(forKey: PersonaMotionAnimations.bobKey)).beginTime
        var shut = face(.working, size: 36)
        shut.eyesShut = true
        view.configure(face: shut, scheme: .light, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(view.workingEyes.allSatisfy { $0.layer.animationKeys() == nil })
        #expect(view.body.layer.animation(forKey: PersonaMotionAnimations.bobKey)?.beginTime == bobBefore)
        #expect(view.arc.layer.animation(forKey: PersonaMotionAnimations.arcKey)?.beginTime == arcBefore)
        var expression = shut
        expression.eyesShut = false
        expression.mouth = 0
        view.configure(face: expression, scheme: .light, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(view.eyes.layer.animation(forKey: PersonaMotionAnimations.eyesKey) != nil)
        #expect(view.body.layer.animation(forKey: PersonaMotionAnimations.bobKey)?.beginTime == bobBefore)
        #expect(view.arc.layer.animation(forKey: PersonaMotionAnimations.arcKey)?.beginTime == arcBefore)
        expression.mouth = 1
        view.configure(face: expression, scheme: .light, displayScale: 2, animating: true, time: 0)
        view.layoutIfNeeded()
        #expect(view.body.layer.animation(forKey: PersonaMotionAnimations.bobKey)?.beginTime == bobBefore)
        #expect(view.arc.layer.animation(forKey: PersonaMotionAnimations.arcKey)?.beginTime == arcBefore)
    }

    private func animationKeys(in view: PersonaFaceMotionView) -> [String] {
        let layers = [view.body.layer, view.arc.layer, view.ring.layer] + view.workingBodies.map(\.layer) +
            view.workingEyes.map(\.layer) + view.dots.map(\.layer) + view.grayDots.map(\.layer)
        return layers.flatMap { $0.animationKeys() ?? [] }.sorted()
    }
}
