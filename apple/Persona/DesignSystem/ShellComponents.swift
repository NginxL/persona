import BeautifulUI
import SwiftUI

// Components of the ChatGPT-style shell (docs/apple/ios.md「New shell」). Neutral in every style (Theme.Shell): the shell is laid
// out like ChatGPT's, the styles still color the rest of the app.

/// The top switch: Assistant | Agents in a glass capsule, the selected one on a soft gray pill that glides across. Agents carries
/// the count of agents waiting on the user. Tapping Assistant while it is selected opens `chatMenu` (the conversation's
/// menu: Knowledge, Details, the launcher…), like tapping the title in ChatGPT; tapping Agents while it is selected opens
/// the list's options (Group By, Display, Machines…).
///
/// In the shell the selection is the shell's (`ShellState.page`, docs/apple/shell.md), not `tab`: a tap selects at once, and a
/// segment is its page's menu only while the strip rests on that page, so a segment never stands for a page that is
/// not on screen (a tab that said Agents over Assistant made Agents the menu of an empty list of options, 2026-11-02).
///
/// The switch reads nothing that the hub's pushes change: an open menu is laid out again whenever its button is, and
/// went back to its top under the finger choosing a machine (TestFlight 70, docs/lessons/ios.md). The count is
/// `WaitingBadge`, beside the menu rather than in its label, and reads the agents itself.
struct ShellSwitch<ChatMenu: View, WorkMenu: View>: View {
    @Binding var tab: AppTab
    @Environment(AppModel.self) private var model
    /// Across the top bar, or down the side column on iPhone Duo (`ShellSideRail`): there it is the Duo's tab bar.
    var axis: Axis = .horizontal
    /// iPhone Duo's tab bar along the foot of the inner display upright (#4231, `TabSwitchOverlay`), across.
    var bottomBar = false
    @ViewBuilder var chatMenu: () -> ChatMenu
    @ViewBuilder var workMenu: () -> WorkMenu
    @Namespace private var pill
    /// The strip under the switch: its pill follows the pages while they slide (`ShellPager`).
    @Environment(\.shell) private var shell
    /// In the moving bar (`ShellMovingBar`): the pill glides with the strip. The pages' own bars are transparent while
    /// it moves, so theirs sits on the page in front and does not read the strip at all (`PagerPill`).
    @Environment(\.shellPillFollowsStrip) private var follows
    @Environment(\.buiTheme) private var theme

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("ShellSwitch")
        #endif
        if tabBar {
            // iPhone Duo's tab bar (#4231): one glass capsule with a hairline edge and a faint shadow, as a tab bar down the
            // side of the outer display looks (the owner's reference), in every style.
            row
                .padding(axis == .vertical ? .vertical : .horizontal, Theme.Space.s)
                .padding(axis == .vertical ? .horizontal : .vertical, Theme.Space.xs)
                .surface(interactive: true, in: .capsule)
                .overlay(Capsule().strokeBorder(Theme.cardStroke, lineWidth: Theme.Size.hairline))
                .compositingGroup()
                .shadow(color: StyleSpec.current.softShadow, radius: Theme.Space.s, y: Theme.Space.xxs)
                .modifier(SwitchMotion(chosen: chosen, upright: true))
        } else if AppStyle.current.usesBeautifulUI {
            // BeautifulUI's pill segmented control: a field-colored track, the chosen segment a raised capsule.
            row
                .padding(Theme.Space.xs)
                .background(theme.colors.field, in: .capsule)
                .overlay(Capsule().strokeBorder(theme.colors.line, lineWidth: Theme.Size.hairline))
                .modifier(SwitchMotion(chosen: chosen, upright: false))
        } else {
            row
                .padding(Theme.Space.xs)
                .surface(interactive: true, in: .capsule)
                .modifier(SwitchMotion(chosen: chosen, upright: false))
        }
    }

    /// The switch is iPhone Duo's tab bar (#4231): down the side column, or along the foot upright. Its items are the
    /// page's symbol over its name; across the top it is the iPhone's switch of names.
    private var tabBar: Bool { axis == .vertical || bottomBar }

    /// The page the switch shows as chosen: the shell's, or `tab` outside it.
    private var chosen: AppTab {
        guard let shell else { return tab }
        return ShellState.tab(for: shell.page)
    }

    /// Whether the chosen segment opens its menu: not while the strip slides (a tap then picks a page). Only `moving`,
    /// not where the strip is: `pager.page` changes in the slide's animated transaction, and a switch reading it took
    /// that animation into a bar a pushed page had taken out of the window, so the slide never ended (`ShellPager.settle`).
    /// At rest the strip is on the shell's page anyway.
    private func opensMenu(_ value: AppTab) -> Bool {
        guard chosen == value else { return false }
        // No menu to open (the switch over a phone's tabs, iPhone Duo open, #3680): the chosen segment is a plain button,
        // not an empty menu that VoiceOver called List Options.
        if value == .chats, let shell, !shell.workAtRoot { return false }
        if value == .chats, WorkMenu.self == EmptyView.self { return false }
        if value == .assistant, ChatMenu.self == EmptyView.self { return false }
        guard let pager = shell?.pager else { return true }
        return !pager.moving
    }

    private var row: some View {
        stack {
            ZStack(alignment: .topTrailing) {
                if opensMenu(.assistant) {
                    Menu { chatMenu() } label: { segment(String(localized: "shell.assistant", defaultValue: "Assistant", comment: "The chat side of the top switch (72 pt wide)"), .assistant, selected: true) }
                        .menuStyle(.button)
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("shell-assistant")
                        .modifier(MenuSegmentAccess(hint: "Chat menu", upright: tabBar, title: String(localized: "shell.assistant", defaultValue: "Assistant", comment: "The chat side of the top switch (72 pt wide)"), symbol: Self.symbol(.assistant)))
                } else {
                    button(String(localized: "shell.assistant", defaultValue: "Assistant", comment: "The chat side of the top switch (72 pt wide)"), .assistant, id: "shell-assistant")
                }
            }
            ZStack(alignment: .topTrailing) {
                if opensMenu(.chats) {
                    Menu { workMenu() } label: { segment("Agents", .chats, selected: true) }
                        .menuStyle(.button)
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("shell-agents")
                        .modifier(MenuSegmentAccess(hint: "List options", upright: tabBar, title: "Agents", symbol: Self.symbol(.chats)))
                } else {
                    button("Agents", .chats, id: "shell-agents")
                }
                // The tab bar's counts are on its items' symbols (`TabBarBadge`).
                if !tabBar { WaitingBadge() }
            }
            #if os(iOS) || os(visionOS)
            if LarkHomePreference.shared.shows {
                button(String(localized: "Messages [Quote Views 4da199]"), .feishu, id: "shell-feishu")
            }
            #endif
        }
        .background(alignment: .leading) {
            // The tab bar marks its chosen item itself (`tabBarSegment`).
            if let shell, !tabBar { PagerPill(shell: shell, follows: follows) }
            // Its lens glides to a newly chosen item (#9837): under the items where it does not bend them…
            if tabBar, !StyleSpec.current.usesGlass { TabBarLens(index: ShellState.index(for: chosen), axis: axis) }
        }
        .overlay(alignment: .topLeading) {
            // …and over them as glass, which does.
            if tabBar, StyleSpec.current.usesGlass { TabBarLens(index: ShellState.index(for: chosen), axis: axis) }
        }
    }

    /// The segments side by side, or one above the other on the side rail.
    @ViewBuilder private func stack<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        if axis == .vertical { VStack(spacing: .zero, content: content) } else { HStack(spacing: .zero, content: content) }
    }

    private func button(_ title: String, _ value: AppTab, id: String) -> some View {
        Button {
            #if DEBUG
            ShellTrace.shared.note("switch \(value) chosen=\(chosen)")
            #endif
            if chosen != value { Haptics.tap() }
            model.selectHomeTab(value)
            if let shell {
                // Also while the strip slides, or when the same tab is reselected: the last tap wins.
                shell.select(ShellState.index(for: value))
            } else if tab != value {
                tab = value
            }
        } label: {
            segment(title, value, selected: chosen == value)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(id)
        .modifier(ButtonSegmentAccess(selected: chosen == value, upright: tabBar, title: title, symbol: Self.symbol(value)))
    }

    @ViewBuilder private func segment(_ title: String, _ value: AppTab, selected: Bool) -> some View {
        if tabBar {
            tabBarSegment(title, value, selected: selected)
        } else if AppStyle.current.usesBeautifulUI {
            beautifulSegment(title, selected: selected)
        } else {
            plainSegment(title, selected: selected)
        }
    }

    /// The page's symbol on the rail, matching its shared tab.
    private static func symbol(_ value: AppTab) -> String {
        #if os(iOS) || os(visionOS)
        if value == .feishu { return Theme.Icon.feishuChats } // a phone's tab only (no such tab on the Mac)
        #endif
        return value == .chats ? Theme.Icon.agents : Theme.Icon.ai
    }

    /// An item of iPhone Duo's tab bar (#4231, after a tab bar down the side of the outer display, the owner's reference):
    /// the page's symbol over its name, the chosen one's symbol white on a round fill of the Theme Color and its name in
    /// it, the others a line symbol over a plain name; the page's count red at the symbol's top trailing corner
    /// (`TabBarBadge`). Its name is what VoiceOver reads; a long press shows it large (`ButtonSegmentAccess`).
    private func tabBarSegment(_ title: String, _ value: AppTab, selected: Bool) -> some View {
        VStack(spacing: Theme.Space.xxs) {
            Group {
                // The chosen Assistant shows the persona's own face (#9837), as the owner's reference shows its brand on
                // its chosen home item.
                if selected, value == .assistant {
                    TabBarPersonaFace()
                } else {
                    ThemedIcon(Self.symbol(value))
                        .font(Theme.Font.heading)
                        .foregroundStyle(selected ? Color.white : Color.primary)
                        .frame(width: Theme.Size.well, height: Theme.Size.well)
                        .background { if selected { Circle().fill(Theme.accent) } }
                }
            }
            .overlay(alignment: .topTrailing) { TabBarBadge(tab: value) }
            Text(title)
                .font(selected ? Theme.Font.labelStrong : Theme.Font.label)
                .foregroundStyle(selected ? Theme.accent : Color.primary)
                .lineLimit(1)
                .minimumScaleFactor(0.7)
        }
        .frame(width: Theme.Shell.tabBarItemWidth, height: Theme.Shell.tabBarItemHeight)
        .contentShape(.rect)
        .accessibilityLabel(title)
    }

    private func beautifulSegment(_ title: String, selected: Bool) -> some View {
        Text(title)
            .buiFont(BUITextStyle(size: 13, weight: selected ? .semibold : .medium, lineHeight: 1.19))
            .foregroundStyle(selected ? theme.colors.ink : theme.colors.ink3)
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .scaledFrame(width: Theme.Shell.segmentWidth, height: Theme.Shell.switchHeight - Theme.Space.s)
            .background {
                if selected, shell == nil {
                    Color.clear.buiSurface(Capsule(), fill: theme.colors.surface, elevation: .button)
                        .matchedGeometryEffect(id: "pill", in: pill)
                }
            }
            .contentShape(.capsule)
    }

    private func plainSegment(_ title: String, selected: Bool) -> some View {
        Text(title)
            .font(selected ? Theme.Font.bodyStrong : Theme.Font.body)
            .foregroundStyle(selected ? Color.primary : Color.secondary)
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .scaledFrame(width: Theme.Shell.segmentWidth, height: Theme.Shell.switchHeight - Theme.Space.s)
            .background {
                if selected, shell == nil {
                    Capsule().fill(Theme.Shell.segment).matchedGeometryEffect(id: "pill", in: pill)
                }
            }
            .contentShape(.capsule)
    }
}

