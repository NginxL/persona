#if os(iOS)
import SwiftUI

/// A recording page's Summary (#5026, docs/apple/recording.md「Minutes」, docs/design/DESIGN_SYSTEM.md "Minutes cards
/// (#5026)"): a header card (the minutes' own title when it differs from the recording's, when · where, who was there, and
/// what is happening to them: Updating, Regenerate, #4062), then one card per group with something in it: Key Points,
/// Decisions, Open Questions, Action Items, each headed by its icon, title and count. An item's time shows that line in the
/// Transcript, lit; its disclosure opens the transcript lines around it in place, so the details read without leaving the
/// Summary. An item that cites a meeting photo shows its thumbnail (`MinutesPhoto`, #3862 phase 3); an action item offers
/// Add to To-Dos (#4062). Sections of the page's List, a card each: the list keeps its lazy rows
/// (docs/apple/performance.md, P6), and the cards come worked out (`RecordingMinutesCards.summary`, P4).
struct RecordingMinutesSummary: View {
    var entry: RecordingEntry
    var model: RecordingMinutesModel
    /// The hub has this revision of its text (Regenerate needs the whole transcript there).
    var synced: Bool
    var summary: RecordingMinutesCards.Summary
    /// A time tapped: the page shows that line in its Transcript.
    var show: (Double) -> Void

    var body: some View {
        // The recording's name: the minutes' own title shows only when it says something else.
        let title = summary.title.flatMap { $0 == entry.displayTitle ? nil : $0 }
        let updates = RecordingMinutesUpdates.shows(entry, model, synced: synced)
        if title != nil || summary.facts != nil || summary.attendees != nil || summary.cards.isEmpty || updates {
            Section {
                MinutesHeaderCard(title: title, facts: summary.facts, attendees: summary.attendees, empty: summary.cards.isEmpty) {
                    if updates { RecordingMinutesUpdates(entry: entry, model: model, synced: synced, boxed: true) }
                }
            }
        }
        ForEach(summary.cards) { card in
            Section {
                MinutesCard(card: card, recording: entry.id, offered: summary.offered, addable: summary.addable,
                            asking: model.asking, add: add, show: show)
            }
        }
    }

    /// Makes action items to-dos (`RecordingMinutesModel.add`): one, or all those left.
    private func add(_ ids: [String]) {
        Task { await model.add(ids, to: entry, synced: synced) }
    }
}

extension View {
    /// A row of the recording page's List drawn on the page's canvas (the switch and Copy / Share above the Summary): no
    /// row background, separator or side insets, so the control is the row, as wide as the page's other sections.
    func minutesCanvasRow() -> some View {
        listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: Theme.Space.sm, leading: Theme.Space.flush, bottom: Theme.Space.sm, trailing: Theme.Space.flush))
            .listRowSeparator(.hidden)
    }

    /// One of the Summary's cards: the one row of a section of its own, padded as a card. The page's list draws it, with
    /// its other sections' background and corners in every style: a `.surface` drawn in a row had its shadow cut off
    /// square by the row's edges.
    fileprivate func minutesCard() -> some View {
        frame(maxWidth: .infinity, alignment: .leading)
            .listRowInsets(EdgeInsets(top: Theme.Space.l, leading: Theme.Space.l, bottom: Theme.Space.l, trailing: Theme.Space.l))
    }
}

/// The Summary's first card: what the minutes say the meeting was, and what is happening to them (`below`). With nothing
/// in the groups it also says so, so the Summary never shows an empty page.
private struct MinutesHeaderCard<Below: View>: View {
    var title: String?
    var facts: String?
    var attendees: String?
    var empty: Bool
    @ViewBuilder var below: Below

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if let title {
                Text(title).font(Theme.Font.heading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let facts { fact(facts, Theme.Icon.calendarEvent).accessibilityIdentifier("minutes-when") }
            if let attendees { fact(attendees, Theme.Icon.people) }
            if empty {
                Text("Nothing to note: no points, decisions or action items came up.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            below
        }
        .minutesCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-card-header")
    }

    /// A line with its icon, read as its words alone (the icon's own name, "calendar day…", says nothing here).
    private func fact(_ text: String, _ symbol: String) -> some View {
        Label {
            Text(text).fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(symbol)
        }
        .font(Theme.Font.secondary).foregroundStyle(.secondary)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(text))
    }
}

/// A group's card: its header (read as one heading: the title and how many), then its items with a hairline between them;
/// the Action Items' card ends on Add All while more than one is left (#4062). Which items are open is this card's own
/// state, for as long as the page shows it.
private struct MinutesCard: View {
    var card: RecordingMinutesCards.Card
    /// The recording, whose photos the items cite.
    var recording: UUID
    /// The minutes' action items can become to-dos (the hub numbers them).
    var offered: Bool
    /// The action items left to add: Add All sends them.
    var addable: [String]
    /// A to-do is being made: no other one starts meanwhile.
    var asking: Bool
    var add: ([String]) -> Void
    var show: (Double) -> Void
    @State private var open: Set<String> = []
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            header
            ForEach(card.items) { item in
                if item.id != card.items.first?.id { Divider() }
                MinutesItemRow(item: item, recording: recording, open: open.contains(item.id), toggle: { toggle(item.id) }, show: show) {
                    if card.kind == .actions { todo(item) }
                }
            }
            if card.kind == .actions, offered, addable.count > 1 {
                Button { add(addable) } label: {
                    Label("Add All", systemImage: Theme.Icon.add).lineLimit(1).minimumScaleFactor(0.8)
                }
                .themedButton()
                .disabled(asking)
                .accessibilityIdentifier("minutes-add-all")
            }
        }
        .minutesCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-card-\(card.kind.rawValue)")
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(card.kind.symbol).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                .accessibilityHidden(true)
            Text(card.kind.title).font(Theme.Font.heading)
            Spacer(minLength: Theme.Space.s)
            Chip(text: card.items.count.formatted(), look: .neutral)
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }

    /// An action item as a to-do the user confirms (docs/assistant/meeting-minutes.md, #4062): Added once it is one, else
    /// Add to To-Dos when the hub names it. A hub that doesn't name its actions offers neither.
    @ViewBuilder private func todo(_ item: RecordingMinutesCards.Item) -> some View {
        if item.added {
            Chip(text: String(localized: "Added [Recording Minutes a4062d]"), look: .tint(Theme.positive), compact: true)
                .accessibilityIdentifier("minutes-added")
        } else if offered, let id = item.action {
            Button { add([id]) } label: {
                Label("Add to To-Dos", systemImage: Theme.Icon.add).lineLimit(1).minimumScaleFactor(0.8)
            }
            .themedButton()
            .disabled(asking)
            .accessibilityLabel(String(localized: "Add to To-Dos: \(item.text)"))
            .accessibilityIdentifier("minutes-add")
        }
    }

