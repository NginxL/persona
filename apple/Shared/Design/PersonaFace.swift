import SwiftUI

/// What the persona's dot shows (docs/apple/persona.md「States」). Raw values are persona.json's.
nonisolated enum PersonaState: String, CaseIterable, Identifiable, Sendable {
    case idle, thinking, working
    case needsYou = "needs_you"
    case offline

    var id: String { rawValue }
    var title: String {
        switch self {
        case .idle: String(localized: "Idle")
        case .thinking: String(localized: "Thinking [Persona Face 43ce45]")
        case .working: StateName.working.text
        case .needsYou: StateName.needsYou.text
        case .offline: String(localized: "Offline [Machine Entity be1b4f]")
        }
    }
}

/// Mochi at one moment, shared with the Live Activity and watch targets. The app supplies its animation clock.
struct PersonaFace: View {
    enum Design: String, Sendable {
        /// Mochi, the default: the app icon's character in the icon's own colors (not the tint).
        case mochi
    }

    var size: CGFloat
    var design: Design
    var state: PersonaState
    /// Seconds (any epoch) for the moving states; ignored when not `moving`.
    var time: TimeInterval = 0
    /// The thinking / working / needs-you motion runs; false: each state's still frame.
    var moving = false
    /// The idle blink every few seconds (`Theme.Motion.blink`); nil: eyes stay open.
    var blink: Animation?
    /// A call's own expressions (#9887, the call screen only; `CallAvatarLook`): the mouth open by the voice's level while
    /// the assistant speaks (0 closed … 1 wide), and the eyes shut while the call is on hold. The defaults (no mouth, eyes
    /// as the state has them) draw Mochi exactly as everywhere else: nothing is added to the picture.
    var mouth: Double? = nil
    var eyesShut = false

    /// Needs You's ring: this share of the face's side, never thinner than `ringMin`. A package's picture with no frame for
    /// the state gets the same ring on the activity (FleetFace), and offline makes it gray at this opacity.
    static let ringShare: CGFloat = 0.07
    static let ringMin: CGFloat = 1.5
    static let offlineOpacity = 0.7

    @Environment(\.colorScheme) private var scheme

    private var needsYou: Color { StatusPalette.needsYou.dot }

    var body: some View {
        switch design {
        case .mochi: mochi
        }
    }

    // MARK: Mochi: the app icon's white blob waving on its paper disc (a dark one in the dark), outlined in ink, with
    // its orange cheek. It ignores the tint: the icon's colors are what make it the icon's character.

    private var mochi: some View {
        let t = time
        let ink = MochiInk(dark: scheme == .dark, offline: state == .offline)
        let compact = size < Mochi.compactBelow
        return ZStack {
            Circle().fill(LinearGradient(colors: ink.disc, startPoint: .top, endPoint: .bottom))
            if state == .working { mochiArc(t) }
            ZStack {
                mochiBody(ink: ink, compact: compact, lines: mochiLines(t))
                if state != .offline && !eyesShut { mochiEyes(t, ink: ink, compact: compact) }
                if eyesShut && state != .offline {
                    Canvas { ctx, box in Mochi.closedEyes(&ctx, side: box.width, ink: ink, compact: compact) }
                }
                if let mouth, state != .offline {
                    Canvas { ctx, box in Mochi.mouth(&ctx, side: box.width, ink: ink, compact: compact, open: mouth) }
                }
            }
            .rotationEffect(.degrees(mochiRock(t)), anchor: UnitPoint(x: Mochi.pivot.x, y: Mochi.pivot.y))
            .offset(y: mochiBob(t))
            if state == .thinking {
                bubble(t, lit: Mochi.cheekColor).offset(x: size * 0.25, y: -size * 0.29)
            }
        }
        .frame(width: size, height: size)
        .clipShape(.circle)
        .overlay { needsYouRing(t) }
    }

    /// Everything except the eyes and optional call expressions. The moving iOS host reuses the same Canvas, including
    /// the original body-and-lines composition in each noncompact working phase.
    func mochiBody(ink: MochiInk, compact: Bool, lines: [Double]) -> some View {
        Canvas { ctx, box in Mochi.draw(&ctx, side: box.width, ink: ink, compact: compact, lines: lines) }
    }

    /// The motion-line phase while working; the still frame selects the first line.
    func workingLineLit(_ t: TimeInterval) -> Int {
        moving ? Int(t * 4) % 3 : 0
    }