/// The switch's pill in the shell: under Assistant or Agents as the strip's position says, gliding with the finger. Its own
/// view: only it reads the position, the switch (and its menus) are not drawn again on every frame of a drag.
///
/// Only the moving bar's pill follows the strip. A page's bar can be out of the window (a page pushed over it, even
/// one pushed while the strip still slides), and a view there that took the slide's animation never runs it: the
/// slide's end waited for it, both pages out of reach and the moving bar's switch over the chat (2026-11-03, opening an
/// agent from the inbox, `ShellInboxAgentTests`).
private struct PagerPill: View {
    let shell: ShellState
    var follows: Bool
    @Environment(\.buiTheme) private var theme

    var body: some View {
        GeometryReader { g in
            let n = CGFloat(shell.pager.pageCount)
            let at = follows ? min(n - 1, max(0, shell.pager.position)) : CGFloat(shell.page)
            let size = CGSize(width: g.size.width / n, height: g.size.height)
            let offset = CGSize(width: at * size.width, height: 0)
            if AppStyle.current.usesBeautifulUI {
                // BeautifulUI's segmented control: the chosen segment is a raised capsule.
                Color.clear.buiSurface(Capsule(), fill: theme.colors.surface, elevation: .button)
                    .frame(width: size.width, height: size.height)
                    .offset(offset)
            } else {
                Capsule().fill(Theme.Shell.segment)
                    .frame(width: size.width, height: size.height)
                    .offset(offset)
            }
        }
    }
}

