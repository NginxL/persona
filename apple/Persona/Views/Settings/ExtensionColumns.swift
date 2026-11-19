import SwiftUI
import PersonaWire

// UI version 8 (docs/extensions/extensions.md "UI version 8", #9158): a dashboard page's columns layout, a section's
// icon and link beside its title, and its rows side by side. On a narrow width a columns view is the stack it always was
// (`ExtensionPageView`'s list); once the page's width holds two columns of `Theme.Size.cardColumn` (scaled with the
// text, as every `Theme.Size`), its sections are cards in lines of two or three, each line as tall as its tallest card
// (docs/design/DESIGN_SYSTEM.md 3.3 "Adaptive card grids"). The width decides, never the kind of device: an iPad's
// narrow window is one column, a wide iPhone on its side may be two.

/// The decisions of UI version 8's layout, apart from drawing them.
nonisolated enum ExtensionColumns {
    /// The most columns a columns view has, and the most rows a section sets side by side (`Section.columns` is 1–3).
    static let most = 3

    /// How many columns of at least `minimum` fit in `width` with `gap` between them, as the card grids count them
    /// (`floor((width + gap) / (minimum + gap))`): at least one (a narrow width never squeezes a card), at most `most`.
    static func count(width: CGFloat, minimum: CGFloat, gap: CGFloat, most: Int) -> Int {
        guard width.isFinite, width > 0, minimum > 0 else { return 1 }
        let fits = Int(((width + gap) / (minimum + gap)).rounded(.down))
        return min(max(fits, 1), max(most, 1))
    }

    /// The narrowest column at this text size: `Theme.Size.cardColumn`, larger with the text like `scaledFrame`.
    @MainActor static func minimum(_ typeSize: DynamicTypeSize) -> CGFloat { Theme.Size.cardColumn * Theme.sizeScale(typeSize) }

    /// How many columns of cards `view` has on a page `pageWidth` wide (its content inside the page's margins): one,
    /// the page's list, unless its layout is columns and the width holds two (three when it holds three).
    @MainActor static func pageColumns(_ view: ExtensionView, pageWidth: CGFloat, typeSize: DynamicTypeSize) -> Int {
        guard view.layout == ExtensionLayout.columns else { return 1 }
        return count(width: pageWidth - 2 * Theme.Space.page, minimum: minimum(typeSize), gap: Theme.Space.m, most: most)
    }

    /// A line of cards: the sections in it by index, and whether it is one section that spans the whole line.
    struct Line: Equatable, Sendable {
        var sections: [Int]
        var full: Bool
    }

    /// A columns view's sections in lines of `columns`, in reading order (left to right, then down): a section that spans
    /// (`span: full`) takes a line of its own, which ends the line before it even when that line has room left (as the
    /// grid places a card that spans every column). The last line of a run may be short; its cards keep their width.
    static func lines(_ sections: [ExtensionSection], columns: Int) -> [Line] {
        let n = max(columns, 1)
        var out: [Line] = []
        var open: [Int] = []
        for (i, s) in sections.enumerated() {
            if s.span == ExtensionSectionSpan.full {
                if !open.isEmpty { out.append(Line(sections: open, full: false)); open = [] }
                out.append(Line(sections: [i], full: true))
                continue
            }
            open.append(i)
            if open.count == n { out.append(Line(sections: open, full: false)); open = [] }
        }
        if !open.isEmpty { out.append(Line(sections: open, full: false)) }
        return out
    }

    /// How many rows a section sets side by side when there is room: its `columns` (1–3), one when it says nothing.
    static func rowColumns(_ section: ExtensionSection) -> Int { min(max(section.columns ?? 1, 1), most) }

    /// A section's components as blocks: with rows side by side (`columns` above one), each run of consecutive rows is
    /// one block laid out by `ExtensionColumnsLayout`; every other component, and every row of a section without
    /// `columns`, is a block of its own across the whole width. By index into the components.
    enum Block: Equatable, Sendable {
        case one(Int)
        case rows([Int])
    }

    static func blocks(_ items: [ExtensionComponent], columns: Int) -> [Block] {
        guard columns > 1 else { return items.indices.map { .one($0) } }
        var out: [Block] = []
        for (i, c) in items.enumerated() {
            guard case .row = c else { out.append(.one(i)); continue }
            if case .rows(let run) = out.last { out[out.count - 1] = .rows(run + [i]) } else { out.append(.rows([i])) }
        }
        return out
    }

    /// The width a list's row content has on a page `width` wide: the page's margin and the row's own on each side.
    /// Only to tell whether rows could sit side by side there; `ExtensionColumnsLayout` then counts its own width.
    static func listRowWidth(_ width: CGFloat) -> CGFloat { width - 2 * (Theme.Space.page + Theme.Space.l) }
}

