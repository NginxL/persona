import SwiftUI

/// The Side Sessions chip in row two of a parent's chat header (#7944, docs/apple/ios.md "Side sessions"): the side
/// sessions' mark, how many, and the needs-you dot while one of them waits for the user. It looks like the row's other
/// pill (`ActivityChips`) and only opens the sheet: it never opens by itself.
struct SideSessionsChip: View {
    var sides: SideSessions
    var open: () -> Void

    var body: some View {
        Button {
            Haptics.tap()
            open()
        } label: {
            HStack(spacing: Theme.Space.xxs) {
                ThemedIcon(Theme.Icon.sideSessions).font(Theme.Font.labelHeavy).foregroundStyle(.secondary)
                Text("\(sides.count)").monospacedDigit().lineLimit(1)
                if sides.needsYou {
                    StatusDot(state: .needsYou, size: Theme.Size.dotS).accessibilityHidden(true)
                }
            }
            .font(Theme.Font.labelStrong)
            .foregroundStyle(Color.primary)
            .padding(.horizontal, Theme.Space.m)
            .scaledFrame(height: Theme.Size.wellS)
            .background(Theme.Fill.chip, in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .fixedSize()
        .accessibilityLabel(Self.label(sides))
        .accessibilityIdentifier("side-sessions-chip")
    }

    /// "Side Sessions (3), A side session needs you".
    static func label(_ sides: SideSessions) -> String {
        let parts = [SideSessionsSheet.menuTitle(sides.count), sides.needsYou ? String(localized: "A side session needs you") : nil]
        return parts.compactMap { $0 }.joined(separator: String(localized: ", ", comment: "Separator between items of a spoken list"))
    }
}

/// A parent's side sessions, risen from its chat's chip or ⋯ (#7944): flat, sides of sides too, the ones that need the
/// user first, then the working ones, then by their last activity, the stopped ones last (`SideFold.ordered`). Each row
/// is the session's state mark, its title and its state's word; a tap closes the sheet, and the chat then shows the
/// side's own (`SideSessionPage`). Never a new side session from here: the desktop app makes them.
struct SideSessionsSheet: View {
    let parent: SessionKey
    var pick: (SessionKey) -> Void
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    /// "Side Sessions (3)": the ⋯ item's title, and the chip's spoken one.
    static func menuTitle(_ count: Int) -> String { String(localized: "Side Sessions (\(count))") }

    var body: some View {
        let sides = model.sides(of: parent)
        NavigationStack {
            Group {
                if sides.rows.isEmpty {
                    EmptyStateView(symbol: Theme.Icon.sideSessions, title: String(localized: "No Side Sessions"),
                                   message: String(localized: "Side sessions started from this session show up here."))
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    ScrollView {
                        VStack(spacing: Theme.Space.sm) {
                            ForEach(sides.rows) { row($0) }
                        }
                        .padding(Theme.Space.l)
                    }
                }
            }
            .background(Theme.pageBackground)
            .navigationTitle(String(localized: "Side Sessions"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    private func row(_ r: SideSessions.Row) -> some View {
        Button { pick(r.key) } label: {
            HStack(spacing: Theme.Space.s) {
                StatusDot(state: r.state, size: Theme.Size.dotL, waiting: r.waitsInBackground)
                    .scaledFrame(width: Theme.Size.markM)
                    .accessibilityHidden(true) // the state's word says it
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(r.title).font(Theme.Font.secondaryMedium).foregroundStyle(Color.primary).lineLimit(1)
                    Text(r.label).font(Theme.Font.label).foregroundStyle(ChatView.quiet(r.state) ? Color.secondary : r.state.tone.text)
                        .lineLimit(1)
                }
                Spacer(minLength: Theme.Space.xs)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
            }
            .padding(Theme.Space.m)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("side-session-row")
    }
}

/// A side session's own chat over its parent's (#7944): its whole conversation (title, cards, composer, Stop), presented
/// the way a task's agent is (`TaskDetailSheet`): a sheet that opens full on a phone, pulled down to 0.6 the parent's chat
/// behind stays usable; in regular width (iPad) pushed into the detail column instead (`detailPresentation`). ✕ (‹ when
/// pushed) goes back to the parent, and the title's ⌄ switches to its other sides.
struct SideSessionPage: View {
    let parent: SessionKey
    @State private var side: SessionKey
    @State private var detent: PresentationDetent = .large
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.detailPushed) private var pushed

    init(parent: SessionKey, side: SessionKey) {
        self.parent = parent
        _side = State(initialValue: side)
    }

    var body: some View {
        DetailStack {
            GeometryReader { geo in
                VStack(spacing: 0) {
                    if model.session(side) != nil {
                        page
                    } else {
                        // Its own header with the way out (#9971): pushed (regular width), the page hid the bar and drew
                        // none, so nothing led back to the parent.
                        DetailHeader(title: String(localized: "Agent"), mark: EmptyView(), menu: EmptyView())
                        Divider()
                        EmptyStateView(symbol: Theme.Icon.sideSessions, title: String(localized: "This agent is gone"),
                                       message: String(localized: "It may have been closed."))
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                }
                .frame(width: geo.size.width, height: geo.size.height, alignment: .top) // the sheet's height, not the page's wish
                .clipped()
            }
        }
        .presentationDetents([.fraction(0.6), .large], selection: $detent)
        .presentationDragIndicator(.visible)
        .presentationBackgroundInteraction(.enabled(upThrough: .fraction(0.6)))
        #if os(iOS)
        // A sheet has no edge-back of its own: a rightward swipe from the leading edge closes it like ✕ (#5603).
        // Not when pushed: the stack's own swipes go back there, and a drag over the whole page took the swipe back from
        // anywhere from it (#9971).
        .simultaneousGesture(DragGesture(minimumDistance: Theme.Size.edgeBackZone / 2).onEnded { drag in
            if !pushed && TaskDetailSheet.edgeSwipeCloses(start: drag.startLocation, translation: drag.translation) { dismiss() }
        }, including: pushed ? .subviews : .all)
        #endif
    }

    /// The side's chat, its header the page's only one (`HostedChat`): its own title, ✕ or ‹ back to the parent, and the
    /// parent's other sides behind the title's ⌄.
    @ViewBuilder private var page: some View {
        let host = HostedChat(title: nil, note: nil, closes: !pushed, leave: { dismiss() },
                              agents: model.sides(of: parent).rows.compactMap { model.session($0.key) },
                              select: { side = $0 }, menu: nil,
                              // It has no row of its own: in Agents it is its parent's chat, which is under this page.
                              openInAgents: { _ in dismiss() },
                              expand: nil)
        if pushed {
            ChatView(key: side, style: .dev, sidePage: true).id(side)
                .environment(\.splitDetail, SplitDetail(isFirstPage: true))
                .environment(\.hostedChat, host)
        } else {
            ChatView(key: side, style: .dev, sidePage: true).id(side)
                .environment(\.hostedChat, host)
        }
    }
}

/// What a parent's chat presents for its side sessions (#7944), one modifier on the page: the sheet of them (from the
/// header's chip or ⋯), the side picked there shown once the sheet has gone (`SideSessionPage`), and a landing that names
/// a side (a notification, a Needs You card, a link: `AppModel.sideLanding`). `on`: an Agents chat of its own, not one a
/// task's sheet or a side's page hosts.
struct SideSessionsPresentation: ViewModifier {
    let parent: SessionKey
    var on: Bool
    let ui: SideSessionsUI
    @State private var picked: SessionKey?
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content
            .sheet(isPresented: Binding(get: { on && ui.showList }, set: { ui.showList = $0 }), onDismiss: {
                // A tap closes the sheet first; the side's page comes once it has gone.
                if let p = picked {
                    picked = nil
                    ui.shown = p
                }
            }) {
                SideSessionsSheet(parent: parent) { key in
                    picked = key
                    ui.showList = false
                }
            }
            .detailPresentation(item: Binding(get: { on ? ui.shown : nil }, set: { ui.shown = $0 })) { side in
                SideSessionPage(parent: parent, side: side)
            }
            .onChange(of: model.sideLanding, initial: true) { _, landing in
                guard on, let landing, landing.parent == parent else { return }
                model.sideLanding = nil
                ui.shown = landing.side
            }
            #if DEBUG
            // `-showSideSessions sheet|side`: the sheet open, or the first side's page, once the samples are in.
            .task(id: parent) {
                guard on, let mode = SideSessionSamples.mode, mode == "sheet" || mode == "side" else { return }
                try? await Task.sleep(for: .seconds(2))
                if mode == "sheet" { ui.showList = true } else { ui.shown = model.sides(of: parent).rows.first?.key }
            }
            #endif
    }
}

/// What a parent's chat shows of its side sessions (#7944): the sheet open, and the side shown over the chat.
@Observable final class SideSessionsUI {
    var showList = false
    var shown: SessionKey?
    init(shown: SessionKey? = nil) { self.shown = shown }
}