/// How many agents wait on the user, over the switch's Agents. Its own view: the count changes with nearly every push
/// (an agent asks, another is answered), and only this badge is drawn again.
private struct WaitingBadge: View {
    @Environment(AppModel.self) private var model
    private var offset: CGSize {
        #if os(iOS) || os(visionOS)
        if LarkHomePreference.shared.shows { return CGSize(width: -Theme.Space.s, height: -Theme.Space.l) }
        #endif
        return CGSize(width: Theme.Space.s, height: -Theme.Space.sm)
    }

    var body: some View {
        let waiting = model.agentsWaiting
        if AppStyle.current.usesBeautifulUI, let text = CountBadge.text(waiting) {
            BUIPill(text, tone: .custom(ink: StatusPalette.needsYou.text, fill: StatusPalette.needsYou.tint))
                .offset(offset)
                .allowsHitTesting(false)
                .accessibilityLabel(StateName.count(.needsYou, waiting))
        } else if waiting > 0 {
            CountBadge(count: waiting, look: .needs, label: StateName.count(.needsYou, waiting))
                .offset(offset)
                .allowsHitTesting(false)
        }
    }
}

/// iPhone Duo's tab bar's lens (#9837, after the system's tab bar there, the owner's reference): when another item is
/// chosen, a lens leaves the one that was, swelling and stretching along the bar, glides to the new one with a little
/// bounce and fades as it lands, where that item's own chosen look already is. At rest there is none: the bar looks as it
/// did. With Reduce Motion it never moves: the items only cross-fade (`SwitchMotion`). The glide is `TabBarGlide`'s, one
/// for every bar on screen; this view draws it while it runs, its timeline paused otherwise. `index` is the chosen item's
/// place on the bar (`ShellState.index`).
private struct TabBarLens: View {
    let index: Int
    let axis: Axis
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let glide = TabBarGlide.shared
        let running = glide.running && glide.to == index
        Color.clear
            .overlay(alignment: .topLeading) {
                if running {
                    TimelineView(.animation(paused: !running)) { context in
                        lens(TabBarGlide.frame(from: glide.from, to: glide.to, at: context.date.timeIntervalSince(glide.start)))
                    }
                }
            }
            .allowsHitTesting(false)
            .accessibilityHidden(true)
            .onChange(of: index) { old, new in if !reduceMotion { glide.begin(from: old, to: new) } }
    }

    private func lens(_ f: TabBarGlide.Frame) -> some View {
        let width = Theme.Shell.tabBarItemWidth, height = Theme.Shell.tabBarItemHeight
        // Swollen, it is longer along the bar than across it, as a drop pulled along.
        let pull = Theme.Motion.lensStretch * (f.swell - 1) / (Theme.Motion.lensSwellScale - 1)
        let along = f.swell + pull, across = f.swell - pull / 2
        return Color.clear
            .lensSurface(in: .capsule)
            .frame(width: width, height: height)
            .scaleEffect(x: axis == .vertical ? across : along, y: axis == .vertical ? along : across)
            .offset(x: axis == .horizontal ? f.at * width : 0, y: axis == .vertical ? f.at * height : 0)
            .opacity(f.opacity)
    }
}

