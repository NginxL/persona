#if os(iOS)
import SwiftUI
import WidgetKit

/// The pieces every Home Screen widget is built from (docs/apple/home-widgets.md「The look」): the name line with the
/// persona in its corner, the empty state, rings, bars, a state's dot. Each draws itself for the mode the system
/// renders in: its own colors in full color; in tinted, clear and Lock Screen modes, where the system paints every
/// opaque pixel in one color (two with the accent group), only shapes and the primary / secondary levels, the colors
/// in the accent group.

/// The levels of words and marks.
enum HomeInk { case primary, secondary, tertiary }

extension View {
    /// Words and marks at one of the system's levels.
    func homeInk(_ level: HomeInk) -> some View { modifier(InkModifier(level: level)) }

    /// A widget's or a state's color, in the accent group where the system paints in one color.
    func homeTone(_ color: Color) -> some View { modifier(ToneModifier(color: color)) }

    /// In the accent group: the color the system tints with in the tinted and clear modes and on the Lock Screen.
    func homeAccent() -> some View { modifier(AccentModifier()) }
}

#if DEBUG
/// The gallery's stand-in for the system's tint (`-showHomeWidgets`, docs/apple/home-widgets.md「Screenshots」): a
/// view outside a widget is never tinted, so the gallery paints the accent group in this color itself.
private struct HomeSimTintKey: EnvironmentKey { static let defaultValue: Color? = nil }
extension EnvironmentValues {
    var homeSimTint: Color? {
        get { self[HomeSimTintKey.self] }
        set { self[HomeSimTintKey.self] = newValue }
    }
}
#endif

private struct InkModifier: ViewModifier {
    var level: HomeInk
    #if DEBUG
    @Environment(\.homeSimTint) private var simTint
    #endif

    func body(content: Content) -> some View { content.foregroundStyle(style) }

    private var style: AnyShapeStyle {
        #if DEBUG
        // The gallery: the system's white levels, which a view outside a widget does not get.
        if simTint != nil { return AnyShapeStyle(Color.white.opacity(level == .primary ? 1 : level == .secondary ? 0.7 : 0.45)) }
        #endif
        switch level {
        case .primary: return AnyShapeStyle(.primary)
        case .secondary: return AnyShapeStyle(.secondary)
        case .tertiary: return AnyShapeStyle(.tertiary)
        }
    }
}

private struct ToneModifier: ViewModifier {
    var color: Color
    @Environment(\.widgetRenderingMode) private var mode

    func body(content: Content) -> some View {
        if mode == .fullColor {
            content.foregroundStyle(color)
        } else {
            content.foregroundStyle(Color.white).homeAccent()
        }
    }
}

private struct AccentModifier: ViewModifier {
    #if DEBUG
    @Environment(\.homeSimTint) private var simTint
    #endif

    func body(content: Content) -> some View {
        #if DEBUG
        if let simTint { content.widgetAccentable().colorMultiply(simTint) } else { content.widgetAccentable() }
        #else
        content.widgetAccentable()
        #endif
    }
}

/// The widget's name in its color, top left, and the persona in the top right corner saying how this widget's
/// subject is: the only place the persona sits on a widget that has something to show.
struct HomeHeader: View {
    var title: LocalizedStringKey
    var color: Color
    var mood: HomeMood?
    var pictures: HomeFacePictures?

    var body: some View {
        HStack(alignment: .center, spacing: HomeWidgetTokens.tight) {
            Text(title).font(HomeWidgetTokens.title).lineLimit(1).homeTone(color)
            Spacer(minLength: HomeWidgetTokens.tight)
            if let mood {
                HomeFace(size: HomeWidgetTokens.faceCorner, mood: mood, pictures: pictures)
            }
        }
        .frame(height: HomeWidgetTokens.faceCorner)
    }
}

/// What a widget shows when it has nothing: the persona, calm, and one plain line.
struct HomeEmpty: View {
    var text: LocalizedStringKey
    var mood: HomeMood = .idle
    var pictures: HomeFacePictures?
    /// Beside the line (a medium widget's half), or above it.
    var horizontal = false