    /// The motion lines beside the hand: steady when idle and needs-you, lit one after another while working, none
    /// while thinking (the bubble is there) or offline.
    func mochiLines(_ t: TimeInterval) -> [Double] {
        switch state {
        case .idle, .needsYou: return [1, 1, 1]
        case .working:
            let lit = workingLineLit(t)
            return (0..<3).map { $0 == lit ? 1 : 0.35 }
        case .thinking, .offline: return []
        }
    }

    /// Where the eyes look at time `t`, before the compositor applies any scan relative to phase zero.
    func mochiEyeLook(_ t: TimeInterval) -> CGSize {
        switch state {
        case .thinking: CGSize(width: size * 0.022, height: -size * 0.03)
        case .working: CGSize(width: size * (moving ? sin(t * 1.8) * 0.028 : -0.024), height: size * 0.012)
        default: .zero
        }
    }

    /// The eyes: they blink when idle, look up at the bubble while thinking, and scan while working.
    func mochiEyes(_ t: TimeInterval, ink: MochiInk, compact: Bool) -> some View {
        let r = Mochi.eyeRadius * (compact ? Mochi.compactFeatures : 1) * size
        let look = mochiEyeLook(t)
        return ZStack {
            ForEach(Mochi.eyes.indices, id: \.self) { i in
                Circle().fill(ink.line).frame(width: r * 2, height: r * 2)
                    .position(x: Mochi.eyes[i].x * size, y: Mochi.eyes[i].y * size)
            }
        }
        .frame(width: size, height: size)
        .modifier(Blink(animation: state == .idle ? blink : nil, anchor: UnitPoint(x: 0.5, y: Mochi.eyeLine)))
        .offset(look)
    }

    /// Needs You waves: the body rocks on its bottom like the icon's hello.
    func mochiRock(_ t: TimeInterval) -> Double {
        state == .needsYou && moving ? sin(t * 7) * 5 : 0
    }

    /// Working bobs a little.
    func mochiBob(_ t: TimeInterval) -> CGFloat {
        state == .working && moving ? CGFloat(abs(sin(t * 5))) * -size * 0.02 : 0
    }

    /// Working's arc angle, shared by the vector drawing and its compositor transform.
    func mochiArcDegrees(_ t: TimeInterval) -> Double {
        let turn = moving ? (t / 1.6).truncatingRemainder(dividingBy: 1) * 360 : 300
        return turn - 90
    }

    /// Working's arc before its rotation, shared by the vector drawing and the compositor's still-angle host.
    func mochiArcShape() -> some View {
        let w = max(Self.ringMin, size * Self.ringShare)
        return Circle()
            .trim(from: 0, to: 0.28)
            .stroke(Mochi.cheekColor, style: StrokeStyle(lineWidth: w, lineCap: .round))
            .padding(w / 2)
    }

    /// Working's arc of the cheek's orange running around the rim.
    func mochiArc(_ t: TimeInterval) -> some View {
        mochiArcShape().rotationEffect(.degrees(mochiArcDegrees(t)))
    }

    /// Needs You's ring opacity at one moment, shared by the vector drawing and its compositor keyframes.
    func needsYouPulse(_ t: TimeInterval) -> Double {
        moving ? 0.55 + 0.45 * cos(t * 3) : 1
    }

    /// Needs You's blue ring, breathing while moving.
    @ViewBuilder func needsYouRing(_ t: TimeInterval) -> some View {
        if state == .needsYou {
            Circle().strokeBorder(needsYou.opacity(needsYouPulse(t)), lineWidth: max(Self.ringMin, size * Self.ringShare))
        }
    }

    /// The thought-bubble phase at one moment. Phase three lights all three dots and is also the still frame.
    func thinkingLit(_ t: TimeInterval) -> Int {
        moving ? Int(t * 2.5) % 4 : 3
    }

    /// A small thought bubble whose three dots light up in turn.
    func bubble(_ t: TimeInterval, lit color: Color) -> some View {
        bubbleDots(lit: thinkingLit(t), lit: color)
        .background(.white, in: .capsule)
        .shadow(color: .black.opacity(0.15), radius: size * 0.02, y: size * 0.01)
    }

    var bubbleDotSide: CGFloat { size * 0.05 }

    /// The bubble's dots without its capsule, shared by the vector frame and the compositor's nested dot layers.
    func bubbleDots(lit: Int, lit color: Color) -> some View {
        bubbleDots { i in Circle().fill(i == lit || lit == 3 ? color : Color(white: 0.7)) }
    }