/// Opens or closes an item's lines: they fade in on the reveal curve and out on the conceal one, at once with Reduce
    /// Motion.
    private func toggle(_ id: String) {
        var next = open
        let opening = next.insert(id).inserted
        if !opening { next.remove(id) }
        withAnimation(reduceMotion ? nil : (opening ? Theme.Motion.reveal : Theme.Motion.conceal)) { open = next }
    }
}

/// An item: its words (and for an action, who and by when), the photo it cites, its time as a chip and the disclosure for
/// its lines, then `below` (an action item's Add to To-Dos) under all of it. At the accessibility sizes the time and the
/// disclosure go under the words, which then keep the card's whole width.
private struct MinutesItemRow<Below: View>: View {
    var item: RecordingMinutesCards.Item
    var recording: UUID
    var open: Bool
    var toggle: () -> Void
    var show: (Double) -> Void
    @ViewBuilder var below: Below
    @Environment(\.dynamicTypeSize) private var size

    var body: some View {
        let layout = size.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: Theme.Space.xs))
            : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: Theme.Space.s))
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            layout {
                words.frame(maxWidth: .infinity, alignment: .leading)
                controls
            }
            if open, !item.context.isEmpty {
                MinutesContext(lines: item.context)
                    .transition(.opacity)
            }
            below
        }
    }

    private var words: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(item.text).readingContent().font(Theme.Font.body).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            if let byline = item.byline {
                Text(byline).font(Theme.Font.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            // The photo it cites, as the minutes' sections show it: a tap opens it full screen; Photo removed once gone.
            if let photo = item.photo { MinutesPhoto(recording: recording, n: photo) }
        }
    }

    @ViewBuilder private var controls: some View {
        if let at = item.at {
            HStack(spacing: Theme.Space.flush) {
                time(at)
                if !item.context.isEmpty { disclosure(at) }
            }
        }
    }

    /// The time it was said: a tap switches the page to its Transcript and lights that line (the page's jump).
    private func time(_ at: Double) -> some View {
        let clock = RecordingFormat.clock(at)
        return Button { show(at) } label: {
            Chip(text: clock, look: .tint(Theme.accent), compact: true)
                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Show at \(clock) in the transcript"))
        .accessibilityIdentifier("minutes-time")
    }

    /// Opens the transcript lines around it under the item. A chevron beside the time, so an item stays one line taller
    /// than its words; VoiceOver hears what it opens and whether it is open.
    private func disclosure(_ at: Double) -> some View {
        Button(action: toggle) {
            ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Transcript around \(RecordingFormat.clock(at))"))
        .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
        .accessibilityIdentifier("minutes-item-context")
    }
}

/// An opened item's transcript lines on an inset well, as the meeting thread's opened lines: the line its time picks in the
/// primary color and the stronger weight (so it never rides on color alone), its neighbours in the secondary color.
private struct MinutesContext: View {
    var lines: [RecordingMinutesCards.Line]
    @Environment(\.dynamicTypeSize) private var size

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(lines) { line in row(line) }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-item-lines")
    }

    private func row(_ line: RecordingMinutesCards.Line) -> some View {
        let stamp = Text(RecordingFormat.clock(line.start)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
        let words = Text(line.text).readingContent()
            .font(line.said ? Theme.Font.secondaryStrong : Theme.Font.secondary)
            .foregroundStyle(line.said ? HierarchicalShapeStyle.primary : HierarchicalShapeStyle.secondary)
            .textSelection(.enabled)
            .fixedSize(horizontal: false, vertical: true)
        return Group {
            if size.isAccessibilitySize {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) { stamp; words }
            } else {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) { stamp; words }
            }
        }
        .accessibilityElement(children: .combine)
    }
}
#endif