    var body: some View {
        let face = HomeFace(size: HomeWidgetTokens.faceEmpty, mood: mood, pictures: pictures)
        let words = Text(text).font(HomeWidgetTokens.headline).homeInk(.secondary)
        Group {
            if horizontal {
                HStack(spacing: HomeWidgetTokens.gap * 1.5) { face; words.multilineTextAlignment(.leading); Spacer(minLength: 0) }
            } else {
                VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) { face; words.multilineTextAlignment(.leading) }
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .lineLimit(3)
    }
}

/// A state's dot.
struct HomeDot: View {
    var color: Color
    var body: some View {
        Circle().frame(width: HomeWidgetTokens.dot, height: HomeWidgetTokens.dot).homeTone(color)
    }
}

/// A ring filled to `fraction`, on its track (the system's Batteries and Fitness rings).
struct HomeRing: View {
    var fraction: Double
    var color: Color
    var width: CGFloat = HomeWidgetTokens.ringStroke
    /// Drawn in dashes: a computer that is offline.
    var dashed = false
    /// Intake and health cells show only the track at zero; existing widget rings keep their original marker.
    var showsZeroArc = true
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        ZStack {
            Circle().stroke(mode == .fullColor ? HomeWidgetTokens.track : HomeWidgetTokens.trackMono,
                            style: StrokeStyle(lineWidth: width, dash: dashed ? [width * 0.4, width * 0.9] : []))
            if !dashed && (showsZeroArc || fraction > 0) {
                Circle().trim(from: 0, to: max(0.002, min(1, fraction)))
                    .stroke(style: StrokeStyle(lineWidth: width, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                    .homeTone(color)
            }
        }
        .padding(width / 2)
    }
}

/// A bar filled to `fraction`, on its track.
struct HomeBar: View {
    var fraction: Double
    var color: Color
    var height: CGFloat = HomeWidgetTokens.bar + HomeWidgetTokens.hair
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(mode == .fullColor ? HomeWidgetTokens.track : HomeWidgetTokens.trackMono)
                Capsule().frame(width: max(height, geo.size.width * max(0, min(1, fraction)))).homeTone(color)
            }
        }
        .frame(height: height)
    }
}

/// The big number a widget is about.
struct HomeHero: View {
    var text: String
    var font: Font = HomeWidgetTokens.hero
    var color: Color?

    var body: some View {
        let t = Text(verbatim: text).font(font).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.heroMinScale).monospacedDigit()
        if let color { t.homeTone(color) } else { t.homeInk(.primary) }
    }
}

/// "Updated 40 minutes ago" under content that stopped coming, so old numbers never pass for current ones.
struct HomeStaleNote: View {
    var since: Date
    /// The entry's date: a timeline's later entries say longer ago.
    var now: Date
    @Environment(\.locale) private var locale

    var body: some View {
        Text("Updated \(ago) [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.tertiary)
    }

    private var ago: String {
        let f = RelativeDateTimeFormatter()
        f.locale = locale
        f.unitsStyle = .full
        return f.localizedString(for: since, relativeTo: now)
    }
}

/// How long since, at the entry's date (a timeline's later entries say more): "4m", "6h".
func homeAgo(_ since: Double, now: Date) -> String {
    Duration.seconds(max(60, now.timeIntervalSince1970 - since))
        .formatted(.units(allowed: [.days, .hours, .minutes], width: .narrow, maximumUnitCount: 1))
}

/// Short counts: 1.2K, 3.4M.
func homeShort(_ n: Int) -> String {
    n.formatted(.number.notation(.compactName).precision(.significantDigits(2)))
}

/// Steps in a small place: whole under ten thousand (6,842, never 6800 or 6.8K: a day's steps are counted to the step),
/// short above.
func homeSteps(_ n: Int) -> String { n < 10_000 ? n.formatted() : homeShort(n) }
#endif
