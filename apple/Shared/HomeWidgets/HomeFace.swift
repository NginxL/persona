#if os(iOS)
import SwiftUI
import WidgetKit
import UIKit

/// How the persona looks in a widget: the app's states (docs/apple/persona.md「States」) and Done, which only the
/// widgets draw: an agent finished and you have not looked yet.
nonisolated enum HomeMood: String, CaseIterable, Sendable {
    case idle, thinking, working, needsYou, done, offline

    /// The face the app's drawing and a package's pictures have for it; Done is Idle with the check.
    var personaState: PersonaState {
        switch self {
        case .idle, .done: .idle
        case .thinking: .thinking
        case .working: .working
        case .needsYou: .needsYou
        case .offline: .offline
        }
    }

    /// The state's color behind the face; nil: the persona's own.
    var tone: StatusTone? {
        switch self {
        case .needsYou: HomeWidgetTokens.needsYou
        case .working: HomeWidgetTokens.working
        case .done: HomeWidgetTokens.done
        case .idle, .thinking, .offline: nil
        }
    }

    /// The fleet's mood (`FleetPersona.state`'s order, with Done): someone waits for you, then work, then something
    /// finished, then Persona answering; a snapshot that stopped coming is Offline.
    static func of(_ counts: HomeSnapshot.Counts, answering: Bool = false, stale: Bool) -> HomeMood {
        if stale { return .offline }
        if counts.needsYou > 0 { return .needsYou }
        if counts.working > 0 { return .working }
        if counts.done > 0 { return .done }
        return answering ? .thinking : .idle
    }
}

/// A package's pictures at the size a widget draws them (the app writes them into `Widgets/faces/`, as it does the
/// Live Activity's): one a state, the avatar for the states without one.
nonisolated struct HomeFacePictures: @unchecked Sendable {
    var states: [String: UIImage] = [:]
    var avatar: UIImage?

    /// The state's own picture (true), else the avatar or idle's, drawn with the built-in cue.
    func picture(_ state: PersonaState) -> (UIImage, own: Bool)? {
        if let own = states[state.rawValue] { return (own, true) }
        if let other = avatar ?? states[PersonaState.idle.rawValue] { return (other, false) }
        return nil
    }
}

/// The persona in a widget, in every way the system draws a widget:
/// - full color: the app's own drawing (`PersonaFace`, Mochi) or the package's picture for the state;
/// - tinted and clear on the Home Screen (`accented`) and the Lock Screen (`vibrant`), where the system paints every
///   opaque pixel in one or two colors: Mochi as a line drawing (`MochiGlyph`) and a package's picture in gray, the
///   state's cue in the accent color.
/// Done adds a check at the bottom right.
struct HomeFace: View {
    var size: CGFloat
    var mood: HomeMood
    var pictures: HomeFacePictures?
    /// Draw the disc behind the line drawing (off where the system draws one, the Lock Screen's circle).
    var disc = true

    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        ZStack {
            if let found = pictures?.picture(mood.personaState) {
                packageFace(found.0, own: found.own)
            } else {
                mochi
            }
        }
        .frame(width: size, height: size)
        .overlay(alignment: .bottomTrailing) {
            if mood == .done { DoneCheck(side: size * HomeWidgetTokens.checkShare) }
        }
        .accessibilityHidden(true)
    }

    @ViewBuilder private var mochi: some View {
        if mode == .fullColor {
            PersonaFace(size: size, design: .mochi, state: mood.personaState)
        } else {
            MochiGlyph(size: size, mood: mood, disc: disc)
        }
    }

    #if DEBUG
    @Environment(\.homeSimTint) private var simTint
    #endif

    /// The gallery has no system to gray a picture in the tinted and clear modes: it grays it itself.
    private var simGray: Bool {
        #if DEBUG
        simTint != nil
        #else
        false
        #endif
    }

    private func packageFace(_ image: UIImage, own: Bool) -> some View {
        let ring = max(PersonaFace.ringMin, size * PersonaFace.ringShare)
        return Group {
            if simGray {
                Image(uiImage: image).resizable().interpolation(.high).grayscale(1)
            } else {
                Image(uiImage: image).resizable().interpolation(.high).widgetAccentedRenderingMode(.desaturated)
            }
        }
            .scaledToFill()
            .frame(width: size, height: size)
            .clipShape(.circle)
            .grayscale(!own && mood == .offline ? 1 : 0)
            .opacity(mood == .offline && (!own || mode != .fullColor) ? PersonaFace.offlineOpacity : 1)
            .overlay {
                // The state's cue on a picture that has none of its own, and on every picture where the system draws
                // it gray: the ring is how a gray face still says it needs you.
                if mood == .needsYou && (!own || mode != .fullColor) {
                    Circle().strokeBorder(mode == .fullColor ? HomeWidgetTokens.needsYou.dot : .white, lineWidth: ring)
                        .homeAccent()
                }
            }
    }
}

