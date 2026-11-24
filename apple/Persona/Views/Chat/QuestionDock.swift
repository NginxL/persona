import SwiftUI

// The assistant's To Decide entry and its pager (#12831, docs/design/DESIGN_SYSTEM.md 4.8): what waits for the owner in an
// assistant conversation (the conversation's own cards and its tasks' questions and approvals) is one entry in the row of
// entries over the composer, and one card at a time over it, paged with dots, "2 / 5" and ‹ ›. Before, the tasks' oldest
// question was a card above the entries and the conversation's own cards a stack under them, with the wrap-up chips in
// between: with several Agents asking they piled up row after row in the middle of the footer.

/// A question card's page, picks and typed answers while its form is not drawn (#12831): the pager draws one card at a
/// time and none while collapsed. In memory only, a few dozen at most; a card's form takes its own back when drawn again.
@MainActor final class QuestionFormMemory {
    static let shared = QuestionFormMemory()
    struct Kept {
        var picks: [String: Set<String>]
        var other: [String: String]
        var page: Int
        var reviewing: Bool
    }
    private var kept: [String: Kept] = [:]
    private var order: [String] = []

    func keep(_ id: String, _ value: Kept) {
        guard !value.picks.values.allSatisfy(\.isEmpty) || value.other.values.contains(where: { !$0.isEmpty }) || value.page > 0 else {
            kept[id] = nil
            return
        }
        if kept[id] == nil { order.append(id) }
        kept[id] = value
        while order.count > 40 { kept[order.removeFirst()] = nil }
    }

    func take(_ id: String) -> Kept? { kept[id] }
}

/// The pager's rules, apart from the views: the order, which card is shown, whether it is folded away and its Skip.
nonisolated enum QuestionDockLogic {
    struct Item: Equatable, Sendable {
        var id: String
        var createdAt: Date?
    }

    /// The conversation's own cards first, as they come (they hold this conversation's turn), then the tasks' questions
    /// and approvals oldest first. A card in both (an Agent asking for this conversation and one of its tasks) once.
    static func order(own: [Item], tasks: [Item]) -> [String] {
        var seen = Set<String>()
        let rest = tasks.enumerated().sorted { a, b in
            let (ta, tb) = (a.element.createdAt ?? .distantPast, b.element.createdAt ?? .distantPast)
            return ta != tb ? ta < tb : a.offset < b.offset
        }.map(\.element)
        return (own + rest).map(\.id).filter { seen.insert($0).inserted }
    }

    /// The card shown: the one the owner was on while it is there, else the card that took its place (`index`, where it
    /// stood: answering a card moves on to the next, and the last moves back to the one before); nil with none.
    static func shown(_ ids: [String], current: String?, index: Int) -> Int? {
        guard !ids.isEmpty else { return nil }
        if let current, let at = ids.firstIndex(of: current) { return at }
        return min(max(index, 0), ids.count - 1)
    }

    /// The owner collapsed the pager while `seen` were in it: it stays collapsed while one of them still waits, and opens
    /// again for a fresh round once all of them are gone. An empty pager is never collapsed.
    static func collapsed(_ ids: [String], folded: Bool, seen: Set<String>) -> Bool {
        folded && !ids.isEmpty && !seen.isDisjoint(with: ids)
    }

    /// How many came in since the owner collapsed it: the entry says "+N".
    static func arrived(_ ids: [String], seen: Set<String>) -> Int {
        ids.filter { !seen.contains($0) }.count
    }

    /// The dots under the pager: all of them up to `most`, else a window of `most` around the shown one.
    static func dots(count: Int, shown: Int, most: Int = 7) -> Range<Int> {
        guard count > most else { return 0..<count }
        let start = min(max(shown - most / 2, 0), count - most)
        return start..<start + most
    }

    /// Why a card's Skip is greyed in the pager (the owner's call, #12831: Skip is not unified across kinds yet).
    enum NoSkip: Equatable, Sendable {
        /// A question read off the terminal: no runtime can release it without an answer yet.
        case terminal
        /// A tool approval, Deny is the answer that does not let it run (a command read off the terminal too).
        case permission
        /// A plan approval, Send Back is the answer that does not go ahead.
        case plan
    }

    /// Skip greyed with its reason, or nil where the card keeps today's behavior (Skip where the request can be cancelled).
    static func noSkip(kind: InteractionKind, command: Bool, canSkip: Bool) -> NoSkip? {
        guard !canSkip else { return nil }
        switch kind {
        case .screen: return command ? .permission : .terminal
        case .permission: return .permission
        case .plan: return .plan
        case .question, .elicitation: return nil
        }
    }
}