/// The tab bar lens's glide (#9837), one for every iPhone Duo tab bar on screen: in the shell each page has its own bar
/// and the moving bar stands in while the pages change, so a bar that comes in midway draws the glide where it is by
/// then, from the same start. Only `begin` writes it, once per choice.
@MainActor @Observable final class TabBarGlide {
    static let shared = TabBarGlide()

    /// Where the lens is, in items from the bar's first; how swollen; how opaque.
    struct Frame: Equatable {
        var at: CGFloat
        var swell: CGFloat
        var opacity: Double
    }

    private(set) var from: CGFloat = 0
    private(set) var to = 0
    private(set) var start = Date.distantPast
    private(set) var running = false
    @ObservationIgnored private var serial = 0

    /// How long a glide lasts, its fade included.
    nonisolated static var length: TimeInterval { Theme.Motion.lensGlideTime + Theme.Motion.lensFadeTime }

    /// The lens `t` seconds after leaving `from` for `to`: it swells as it goes, glides with a bounce, and once there it
    /// shrinks back and fades.
    nonisolated static func frame(from: CGFloat, to: Int, at t: TimeInterval) -> Frame {
        let m = Theme.Motion.self
        let along = m.lensGlide.value(target: 1.0, time: max(0, t))
        let at = t >= length ? CGFloat(to) : from + (CGFloat(to) - from) * CGFloat(along)
        let landed = max(0, t - m.lensGlideTime) / m.lensFadeTime
        let rise = min(1, max(0, t) / m.lensSwellTime)
        let swollen = min(rise * (2 - rise), 1 - min(1, landed)) // ease out up, then down as it fades
        return Frame(at: at, swell: 1 + (m.lensSwellScale - 1) * CGFloat(swollen), opacity: max(0, 1 - landed))
    }