extension EnvironmentValues {
    /// The width of the extension page a list of its sections is drawn on (`ExtensionPageView`, for a view with UI
    /// version 8's columns): nil where nobody measured it (a launcher's page, an Inbox item), which keeps each row a
    /// list row of its own.
    @Entry var extensionPageWidth: CGFloat? = nil
    /// A row's `swipe` actions are under a swipe (`ExtensionRowActions`); false for rows side by side in one list row,
    /// which keep their menu and their accessibility actions.
    @Entry var extensionRowSwipes = true
}

/// Lines of `most` columns, each subview a column wide, every line as tall as its tallest subview (offered that height,
/// so a card that fills it lines up with its neighbours, as `EqualColumns` places its cells; that one counts columns by
/// what its cells hold, this one by the width); a short last line keeps its columns' width. With a `minimum`,
/// the columns are as many as keep that width each in the width offered (at most `most`): one, stacked, on a narrow
/// one. Nothing is measured into state: the width offered decides, so a rotation or a resized window lays it out again.
struct ExtensionColumnsLayout: Layout {
    var most: Int
    var minimum: CGFloat? = nil
    var spacing: CGFloat = Theme.Space.m

    func columns(for width: CGFloat?) -> Int {
        guard let minimum else { return max(most, 1) }
        return ExtensionColumns.count(width: width ?? 0, minimum: minimum, gap: spacing, most: most)
    }

    private struct Placement { var width: CGFloat; var lines: [(range: Range<Int>, height: CGFloat)] }

    private func placement(_ proposal: ProposedViewSize, _ subviews: Subviews) -> Placement {
        let n = columns(for: proposal.width)
        let width: CGFloat
        if let w = proposal.width, w.isFinite {
            width = w
        } else {
            // Asked for its ideal size: as wide as its widest subview in each column.
            let widest = subviews.map { $0.sizeThatFits(.unspecified).width }.max() ?? 0
            width = widest * CGFloat(n) + spacing * CGFloat(n - 1)
        }
        let cell = n > 1 ? max((width - spacing * CGFloat(n - 1)) / CGFloat(n), 0) : width
        var lines: [(Range<Int>, CGFloat)] = []
        var start = 0
        while start < subviews.count {
            let end = min(start + n, subviews.count)
            // In whole points (docs/apple/performance.md P7).
            let height = (subviews[start..<end].map { $0.sizeThatFits(ProposedViewSize(width: cell, height: nil)).height }.max() ?? 0)
                .rounded(.up)
            lines.append((start..<end, height))
            start = end
        }
        return Placement(width: width, lines: lines.map { (range: $0.0, height: $0.1) })
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let p = placement(proposal, subviews)
        let height = p.lines.reduce(0) { $0 + $1.height } + spacing * CGFloat(max(p.lines.count - 1, 0))
        return CGSize(width: p.width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let p = placement(ProposedViewSize(width: bounds.width, height: nil), subviews)
        let n = columns(for: bounds.width)
        let cell = n > 1 ? max((bounds.width - spacing * CGFloat(n - 1)) / CGFloat(n), 0) : bounds.width
        var y = bounds.minY
        for line in p.lines {
            for (column, i) in line.range.enumerated() {
                let x = bounds.minX + CGFloat(column) * (cell + spacing)
                subviews[i].place(at: CGPoint(x: x, y: y), anchor: .topLeading,
                                  proposal: ProposedViewSize(width: cell, height: line.height))
            }
            y += line.height + spacing
        }
    }
}

/// Where a section's head is drawn: a list's section header (a page on a narrow width), over a section's card on a page
/// that is not a list (a machine's page, a card, an assistant's reply), or inside a card of a columns view.
enum ExtensionSectionHeadPlace {
    case list, stack, card
}

/// A section's title with UI version 8's icon before it, in its named color (the row icons' set and colors; the color
/// never goes on the words), and its link at the end ("Calendar ›"). A section with neither keeps the plain title its
/// place always drew; this is drawn only for one that has them, or for a card's head.
struct ExtensionSectionHead: View {
    var section: ExtensionSection
    /// The title drawn: the section's, or the heading it stands in for (a launcher's section).
    var title: String?
    var place: ExtensionSectionHeadPlace
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?

    /// The section draws something here beyond its plain title.
    nonisolated static func wanted(_ s: ExtensionSection) -> Bool { s.icon?.isEmpty == false || s.link != nil }