    /// The original HStack with caller-provided dots, used by the compositor without duplicating its spacing or padding.
    func bubbleDots<Content: View>(@ViewBuilder _ content: @escaping (Int) -> Content) -> some View {
        HStack(spacing: size * 0.025) {
            ForEach(0..<3) { i in
                content(i).frame(width: bubbleDotSide, height: bubbleDotSide)
            }
        }
        .padding(.horizontal, size * 0.04)
        .frame(height: size * 0.13)
    }

    /// A blink every few seconds: a periodic timeline (one wake-up per blink, nothing between) driving a short
    /// phase animation. No animation: no blink.
    private struct Blink: ViewModifier {
        var animation: Animation?
        var anchor: UnitPoint = .center

        func body(content: Content) -> some View {
            if let animation {
                TimelineView(.periodic(from: .now, by: 3.6)) { tl in
                    content.phaseAnimator([1.0, 0.12, 1.0], trigger: tl.date) { v, open in
                        v.scaleEffect(x: 1, y: open, anchor: anchor)
                    } animation: { _ in animation }
                }
            } else {
                content
            }
        }
    }
}

/// Mochi's colors in one appearance: the icon's paper (or its dark) behind, a white body outlined in ink. Offline
/// fades the body to gray. On the dark disc the offline face keeps the dark icon's ink outline (#11283): a mid-gray one
/// sat halfway between the disc and the gray body, so the body's edge came in two small steps and the face looked
/// blurred at a glance; with the ink the edge is one step, as the online face's, and only the gray says it is asleep.
nonisolated struct MochiInk {
    var disc: [Color]
    var body: [Color]
    var line: Color
    /// The motion lines: ink on the paper, light gray on the dark disc (as in the dark icon).
    var motion: Color
    var outline: Color
    var gloss: Color
    var cheek: Color
    var shadow: Color
    /// Asleep: closed eyes, drawn with the body.
    var offline: Bool

    init(dark: Bool, offline: Bool) {
        self.offline = offline
        disc = dark ? [Color(hex: 0x332A26), Color(hex: 0x1D1816)] : [Color(hex: 0xF4EADF), Color(hex: 0xE7D7C4)]
        // Asleep on the dark disc: a lighter gray body, further from the disc.
        let asleepOnDark = offline && dark
        if asleepOnDark {
            body = [Color(hex: 0xE2DDD9), Color(hex: 0xC8C1BC)]
        } else if offline {
            body = [Color(hex: 0xD9D4D0), Color(hex: 0xBDB6B1)]
        } else {
            body = [.white, Color(hex: 0xE9E3DE)]
        }
        line = offline ? Color(hex: 0x6E6763) : Color(hex: 0x1D1816)
        motion = dark ? Color(hex: 0xD6D1CD) : line
        // The dark icon keeps the ink outline: on the dark disc it reads as the body's shadow edge. Asleep too there.
        outline = offline && !dark ? Color(hex: 0x6E6763) : Color(hex: dark ? 0x0E0B0A : 0x1D1816)
        gloss = .white
        cheek = offline ? Color(hex: 0xA59D98) : Mochi.cheekColor
        // A lighter shadow under the asleep body on the dark disc: the dark one blurred into its edge.
        shadow = .black.opacity(asleepOnDark ? 0.3 : dark ? 0.45 : 0.14)
    }
}