/// What the To Decide entry and its pager share in one conversation: collapsed or not, and the card shown. A class, so the
/// pager and the entry (in the action bar) read the same one.
@Observable final class QuestionDockState {
    /// The owner collapsed the pager, with these in it (`QuestionDockLogic.collapsed`).
    var folded = false
    var seen: Set<String> = []
    /// The card the owner is on, and where it stood.
    var current: String?
    var index = 0
    /// A tray of the action bar is open over the entries: the pager steps aside, one of them at a time.
    var trayOpen = false

    /// Opens the pager, on `id` when given.
    func show(_ id: String? = nil) {
        folded = false
        seen = []
        if let id { current = id }
    }

    /// Collapses it into the entry.
    func fold(_ ids: [String]) {
        folded = true
        seen = Set(ids)
    }

    func collapsed(_ ids: [String]) -> Bool { QuestionDockLogic.collapsed(ids, folded: folded, seen: seen) }

    /// The pager shows: something in it, not collapsed, no tray open over the entries.
    func showing(_ ids: [String]) -> Bool { !ids.isEmpty && !trayOpen && !collapsed(ids) }

    func go(to at: Int, in ids: [String]) {
        guard ids.indices.contains(at) else { return }
        current = ids[at]
        index = at
    }
}

extension EnvironmentValues {
    /// The card is the pager's (#12831): its fold collapses the pager, View in Chat collapses it too, and a card whose Skip
    /// is not available says why.
    @Entry var cardDock: CardDock? = nil
}

struct CardDock {
    var collapse: @MainActor () -> Void
    /// View in Chat of a task's card (#12625): its Agent's chat pushed over the conversation, at the message that asked.
    var source: (@MainActor () -> Void)? = nil
}

/// One card of the pager.
struct DockCard: Identifiable {
    var interaction: Interaction
    var enabled = true
    var localOnly = false
    var blocked: AnswerBlock? = nil
    /// The line under its title: the task it asks for, or whose it is (a side session's card).
    var from: String? = nil
    /// One of the tasks' cards, asked in its Agent's chat (not the conversation's own).
    var task = false
    var id: String { interaction.id }
}

/// The pager over the assistant's composer: one card at a time, the To Decide entry in the action bar opening and
/// collapsing it. Capped as the task question was (#9392, `FooterRoom.cardsRoom`): it is the only place of cards there.
struct QuestionDock: View {
    /// The conversation's own cards, as the chat lists them; its tasks' are read here (`cards(own:conversation:)`), so
    /// the chat's page does not follow the tasks' every change.
    var own: [DockCard]
    var conversation: String
    var room: FooterRoom
    var state: QuestionDockState
    /// A voice call is on: the entries are out of the way, so the pager stays open.
    var inCall = false
    @State private var navHeight: CGFloat = 0
    @State private var shown: CGFloat = 0
    /// What the footer holds besides the pager, held as `PendingCards.footerRest` is (#1580).
    @State private var footerRest: CGFloat = 0
    @State private var containerLength: CGFloat = 0
    /// Pushes an Agent's chat over the conversation (`conversationAgentPush`, #12030).
    @Environment(\.pushInboxChat) private var pushChat