    var body: some View {
        let row = HStack(spacing: Theme.Space.s) {
            if let icon = section.icon, !icon.isEmpty {
                ThemedIcon(ExtensionUIStyle.icon(icon))
                    .foregroundStyle(ExtensionUIStyle.color(section.iconColor) ?? Color.secondary)
                    .accessibilityHidden(true)
            }
            if let title, !title.isEmpty {
                words(title)
            }
            Spacer(minLength: Theme.Space.s)
            if let l = section.link, let model {
                ExtensionSectionLinkButton(sectionLink: l, key: section.key, font: linkFont, model: model, link: $link)
            }
        }
        // A list's header keeps the list's own header font (and its words `SettingsHeading`'s): the icon and the link
        // take it too.
        row.fontIfSet(font)
    }

    @ViewBuilder private func words(_ title: String) -> some View {
        switch place {
        case .list: SettingsHeading(title)
        case .stack: Text(title).foregroundStyle(.secondary).lineLimit(1).accessibilityAddTraits(.isHeader)
        case .card: Text(title).foregroundStyle(Color.primary).lineLimit(2).accessibilityAddTraits(.isHeader)
        }
    }

    private var font: Font? {
        switch place {
        case .list: nil
        // As `ExtensionViewStack`'s plain titles.
        case .stack: Theme.Font.sectionHeader
        // As a card's title in the card grids (Machines).
        case .card: Theme.Font.heading
        }
    }

    private var linkFont: Font? { place == .card ? Theme.Font.secondary : nil }
}

/// A section's link: its words and › in the accent, pressed as a row is (its action through `ui.action`, or the page
/// its `push` opens), a spinner in place of › while its action is on its way. At least a finger's size (44 pt, larger
/// with the text) and named by its own words.
struct ExtensionSectionLinkButton: View {
    var sectionLink: ExtensionSectionLink
    var key: String?
    /// Its own font (a card's head); nil: the one around it (a list's header, a stack's title).
    var font: Font?
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?

    var body: some View {
        ExtensionActionButton(action: sectionLink.action, title: sectionLink.title, model: model, link: $link,
                              identifier: "plugin-ui-section-link-" + (key ?? sectionLink.action.id)) {
            HStack(spacing: Theme.Space.xxs) {
                Text(sectionLink.title).lineLimit(1)
                if model.running == sectionLink.action.id {
                    ProgressView().controlSize(.small)
                } else {
                    ThemedIcon(Theme.Icon.disclosure).imageScale(.small).accessibilityHidden(true)
                }
            }
            .fontIfSet(font)
            // A finger's size wherever it is drawn: a list's header and a card's head grow to it.
            .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget, alignment: .trailing)
            .frame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget, alignment: .trailing)
            .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .textCase(nil)
        .accessibilityLabel(sectionLink.title)
    }
}

/// A section's components on a page that is not a list (`ExtensionViewStack`, a card of a columns view): one under
/// another, a divider a line between them; with UI version 8's `columns`, its runs of rows side by side where the
/// width holds them (`ExtensionColumnsLayout`), stacked as before where it does not.
struct ExtensionSectionItems: View {
    @Environment(\.assistantHTMLRendering) private var htmlContext
    @Environment(\.dynamicTypeSize) private var typeSize
    var section: ExtensionSection
    var index: Int
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?

    var body: some View {
        let items = section.items.enumerated().compactMap { j, item -> ExtensionParts.Item? in
            ExtensionViewStack.leftOut(item, html: htmlContext != nil) ? nil
                : ExtensionParts.Item(id: ExtensionParts.Item.id(item, section: index, index: j), component: item)
        }
        let columns = ExtensionColumns.rowColumns(section)
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            ForEach(ExtensionPartsBlock.of(items, columns: columns)) { block in
                Group {
                    switch block.content {
                    case .one(let entry):
                        if case .divider = entry.component { Divider() } else {
                            ExtensionItemRow(item: entry.component, model: model, link: $link)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    case .rows(let entries):
                        ExtensionColumnsLayout(most: columns, minimum: ExtensionColumns.minimum(typeSize)) {
                            ForEach(entries) { entry in
                                // Its own height, at the top of the line (`ExtensionSectionCard`: an exact height
                                // would scale a row's title down).
                                ExtensionItemRow(item: entry.component, model: model, link: $link)
                                    .fixedSize(horizontal: false, vertical: true)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                        }
                    }
                }
                .transition(.opacity)
            }
            if let more = section.more, let model { ExtensionMoreRow(more: more, model: model, link: $link) }
        }
    }
}

/// A section's drawn components as `ExtensionColumns.Block`s, with their identities: a run of rows is known by its
/// first row's.
struct ExtensionPartsBlock: Identifiable {
    enum Content {
        case one(ExtensionParts.Item)
        case rows([ExtensionParts.Item])
    }