    /// A new choice: from where the lens is now (the item that was chosen, or partway on an earlier glide) to `to`. Every
    /// bar that sees the choice asks; the first one starts it.
    func begin(from old: Int, to new: Int) {
        guard old != new, !(running && to == new) else { return }
        let now = Date.now
        from = running ? Self.frame(from: from, to: to, at: now.timeIntervalSince(start)).at : CGFloat(old)
        to = new
        start = now
        running = true
        serial += 1
        let mine = serial
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(Self.length))
            if let self, self.serial == mine { self.running = false }
        }
    }
}

/// The persona's face on the chosen Assistant item of iPhone Duo's tab bar (#9837): still, a `Size.well` circle.
private struct TabBarPersonaFace: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let persona = model.persona
        Group {
            #if os(iOS) || os(visionOS)
            if !persona.isBuiltin, let frames = model.personas.frames(persona, .idle) {
                PersonaFramesView(frames: frames, animating: false, eligible: false)
            } else {
                AssistantAvatar(size: Theme.Size.well, design: persona.avatarDesign, still: true)
            }
            #else
            AssistantAvatar(size: Theme.Size.well, design: persona.avatarDesign, still: true)
            #endif
        }
        .frame(width: Theme.Size.well, height: Theme.Size.well)
        .clipShape(.circle)
        .accessibilityHidden(true)
    }
}

/// A page's count on iPhone Duo's tab bar (#4231): white on red on the rim of its symbol's well, at the top trailing
/// corner, as the system's tab bar badges its items (the iPad's too). The tabs' numbers: what waits on the user among the
/// Agents (`agentsWaiting`) and in Persona's own conversations (`TabBadges.assistant`); Messages has none, as its tab had
/// none. Its own view, as `WaitingBadge`: only it is drawn again when the count changes. `shell-agents-count`,
/// `shell-assistant-count` for the UI tests.
private struct TabBarBadge: View {
    @Environment(AppModel.self) private var model
    let tab: AppTab

    var body: some View {
        let waiting = switch tab {
        case .chats: model.agentsWaiting
        case .assistant: model.badges.assistant
        default: 0
        }
        if waiting > 0 {
            TabBarCount(count: waiting, label: StateName.count(.needsYou, waiting))
                .tabBarBadgeSpot()
                .allowsHitTesting(false)
                .accessibilityIdentifier(tab == .assistant ? "shell-assistant-count" : "shell-agents-count")
        }
    }
}

/// The count iPhone Duo's tab bar draws (#4231), the system's tab bar badge: `Theme.Size.badge` (18 pt) tall, round for one
/// digit and a capsule for more, its number `Theme.Font.labelHeavy` (12 pt) white on the system's red (`Theme.tabBarBadge`),
/// ringed `Theme.Shell.tabBarBadgeRing` in the page's color so it stands apart from the chosen item's circle and the
/// symbol under it. The same view on both displays (`DuoTabBarAlignmentTests`).
struct TabBarCount: View {
    let count: Int
    /// What VoiceOver says for it ("3 need you"); by default the exact number.
    var label: String? = nil

    var body: some View {
        if let text = CountBadge.text(count) {
            Text(text)
                .font(Theme.Font.labelHeavy.monospacedDigit())
                .contentTransition(.numericText(value: Double(count)))
                .foregroundStyle(Color.white)
                .lineLimit(1)
                .padding(.horizontal, Theme.Space.xs)
                .frame(minWidth: Theme.Size.badge, minHeight: Theme.Size.badge)
                .background(Theme.tabBarBadge, in: .capsule)
                .padding(Theme.Shell.tabBarBadgeRing)
                .background(Theme.pageBackground, in: .capsule)
                .fixedSize()
                .accessibilityLabel(label ?? String(count))
                .accessibilityShowsLargeContentViewer()
        }
    }
}