/// Mochi's geometry in fractions of the avatar's side, 0…1 from the top left, traced from the 1024 px app icon
/// (`p` maps icon pixels into the disc, a little larger than the icon's square so it fills the circle).
nonisolated enum Mochi {
    static let scale: CGFloat = 1.16
    static let origin = CGPoint(x: 545, y: 548)
    static let cheekColor = Color(hex: 0xF2703C)
    /// Below this many points the motion lines and the gloss go, and the eyes and cheek grow, so the face still reads.
    static let compactBelow: CGFloat = 36
    static let compactFeatures: CGFloat = 1.3

    static func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
        CGPoint(x: 0.5 + (x - origin.x) / 1024 * scale, y: 0.5 + (y - origin.y) / 1024 * scale)
    }

    static let eyes = [p(397, 497), p(553, 480)]
    static var eyeLine: CGFloat { (eyes[0].y + eyes[1].y) / 2 }
    static let eyeRadius: CGFloat = 29 / 1024 * scale
    static let cheek = p(312, 556)
    static let cheekRadius: CGFloat = 37 / 1024 * scale
    /// The outline, as a share of the side; never thinner than `strokeMin` points.
    static let stroke: CGFloat = 30 / 1024 * scale
    static let strokeMin: CGFloat = 1.3
    /// Where the body rocks from (its bottom middle).
    static let pivot = p(500, 780)
    /// The three lines beside the hand.
    static let lines: [(CGPoint, CGPoint)] = [
        (p(792, 352), p(818, 298)),
        (p(838, 384), p(884, 340)),
        (p(864, 428), p(924, 408)),
    ]

    static func body(_ w: CGFloat) -> Path {
        func q(_ x: CGFloat, _ y: CGFloat) -> CGPoint { let u = p(x, y); return CGPoint(x: u.x * w, y: u.y * w) }
        var b = Path()
        b.move(to: q(262, 778))
        b.addCurve(to: q(702, 772), control1: q(420, 792), control2: q(600, 790))
        b.addCurve(to: q(760, 640), control1: q(742, 764), control2: q(752, 700))
        b.addCurve(to: q(826, 502), control1: q(800, 598), control2: q(828, 562))
        b.addCurve(to: q(764, 428), control1: q(825, 446), control2: q(795, 418))
        b.addCurve(to: q(712, 494), control1: q(736, 440), control2: q(722, 468))
        b.addCurve(to: q(560, 304), control1: q(690, 440), control2: q(650, 350))
        b.addCurve(to: q(254, 382), control1: q(470, 260), control2: q(330, 288))
        b.addCurve(to: q(168, 672), control1: q(184, 462), control2: q(158, 582))
        b.addCurve(to: q(262, 778), control1: q(176, 742), control2: q(206, 772))
        b.closeSubpath()
        return b
    }

    /// The icon's highlight on the top left of the head.
    static func gloss(_ w: CGFloat) -> Path {
        func q(_ x: CGFloat, _ y: CGFloat) -> CGPoint { let u = p(x, y); return CGPoint(x: u.x * w, y: u.y * w) }
        var g = Path()
        g.move(to: q(312, 380))
        g.addQuadCurve(to: q(412, 334), control: q(352, 346))
        g.move(to: q(288, 414))
        g.addLine(to: q(290, 411))
        return g
    }

    /// Everything but the eyes (views, so they can blink) on a side of `w`: the shadow, the body, the cheek, the
    /// gloss, and the motion lines at the given opacities (none: no lines). Offline closes the eyes here.
    static func draw(_ ctx: inout GraphicsContext, side w: CGFloat, ink: MochiInk, compact: Bool, lines: [Double]) {
        let lw = max(strokeMin, w * stroke)
        ctx.drawLayer { shadowCtx in
            shadowCtx.addFilter(.blur(radius: w * 0.02))
            let c = p(490, 800)
            shadowCtx.fill(Path(ellipseIn: CGRect(x: (c.x - 0.3) * w, y: (c.y - 0.022) * w, width: 0.6 * w, height: 0.044 * w)), with: .color(ink.shadow))
        }
        let body = body(w)
        ctx.fill(body, with: .linearGradient(Gradient(colors: ink.body), startPoint: CGPoint(x: 0.4 * w, y: 0.3 * w), endPoint: CGPoint(x: 0.5 * w, y: 0.82 * w)))
        ctx.stroke(body, with: .color(ink.outline), style: StrokeStyle(lineWidth: lw, lineCap: .round, lineJoin: .round))
        let k: CGFloat = compact ? compactFeatures : 1
        let r = cheekRadius * k
        ctx.fill(Path(ellipseIn: CGRect(x: (cheek.x - r) * w, y: (cheek.y - r * 0.9) * w, width: 2 * r * w, height: 1.8 * r * w)), with: .color(ink.cheek))
        if !compact {
            ctx.stroke(gloss(w), with: .color(ink.gloss), style: StrokeStyle(lineWidth: lw * 0.6, lineCap: .round))
            for (i, (a, b)) in self.lines.enumerated() where i < lines.count {
                var l = Path()
                l.move(to: CGPoint(x: a.x * w, y: a.y * w))
                l.addLine(to: CGPoint(x: b.x * w, y: b.y * w))
                ctx.stroke(l, with: .color(ink.motion.opacity(lines[i])), style: StrokeStyle(lineWidth: lw * 0.85, lineCap: .round))
            }
        }
        if ink.offline { closedEyes(&ctx, side: w, ink: ink, compact: compact) }
    }

    /// Closed eyes, two short curves: asleep (offline), and shut on a call that is on hold (`PersonaFace.eyesShut`).
    static func closedEyes(_ ctx: inout GraphicsContext, side w: CGFloat, ink: MochiInk, compact: Bool) {
        let lw = max(strokeMin, w * stroke)
        let k: CGFloat = compact ? compactFeatures : 1
        for e in eyes {
            var l = Path()
            let half = eyeRadius * k * w
            l.move(to: CGPoint(x: e.x * w - half, y: e.y * w))
            l.addQuadCurve(to: CGPoint(x: e.x * w + half, y: e.y * w), control: CGPoint(x: e.x * w, y: e.y * w + half * 0.7))
            ctx.stroke(l, with: .color(ink.line), style: StrokeStyle(lineWidth: lw * 0.8, lineCap: .round))
        }
    }

    /// Where the mouth sits (a call only, `PersonaFace.mouth`): under the eyes, as the face tilts with them; its width
    /// and how far it opens at a full voice, in icon pixels.
    static let mouthCenter = p(484, 562)
    static let mouthWidth: CGFloat = 56 / 1024 * scale
    static let mouthDepth: CGFloat = 44 / 1024 * scale
    /// The eyes' tilt (the right one sits higher): the mouth follows it.
    static var tilt: Angle { .radians(atan2(eyes[1].y - eyes[0].y, eyes[1].x - eyes[0].x)) }

    /// The mouth while the assistant speaks on a call: a small smile when `open` is 0, opening into a rounded mouth (ink,
    /// its tongue in the cheek's orange) as the voice gets louder. Drawn only when a call asks for it.
    static func mouth(_ ctx: inout GraphicsContext, side w: CGFloat, ink: MochiInk, compact: Bool, open: Double) {
        let lw = max(strokeMin, w * stroke)
        let k: CGFloat = compact ? compactFeatures : 1
        let half = mouthWidth * k * w / 2
        let depth = mouthDepth * k * w * CGFloat(min(max(open, 0), 1))
        var face = ctx
        face.translateBy(x: mouthCenter.x * w, y: mouthCenter.y * w)
        face.rotate(by: tilt)
        var lip = Path()
        lip.move(to: CGPoint(x: -half, y: 0))
        lip.addQuadCurve(to: CGPoint(x: half, y: 0), control: CGPoint(x: 0, y: half * 0.35))
        guard depth > lw * 0.5 else {
            face.stroke(lip, with: .color(ink.line), style: StrokeStyle(lineWidth: lw * 0.8, lineCap: .round))
            return
        }
        // The upper lip a gentle smile, the lower one a round U as deep as the voice is loud.
        var mouth = Path()
        mouth.move(to: CGPoint(x: -half, y: 0))
        mouth.addQuadCurve(to: CGPoint(x: half, y: 0), control: CGPoint(x: 0, y: half * 0.2))
        mouth.addCurve(to: CGPoint(x: -half, y: 0), control1: CGPoint(x: half * 0.95, y: depth * 1.33),
                       control2: CGPoint(x: -half * 0.95, y: depth * 1.33))
        mouth.closeSubpath()
        face.fill(mouth, with: .color(ink.line))
        var tongue = face
        tongue.clip(to: mouth)
        tongue.fill(Path(ellipseIn: CGRect(x: -half * 0.6, y: depth * 0.55, width: half * 1.2, height: depth)), with: .color(ink.cheek))
        face.stroke(mouth, with: .color(ink.line), style: StrokeStyle(lineWidth: lw * 0.6, lineJoin: .round))
    }
}

extension Color {
    /// `mix(with:by:)` is watchOS 11+, and the watch app still installs on watchOS 10 (older watches stop there):
    /// there it blends the resolved components instead.
    nonisolated func blended(with other: Color, by t: Double) -> Color {
        if #available(watchOS 11, *) { return mix(with: other, by: t) }
        let a = resolve(in: EnvironmentValues()), b = other.resolve(in: EnvironmentValues())
        func m(_ x: Float, _ y: Float) -> Double { Double(x) + (Double(y) - Double(x)) * t }
        return Color(.sRGBLinear, red: m(a.red, b.red), green: m(a.green, b.green), blue: m(a.blue, b.blue), opacity: m(a.opacity, b.opacity))
    }
}