    var body: some View {
        let cards = Self.cards(own: own, conversation: conversation)
        let ids = cards.map(\.id)
        if let at = QuestionDockLogic.shown(ids, current: state.current, index: state.index), inCall || state.showing(ids) {
            let card = cards[at]
            let resize = resize(card.id)
            let cap = cap(resize)
            let keyboard = keyboardRoom
            let nav = ids.count > 1 ? navHeight + Theme.Space.xs : 0
            let area = cap > 0 ? max(cap - nav, Theme.Size.tapTarget) : 0
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                if ids.count > 1 {
                    pager(at: at, ids: ids)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
                            if abs(h - navHeight) >= 1 { navHeight = h }
                        }
                }
                // One card drawn at a time: a question's page, picks and typed answers wait in `QuestionFormMemory` while
                // the owner pages through the others or collapses the pager. (Every card kept mounted at no height showed
                // the hidden ones' buttons to VoiceOver.)
                page(card, resize: resize, ids: ids)
                    .id(card.id)
                    .transition(.opacity)
                .environment(\.cardRoom, containerLength)
                .environment(\.cardArea, area)
                .environment(\.cardFits, keyboard ? area - Theme.Space.l : 0)
                .environment(\.cardTakesArea, true)
                .simultaneousGesture(swipe(at: at, ids: ids), isEnabled: ids.count > 1)
                #if os(iOS)
                // On a phone a sideways drag anywhere opens the drawer or pages the strip (`DrawerPan`): on the card it pages.
                .background { if ids.count > 1 { SwipePageMark() } }
                #endif
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("question-dock")
            .transition(.move(edge: .bottom).combined(with: .opacity))
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
                shown = h
                if abs(room.taskShown - h) >= 1 { room.taskShown = h }
            }
            .onChange(of: room.footer - shown - room.stackShown, initial: true) { _, o in
                if abs(o - footerRest) >= 1 { footerRest = o }
            }
            // Where it stands, so a card answered elsewhere leaves the next in its place.
            .onChange(of: card.id, initial: true) { _, id in
                state.current = id
                state.index = ids.firstIndex(of: id) ?? at
            }
            .onDisappear { room.taskShown = 0 }
            .background {
                Color.clear.containerRelativeFrame(.vertical)
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { containerLength = $0 }
                    .hidden()
            }
        }
    }

    /// The pager's card.
    private func page(_ c: DockCard, resize: CardResize?, ids: [String]) -> some View {
        var dock = CardDock(collapse: { collapse(ids) })
        if c.task {
            let ix = c.interaction
            dock.source = { openAgent(ix) }
        }
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            InteractionPanel(interaction: c.interaction, from: c.from)
                .disabled(!c.enabled)
                .environment(\.cardLocalOnly, c.localOnly)
                .chatMark("chat.card." + c.id)
            if let blocked = c.blocked { CardBlockedLine(block: blocked, key: c.interaction.sessionKey) }
        }
        .environment(\.cardResize, resize)
        .environment(\.cardDock, dock)
    }

    /// The dots, "2 / 5" and ‹ ›.
    private func pager(at: Int, ids: [String]) -> some View {
        HStack(spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.xs) {
                ForEach(QuestionDockLogic.dots(count: ids.count, shown: at), id: \.self) { i in
                    Capsule()
                        .fill(i == at ? Theme.accent : Theme.Fill.chip)
                        .scaledFrame(width: i == at ? Theme.Size.dotL * 2 : Theme.Size.dotL, height: Theme.Size.dotL)
                }
            }
            .accessibilityHidden(true)
            Text("\(at + 1) / \(ids.count)")
                .font(Theme.Font.captionMedium).monospacedDigit().foregroundStyle(.secondary).lineLimit(1).fixedSize()
                .accessibilityIdentifier("question-dock.position")
            Spacer(minLength: Theme.Space.s)
            arrow(Theme.Icon.back, label: "Previous [Question Pager]", id: "question-dock.previous", enabled: at > 0) {
                move(to: at - 1, ids: ids)
            }
            arrow(Theme.Icon.forward, label: "Next [Question Pager]", id: "question-dock.next", enabled: at < ids.count - 1) {
                move(to: at + 1, ids: ids)
            }
        }
        .padding(.horizontal, Theme.Space.xs)
    }

    private func arrow(_ icon: String, label: LocalizedStringKey, id: String, enabled: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            ThemedIcon(icon).font(Theme.Font.captionStrong)
                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                .background(Theme.Fill.chip, in: .circle)
                .tapTargetFrame()
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .foregroundStyle(enabled ? AnyShapeStyle(Theme.primaryText) : AnyShapeStyle(.tertiary))
        .disabled(!enabled)
        .accessibilityLabel(Text(label))
        .accessibilityIdentifier(id)
        // a finger's room without a taller row
        .padding(.vertical, -(Theme.Size.tapTarget - Theme.Size.wellXS) / 2)
    }

    private func move(to at: Int, ids: [String]) {
        guard ids.indices.contains(at) else { return }
        Haptics.tap()
        withAnimation(Theme.Motion.standard) { state.go(to: at, in: ids) }
    }

    /// A sideways swipe on the card moves to the one beside it; the card's own scroll keeps up and down.
    private func swipe(at: Int, ids: [String]) -> some Gesture {
        DragGesture(minimumDistance: Theme.Space.xl)
            .onEnded { g in
                let dx = g.translation.width, dy = g.translation.height
                guard abs(dx) > Theme.Size.tapTarget * 1.5, abs(dx) > abs(dy) * 2 else { return }
                move(to: dx < 0 ? at + 1 : at - 1, ids: ids)
            }
    }

    /// View in Chat on a task's card (#12625): its Agent's chat as a drill-down over the conversation (`openInPage`), landing
    /// on the message that asked (`MessageJump.card`); Back comes back to the pager on the same card.
    private func openAgent(_ ix: Interaction) {
        let model = AppModel.shared
        model.messageJump = MessageJump(key: ix.sessionKey, item: "", card: ix)
        model.openInPage(ix.sessionKey, push: conversationAgentPush(pushChat))
    }

    private func collapse(_ ids: [String]) {
        Haptics.tap()
        withAnimation(Theme.Motion.state) { state.fold(ids) }
    }

    private var keyboardRoom: Bool { KeyboardState.shared.isUp && room.footer > 0 }

    private var rest: CGFloat { footerRest > 0 ? footerRest : room.footer - shown - room.stackShown }

    /// How tall the pager may be; 0 before the page is measured. As a lone question card (#6277): down to the
    /// conversation's `cardThreadRoom`, never less than half the page; the keyboard up, the conversation's room; the
    /// owner's height (`CardHeight`) wins with the keyboard down. In a voice call, half the page for its captions.
    private func cap(_ resize: CardResize?) -> CGFloat {
        guard room.height > 0, room.footer > 0 else { return 0 }
        let whole = room.cardsRoom(rest: rest, keyboard: keyboardRoom)
        if keyboardRoom { return max(Theme.Size.tapTarget, whole) }
        if inCall { return max(Theme.Size.tapTarget, room.height / 2) }
        let fallback = max(room.height / 2, whole)
        guard let resize else { return fallback }
        return CardHeight.shared.cap(default: fallback, range: resize.range, id: resize.id)
    }

    /// The grabber's terms (#12306), the keyboard down and no call.
    private func resize(_ id: String) -> CardResize? {
        guard room.height > 0, room.footer > 0, !keyboardRoom, !inCall else { return nil }
        return CardResize(id: id, height: { shown }, range: FooterRoom.resizeRange(tallest: room.tallestRoom(rest: rest)))
    }
}