extension View {
    /// A tab bar item's count, in an overlay at the top trailing corner of its symbol's well: centred
    /// `Theme.Shell.tabBarBadgeInset` inside that corner, so it rests on the circle's rim and covers its edge only, not
    /// the symbol, at any width (a zero-sized anchor at the corner, the count centred on it).
    func tabBarBadgeSpot() -> some View {
        frame(width: 0, height: 0).offset(x: -Theme.Shell.tabBarBadgeInset, y: Theme.Shell.tabBarBadgeInset)
    }
}

/// The switch's motion, in the one step of its chain where the pill's glide always was (the switch across the top as it
/// was, #6735's stack). As iPhone Duo's tab bar (`upright`, #4231) also: a bar of fixed items at the default text size
/// (`Theme.Shell.switchTypeSize`), one container VoiceOver reads as a tab bar, the chosen item cross-fading with
/// Reduce Motion on, and away while the keyboard is up, as the tab bar it stands for goes under the keyboard: faded and
/// out of reach, never taken out of the tree (an item's menu may be open as the keyboard goes, docs/lessons/ios.md).
private struct SwitchMotion: ViewModifier {
    let chosen: AppTab
    let upright: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        if upright {
            let up = KeyboardState.shared.isUp
            content
                // With Reduce Motion the chosen item cross-fades and the lens stays still (`TabBarLens`, #9837).
                .animation(reduceMotion ? Theme.Motion.tabFade : Theme.Motion.state, value: chosen)
                .dynamicTypeSize(...Theme.Shell.switchTypeSize)
                .accessibilityElement(children: .contain)
                .accessibilityAddTraits(.isTabBar)
                // Only the fade moves with it, not what the segments do as the keyboard comes or goes.
                .animation(Theme.Motion.standard) {
                    $0.opacity(up ? 0 : 1).offset(y: up && !reduceMotion ? Theme.Space.l : 0)
                }
                .allowsHitTesting(!up)
                .accessibilityHidden(up)
        } else {
            content.animation(Theme.Motion.state, value: chosen)
        }
    }
}

/// A chosen segment that opens its menu: its hint, in the one step where it always was. Upright (#4231) also selected,
/// and its name and symbol shown large on a long press, as a tab bar's item shows them (DESIGN_SYSTEM.md 6.2): the
/// segment draws its symbol alone at the default text size.
private struct MenuSegmentAccess: ViewModifier {
    let hint: LocalizedStringKey
    let upright: Bool
    let title: String
    let symbol: String

    func body(content: Content) -> some View {
        if upright {
            content.accessibilityHint(hint).accessibilityAddTraits(.isSelected)
                .accessibilityShowsLargeContentViewer { ThemedIcon(symbol); Text(title) }
        } else {
            content.accessibilityHint(hint)
        }
    }
}

/// A segment's button: selected when chosen, in the one step where that always was. Upright (#4231) also its name and
/// symbol shown large on a long press.
private struct ButtonSegmentAccess: ViewModifier {
    let selected: Bool
    let upright: Bool
    let title: String
    let symbol: String

    func body(content: Content) -> some View {
        if upright {
            content.accessibilityAddTraits(selected ? .isSelected : [])
                .accessibilityShowsLargeContentViewer { ThemedIcon(symbol); Text(title) }
        } else {
            content.accessibilityAddTraits(selected ? .isSelected : [])
        }
    }
}

/// A round glass button of the shell's top bar (New Chat, New Session): the same size as the system toolbar's, so the Assistant
/// page's floating bar and the Agents page's navigation bar line up.
struct ShellCircleButton: View {
    var symbol: String
    var label: String
    var id: String
    var action: () -> Void
    @Environment(\.buiTheme) private var theme