/// Done's check: a filled disc with the mark knocked out of it, so it still reads where the system paints the disc in
/// one color (tinted, clear, the Lock Screen).
private struct DoneCheck: View {
    var side: CGFloat
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        let full = mode == .fullColor
        Circle()
            .fill(full ? HomeWidgetTokens.done.dot : .white)
            .overlay {
                HomeWidgetTokens.symbol(HomeWidgetTokens.checkSymbol)
                    .font(.system(size: side * 0.56, weight: .heavy))
                    .foregroundStyle(full ? Color.white : Color.black)
                    .blendMode(full ? .normal : .destinationOut)
            }
            .compositingGroup()
            .frame(width: side, height: side)
            .homeAccent()
    }
}

/// Mochi as a line drawing, for the widget modes where every pixel takes one color: the outline, the eyes and the
/// cheek of the app icon's character, its state told by the same cues as the app's drawing (the working arc, the
/// needs-you ring, the thought bubble, closed eyes when offline, happy eyes when done), the cues in the accent color.
struct MochiGlyph: View {
    var size: CGFloat
    var mood: HomeMood
    var disc = true

    /// The outline is heavier than the icon's (`Mochi.stroke`): a line drawing has nothing else to hold the shape.
    static let stroke: CGFloat = 0.05
    static let discOpacity = 0.16
    static let cheekOpacity = 0.5
    static let offlineOpacity = 0.55

    var body: some View {
        let w = size
        let lw = max(Mochi.strokeMin, w * Self.stroke)
        let compact = w < Mochi.compactBelow
        ZStack {
            if disc { Circle().fill(Color.white.opacity(Self.discOpacity)) }
            Canvas { ctx, box in draw(&ctx, side: box.width, lw: lw, compact: compact) }
                .opacity(mood == .offline ? Self.offlineOpacity : 1)
            cue(lw: lw).homeAccent()
        }
        .frame(width: w, height: w)
    }

    private func draw(_ ctx: inout GraphicsContext, side w: CGFloat, lw: CGFloat, compact: Bool) {
        let ink = GraphicsContext.Shading.color(.white)
        let body = Mochi.body(w)
        ctx.stroke(body, with: ink, style: StrokeStyle(lineWidth: lw, lineCap: .round, lineJoin: .round))
        let k: CGFloat = compact ? Mochi.compactFeatures : 1.12
        let r = Mochi.cheekRadius * k
        ctx.fill(Path(ellipseIn: CGRect(x: (Mochi.cheek.x - r) * w, y: (Mochi.cheek.y - r * 0.9) * w, width: 2 * r * w, height: 1.8 * r * w)),
                 with: .color(.white.opacity(Self.cheekOpacity)))
        // Eyes: open dots, looking up (thinking) or aside (working); closed when offline, happy arcs when done.
        let er = Mochi.eyeRadius * k * w
        let look: CGSize = switch mood {
        case .thinking: CGSize(width: w * 0.022, height: -w * 0.03)
        case .working: CGSize(width: -w * 0.024, height: w * 0.012)
        default: .zero
        }
        for e in Mochi.eyes {
            let c = CGPoint(x: e.x * w + look.width, y: e.y * w + look.height)
            switch mood {
            case .offline, .done:
                var l = Path()
                l.move(to: CGPoint(x: c.x - er, y: c.y))
                let lift = mood == .done ? -er * 1.1 : er * 0.7
                l.addQuadCurve(to: CGPoint(x: c.x + er, y: c.y), control: CGPoint(x: c.x, y: c.y + lift))
                ctx.stroke(l, with: ink, style: StrokeStyle(lineWidth: lw * 0.9, lineCap: .round))
            default:
                ctx.fill(Path(ellipseIn: CGRect(x: c.x - er, y: c.y - er, width: er * 2, height: er * 2)), with: ink)
            }
        }
        // The motion lines beside the hand, as the app draws them when idle (none while thinking or offline).
        if !compact, mood == .idle || mood == .needsYou || mood == .done {
            for (a, b) in Mochi.lines {
                var l = Path()
                l.move(to: CGPoint(x: a.x * w, y: a.y * w))
                l.addLine(to: CGPoint(x: b.x * w, y: b.y * w))
                ctx.stroke(l, with: ink, style: StrokeStyle(lineWidth: lw * 0.8, lineCap: .round))
            }
        }
    }

    /// The state's cue, in the accent color where the system has one.
    @ViewBuilder private func cue(lw: CGFloat) -> some View {
        let ring = max(PersonaFace.ringMin, size * PersonaFace.ringShare)
        switch mood {
        case .working:
            Circle().trim(from: 0, to: 0.28)
                .stroke(Color.white, style: StrokeStyle(lineWidth: ring, lineCap: .round))
                .padding(ring / 2)
                .rotationEffect(.degrees(300 - 90))
        case .needsYou:
            Circle().strokeBorder(Color.white, lineWidth: ring)
        case .thinking:
            HStack(spacing: size * 0.03) {
                ForEach(0..<3) { _ in Circle().fill(Color.white).frame(width: size * 0.065, height: size * 0.065) }
            }
            .offset(x: size * 0.25, y: -size * 0.3)
        case .idle, .done, .offline:
            EmptyView()
        }
    }
}
#endif