    var content: Content
    var id: String {
        switch content {
        case .one(let item): item.id
        case .rows(let items): "rows:" + (items.first?.id ?? "")
        }
    }

    static func of(_ items: [ExtensionParts.Item], columns: Int) -> [ExtensionPartsBlock] {
        ExtensionColumns.blocks(items.map(\.component), columns: columns).map { b in
            switch b {
            case .one(let i): ExtensionPartsBlock(content: .one(items[i]))
            case .rows(let run): ExtensionPartsBlock(content: .rows(run.map { items[$0] }))
            }
        }
    }
}

/// A columns view on a page wide enough for two columns: its sections as cards in lines, in reading order, a section
/// that spans taking a line of its own; each line as tall as its tallest card. Lines are the lazy stack's rows, so a
/// long dashboard lays out only what is on screen.
struct ExtensionColumnsGrid: View {
    var view: ExtensionView
    var model: any ExtensionViewControlling
    /// Cards to a line (2 or 3), from the page's width.
    var columns: Int
    @Binding var link: LinkTarget?

    struct LineEntry: Identifiable {
        var line: ExtensionColumns.Line
        var cards: [ExtensionViewStack.SectionEntry]
        /// A line is known by its first card.
        var id: String { "line:" + (cards.first?.id ?? "") }
    }

    var body: some View {
        let lines = ExtensionColumns.lines(view.sections, columns: columns).map { line in
            LineEntry(line: line, cards: line.sections.map { ExtensionViewStack.SectionEntry(index: $0, section: view.sections[$0]) })
        }
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Theme.Space.m) {
                if let search = view.search, model.managesConfirmation {
                    ExtensionSearchRow(search: search, model: model, link: $link)
                }
                if let f = view.filter { ExtensionFilterChips(filter: f, model: model, link: $link) }
                ForEach(lines) { entry in
                    ExtensionColumnsLayout(most: entry.line.full ? 1 : columns) {
                        ForEach(entry.cards) { card in
                            ExtensionSectionCard(section: card.section, index: card.index, model: model, link: $link)
                        }
                    }
                    .transition(.opacity)
                }
            }
            .padding(Theme.Space.page)
        }
        .themedGroupedPage()
    }
}

/// One section of a columns view as a card: its head inside at the top (icon, title, link), its components, its
/// footer; as tall as the line it is in, its content at the top.
struct ExtensionSectionCard: View {
    var section: ExtensionSection
    var index: Int
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if section.title?.isEmpty == false || ExtensionSectionHead.wanted(section) {
                ExtensionSectionHead(section: section, title: section.title, place: .card, model: model, link: $link)
            }
            ExtensionSectionItems(section: section, index: index, model: model, link: $link)
            if let f = section.footer, !f.isEmpty {
                Text(f).font(Theme.Font.label).foregroundStyle(.secondary)
            }
        }
        // Its content at its own height, the card as tall as the line: offered the line's exact height, a stack hands
        // its rows equal shares and a row's title (`minimumScaleFactor`) scales down to fit one.
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .environment(\.extensionCardInset, CardStyle.padding)
        .card()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("extension-columns-card-" + (section.key ?? String(index)))
    }
}

private extension View {
    /// `font` when there is one; nil leaves the font around it (`.font(nil)` would put the default back).
    @ViewBuilder func fontIfSet(_ font: Font?) -> some View {
        if let font { self.font(font) } else { self }
    }
}

nonisolated extension ExtensionView {
    /// Its page's width decides how it is drawn (UI version 8): a columns layout, or a section's rows side by side.
    var wantsPageWidth: Bool {
        layout == ExtensionLayout.columns || sections.contains { ExtensionColumns.rowColumns($0) > 1 }
    }
}

/// An extension's page whose View its width lays out (UI version 8, `wantsPageWidth`): a columns view is a grid of cards
/// once the page's content holds two columns, the page's list otherwise (one column, as a stack), whose sections set
/// their rows side by side by the same width. Measured by the page's own frame, nothing written back.
struct ExtensionWidthPage<Narrow: View>: View {
    @Environment(\.dynamicTypeSize) private var typeSize
    var view: ExtensionView
    var model: any ExtensionViewControlling
    @Binding var link: LinkTarget?
    /// The page's list.
    @ViewBuilder var list: Narrow

    var body: some View {
        GeometryReader { g in
            let columns = ExtensionColumns.pageColumns(view, pageWidth: g.size.width, typeSize: typeSize)
            if columns > 1 {
                ExtensionColumnsGrid(view: view, model: model, columns: columns, link: $link)
            } else {
                list.environment(\.extensionPageWidth, g.size.width)
            }
        }
    }
}