    var body: some View {
        Button {
            Haptics.tap()
            action()
        } label: {
            ThemedIcon(symbol)
                .font(Theme.Font.heading)
                .fontWeight(.medium) // a touch heavier and rounder, like ChatGPT's line icons
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .foregroundStyle(AppStyle.current.usesBeautifulUI ? theme.colors.ink : Color.primary)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .modifier(CircleSurface())
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }
}

/// The top bar's left button on both pages (2026-11-01 user request, in place of ☰): the persona's face in Persona's
/// state, shared with the platform bar's centered `PersonaDot` (docs/apple/persona.md「States」), opening the drawer
/// (Chat History, Inbox…). The state is that of Persona's conversation on Assistant (`assistantCurrent`), on Agents too, so the
/// face stays the same while the strip slides. While the inbox has something for the user a small soft red dot sits
/// on the face's lower right (2026-11-04 user request, in place of a red tray beside it: smaller, paler, overlapping
/// the face so it takes no room and does not draw the eye), inside the round face so nothing reaches past it (it once
/// hung below the frame over Agents' search field), away from the corner the packages draw in (the thought bubble and
/// the z are top right). A ring in the page's color sets it apart from the face. The button reads all of it itself:
/// the bars around it are not drawn again with each push (#178).
struct ShellPersonaButton: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var typeSize
    var id: String
    var action: () -> Void

    /// How far up and left of the face frame's lower right corner the dot goes so that it lies on the face's rim at
    /// 45°, just inside the round face: its center `face / 2 - dot / 2` from the face's, down and right.
    nonisolated static func dotShift(face: CGFloat, dot: CGFloat) -> CGFloat {
        let r = face / 2, d = dot / 2
        let along = (r - d) / 2.squareRoot() // the dot's center right of (and below) the face's
        return along - (r - d) // from bottomTrailing, where the dot's center is r - d right of the face's; negative
    }

    var body: some View {
        let key = model.assistantCurrent
        let state = model.personaState(key, machineID: model.assistantMachineID(key))
        let inbox = InboxStore.shared.actionCount > 0
        // As big as the bar's round buttons (ShellCircleButton's frame): the tap target, growing with the text size.
        let k = Theme.sizeScale(typeSize)
        let dot = Theme.Size.dotXL * k
        Button {
            Haptics.tap()
            action()
        } label: {
            PersonaDot(size: Theme.Size.tapTarget * k, state: state)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .overlay(alignment: .bottomTrailing) {
            if inbox {
                let shift = Self.dotShift(face: Theme.Size.tapTarget * k, dot: dot)
                InboxDot(size: dot).offset(x: shift, y: shift)
            }
        }
        .accessibilityLabel("Chat History [Shell Components 60636b]")
        .accessibilityValue([String(localized: "\(model.assistantShownName), \(state.title)"), inbox ? String(localized: "Something in the Inbox needs you") : nil]
            .compactMap { $0 }.joined(separator: String(localized: ", ", comment: "List separator")))
        .accessibilityHint("Opens the sidebar")
        .accessibilityIdentifier(id)
    }
}

/// The inbox's dot on the persona's face: soft red, no number or symbol, ringed inside its size in the page's color so
/// it stands apart from the face without growing past it.
private struct InboxDot: View {
    var size: CGFloat

    var body: some View {
        Circle()
            .fill(Theme.inboxDot)
            .overlay(Circle().strokeBorder(Theme.pageBackground, lineWidth: Theme.Size.lineMedium))
            .frame(width: size, height: size)
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

/// A round button's surface: glass, or in the BeautifulUI style its raised white circle.
private struct CircleSurface: ViewModifier {
    func body(content: Content) -> some View { content.chromeSurface(in: Circle()) }
}

extension View {
    /// The floating chrome's button surface (the shell's round buttons, the image viewer's bars): interactive glass,
    /// or BeautifulUI's raised button in that style.
    @ViewBuilder func chromeSurface<S: InsettableShape>(in shape: S) -> some View {
        if AppStyle.current.usesBeautifulUI {
            buiSurface(shape, elevation: .button)
        } else {
            surface(interactive: true, in: shape)
        }
    }
}

extension View {
    /// The shell's chrome a size up (docs/apple/ios.md「New shell」): one Dynamic Type step, about 12 %, so its text, icons, buttons
    /// and the composer grow together and keep their proportions, ChatGPT's roomier scale. The conversation's messages
    /// keep the reading size. Small / Standard / Large and the system's size still apply under it.
    func shellScaled() -> some View { modifier(ShellScaled()) }

    /// A top bar of the shell: a size up like the rest of its chrome, but no larger than
    /// `Theme.Shell.topBarLargestTypeSize`, so it stays clear of Agents' search field at the accessibility sizes.
    func shellTopBarScaled() -> some View { dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize).shellScaled() }
}

/// Room for the shell's floating top bar (`AgentsTopBar`) on a page that keeps the system navigation bar for its search
/// field (Agents): a blank prompt line on the page's own navigation item, which makes that page's navigation bar taller
/// by the line, so the search field the bar holds and the list under it start below the top bar. While searching the
/// line goes, and the field rises into the top row.
///
/// The room is the page's, not the navigation container's: it used to be padding around the container, taken away for
/// a chat pushed on the list and given back on the way back, and UIKit does not lay out its navigation bar again in the
/// middle of a push or pop. Coming back, the bar sat where it had been for the chat (52 pt low), the list's inset with
/// it, which unfolded the search field, and everything sprang up once the pop was over (#340). A page's own navigation
/// item is carried through the transition by UIKit itself; the container keeps its frame and its safe area (which the
/// shell's status row changes, ShellStatus).
///
/// The line's height is UIKit's, the same at every text size; the top bar stops growing at
/// `Theme.Shell.topBarLargestTypeSize` to stay within it (`needed(at:)`, ShellNavigationClearanceTests).
struct ShellNavigationClearance: View {
    var active: Bool