extension QuestionDock {
    /// The pager's cards: the conversation's own (`own`, as the chat lists them) and then its tasks' questions and
    /// approvals, each under its task's name.
    @MainActor static func cards(own: [DockCard], conversation: String) -> [DockCard] {
        let store = TasksStore.shared
        var tasks: [String: DockCard] = [:]
        var items: [QuestionDockLogic.Item] = []
        for todo in store.tasks(of: conversation) {
            for ix in store.interactions(todo) where tasks[ix.id] == nil {
                tasks[ix.id] = DockCard(interaction: ix, from: todo.title, task: true)
                items.append(.init(id: ix.id, createdAt: ix.createdAt))
            }
        }
        var byID = tasks
        for card in own { byID[card.id] = card }
        let order = QuestionDockLogic.order(own: own.map { .init(id: $0.id, createdAt: $0.interaction.createdAt) }, tasks: items)
        return order.compactMap { byID[$0] }
    }
}

/// The To Decide entry, first in the row of entries over the assistant's composer: how many cards wait, opening and
/// collapsing the pager; collapsed, "+N" for the cards that came in since.
struct QuestionDockEntry: View {
    var count: Int
    var arrived: Int
    var open: Bool
    var toggle: () -> Void

    var body: some View {
        Button(action: toggle) {
            HStack(spacing: Theme.Space.xs) {
                Text("To Decide [Question Pager]")
                if let text = CountBadge.text(count) { Text(text).monospacedDigit() }
                ThemedIcon(Theme.Icon.expand).rotationEffect(.degrees(open ? 0 : 180))
                if !open, arrived > 0 {
                    Text("+\(arrived)").monospacedDigit().accessibilityIdentifier("action-bar.to-decide.arrived")
                }
            }
            .dockCapsule(open ? .selected : .waiting(.needsYou))
            .background(Theme.pageBackground, in: .capsule)
            .fixedSize()
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(open ? .isSelected : [])
        .accessibilityIdentifier("action-bar.to-decide")
    }
}