    /// How far below the status bar the search field must start at this text size: the top bar's height (its buttons
    /// a size up, no larger than the top bars' largest; the inbox mark hanging off the face; the bar's top padding) and
    /// a step of space. The navigation bar with the prompt line must be at least this tall.
    static func needed(at size: DynamicTypeSize) -> CGFloat {
        let all = DynamicTypeSize.allCases
        let up = all[min((all.firstIndex(of: size) ?? 0) + 1, all.count - 1)]
        let k = Theme.sizeScale(min(up, Theme.Shell.topBarLargestTypeSize))
        return Theme.Size.tapTarget * k + Theme.Size.badge * k / 2 + Theme.Space.xxs * 2 + Theme.Space.s
    }

    /// The blank prompt line.
    static let line = " "

    var body: some View {
        #if os(iOS) || os(visionOS)
        PromptLine(active: active)
        #endif
    }
}

#if os(iOS) || os(visionOS)
private struct PromptLine: UIViewControllerRepresentable {
    var active: Bool

    func makeUIViewController(context: Context) -> Line { Line() }
    func updateUIViewController(_ line: Line, context: Context) { line.active = active }

    /// A child of the page's controller (the representable's), which owns the navigation item.
    final class Line: UIViewController {
        var active = false { didSet { apply() } }

        override func loadView() {
            view = UIView()
            view.isUserInteractionEnabled = false
        }

        override func didMove(toParent parent: UIViewController?) {
            super.didMove(toParent: parent)
            apply()
        }

        private func apply() {
            guard let item = parent?.navigationItem else { return }
            let prompt = active ? ShellNavigationClearance.line : nil
            if item.prompt != prompt { item.prompt = prompt }
        }
    }
}
#endif

/// The text size the app settled on (the system's, moved by Small / Standard / Large), set once at the root. `shellScaled` goes one
/// step up from this, not from the environment's own size: that one is not always what the page has. The drawer's
/// sidebar lives in a hosting controller whose environment follows its UIKit trait collection, which takes up the
/// step set inside it, so reading the environment there added a step on every refresh (Inbox's row 59 → 78 → 80 pt).
private struct ShellBaseTypeSizeKey: EnvironmentKey {
    static let defaultValue: DynamicTypeSize? = nil
}

extension EnvironmentValues {
    var shellBaseTypeSize: DynamicTypeSize? {
        get { self[ShellBaseTypeSizeKey.self] }
        set { self[ShellBaseTypeSizeKey.self] = newValue }
    }
}

private struct ShellScaled: ViewModifier {
    @Environment(\.dynamicTypeSize) private var size
    @Environment(\.shellBaseTypeSize) private var base

    func body(content: Content) -> some View {
        let all = DynamicTypeSize.allCases
        let i = all.firstIndex(of: base ?? size) ?? 0
        content.dynamicTypeSize(all[min(i + 1, all.count - 1)])
    }
}

extension ShellSwitch where WorkMenu == EmptyView {
    init(tab: Binding<AppTab>, axis: Axis = .horizontal, @ViewBuilder chatMenu: @escaping () -> ChatMenu) {
        self.init(tab: tab, axis: axis, chatMenu: chatMenu, workMenu: { EmptyView() })
    }
}
