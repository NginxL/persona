import SwiftUI

/// How warm a relationship is.
public enum RecordStrength: Int, CaseIterable, Sendable {
    case none = 0, veryWeak, weak, strong

    public var label: String {
        switch self {
        case .strong: "Very strong"
        case .weak: "Weak"
        case .veryWeak: "Very weak"
        case .none: "No communication"
        }
    }

    func tint(_ c: BUIColors) -> Color {
        switch self {
        case .strong: c.green
        case .weak: c.orange
        case .veryWeak: c.red
        case .none: c.ink3
        }
    }
}

/// One company in the records grid.
public struct RecordItem: Identifiable, Sendable {
    public var id: String
    public var name: String
    public var tags: [String]
    public var lastInteraction: String
    public var strength: RecordStrength
    public var website: String?

    public init(id: String, name: String, tags: [String], lastInteraction: String, strength: RecordStrength, website: String? = nil) {
        self.id = id
        self.name = name
        self.tags = tags
        self.lastInteraction = lastInteraction
        self.strength = strength
        self.website = website
    }
}

/// An AI property column that fills itself in row by row when it runs.
public struct RecordsAIColumn: Sendable {
    public var title: String
    public var prompt: String
    public var value: @Sendable (RecordItem, Int) -> String

    public init(title: String, prompt: String, value: @escaping @Sendable (RecordItem, Int) -> String) {
        self.title = title
        self.prompt = prompt
        self.value = value
    }
}

/// Sort state for the grid.
public struct RecordsSort: Equatable, Sendable {
    public enum Key: Sendable { case name, lastInteraction, strength }
    public var key: Key
    public var ascending: Bool

    public init(_ key: Key = .name, ascending: Bool = true) {
        self.key = key
        self.ascending = ascending
    }

    /// Orders records like the original: names and interaction text compare as strings,
    /// strength by rank.
    public func apply(_ records: [RecordItem]) -> [RecordItem] {
        records.sorted { a, b in
            let order: ComparisonResult = switch key {
            case .name: a.name.localizedStandardCompare(b.name)
            case .lastInteraction: a.lastInteraction.localizedStandardCompare(b.lastInteraction)
            case .strength: a.strength.rawValue == b.strength.rawValue ? .orderedSame : a.strength.rawValue < b.strength.rawValue ? .orderedAscending : .orderedDescending
            }
            return ascending ? order == .orderedAscending : order == .orderedDescending
        }
    }
}

/// A CRM-style grid: sticky header, company column and totals row, tag chips that collapse
/// into "+N", relationship strength, links, row selection and sorting. Tapping a header opens
/// its property settings; "+" adds an AI column that resolves row by row.
///
/// The original's draggable column widths are a pointer affordance and are left out.
public struct RecordsTable: View {
    var records: [RecordItem]
    var tagTints: [String: Color]
    var aiColumn: RecordsAIColumn?
    var maxHeight: CGFloat

    @State private var sort = RecordsSort()
    @State private var selected: Set<String> = []
    @State private var aiAdded: Bool
    @State private var aiDone: Bool
    @State private var calc: (column: String, resolved: Int)?
    @State private var configColumn: String?
    @State private var offset = CGPoint.zero
    @State private var position = ScrollPosition()
    @Environment(\.buiTheme) private var theme

    static let rowHeight: CGFloat = 35
    static let widths: [String: CGFloat] = [
        "Company": 270, "Categories": 275, "Last interaction": 190, "Connection strength": 210, "Links": 175, "ai": 240, "actions": 100,
    ]

    /// - Parameter showsAIColumn: start with the AI column already added and filled in.
    public init(records: [RecordItem], tagTints: [String: Color] = [:], aiColumn: RecordsAIColumn? = nil, showsAIColumn: Bool = false, maxHeight: CGFloat = 438) {
        self.records = records
        self.tagTints = tagTints
        self.aiColumn = aiColumn
        self.maxHeight = maxHeight
        _aiAdded = State(initialValue: showsAIColumn)
        _aiDone = State(initialValue: showsAIColumn)
    }

    private var rows: [RecordItem] { sort.apply(records) }

    public var body: some View {
        let rows = rows
        ScrollView([.horizontal, .vertical]) {
            LazyVStack(alignment: .leading, spacing: 0, pinnedViews: [.sectionHeaders, .sectionFooters]) {
                Section {
                    ForEach(Array(rows.enumerated()), id: \.element.id) { index, row in
                        dataRow(row, index: index)
                    }
                } header: {
                    headerRow
                } footer: {
                    footerRow(rows)
                }
            }
        }
        .scrollIndicators(.hidden)
        .scrollPosition($position)
        .onScrollGeometryChange(for: CGPoint.self) { $0.contentOffset } action: { _, new in offset = new }
        .frame(maxHeight: maxHeight)
        .background(theme.colors.surface)
        .clipShape(.rect(cornerRadius: theme.radii.card, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: theme.radii.card, style: .continuous).strokeBorder(theme.colors.line, lineWidth: 1))
        .shadow(color: .black.opacity(0.06), radius: 1, y: 1)
        .task(id: calc?.column) { await runCalculation() }
        .onAppear {
            if aiAdded { position.scrollTo(x: aiColumnOffset) }
        }
    }

    // MARK: Rows

    /// Width of the company column once the grid scrolls sideways. iOS adaptation: on a phone
    /// the full 270 pt sticky column would hide everything else, so it narrows as you scroll.
    static let compactCompanyWidth: CGFloat = 120

    /// Keeps the company column in view while the grid scrolls sideways, narrowing it to
    /// `compactCompanyWidth`.
    private func sticky<V: View>(_ cell: V) -> some View {
        let full = Self.widths["Company"]!
        let visible = max(Self.compactCompanyWidth, full - max(0, offset.x))
        return cell
            .mask(alignment: .leading) { Rectangle().frame(width: visible) }
            .offset(x: max(0, offset.x))
            .shadow(color: .black.opacity(offset.x > 0 ? 0.12 : 0), radius: 4, x: 3)
            .zIndex(1)
    }

    /// Scroll offset that puts the AI column right after the narrowed company column.
    private var aiColumnOffset: CGFloat {
        ["Company", "Categories", "Last interaction", "Connection strength", "Links"].compactMap { Self.widths[$0] }.reduce(0, +) - Self.compactCompanyWidth
    }

    private var headerRow: some View {
        HStack(spacing: 0) {
            sticky(
                HStack(spacing: 8) {
                    RecordCheckbox(state: selected.isEmpty ? .off : selected.count == records.count ? .on : .mixed) {
                        selected = selected.count == records.count ? [] : Set(records.map(\.id))
                    }
                    Text("Company")
                    Spacer(minLength: 0)
                    sortMark(.name)
                }
                .padding(.leading, 6)
                .padding(.trailing, 12)
                .headerCell(width: Self.widths["Company"]!, theme: theme)
                .contentShape(.rect)
                .onTapGesture {
                    withAnimation(BUIMotion.standard) {
                        sort = sort.key == .name ? RecordsSort(.name, ascending: !sort.ascending) : RecordsSort(.name)
                    }
                }
            )
            headerButton("Categories", glyph: Self.typeGlyph("Multi select"), sortKey: nil)
            headerButton("Last interaction", glyph: Self.typeGlyph("Date"), sortKey: .lastInteraction)
            headerButton("Connection strength", glyph: Self.typeGlyph("Single select"), sortKey: .strength)
            headerButton("Links", glyph: Self.typeGlyph("URL"), sortKey: nil)
            if aiAdded, let aiColumn {
                headerButton(aiColumn.title, glyph: Self.typeGlyph("Text"), sortKey: nil, width: Self.widths["ai"]!)
            }
            HStack(spacing: 4) {
                Button {
                    addAIColumn()
                } label: {
                    BUIIcon(.plus, size: 15)
                }
                .buttonStyle(IconButtonStyle())
                .disabled(aiColumn == nil || aiAdded)
                .accessibilityLabel("New property")
            }
            .padding(.horizontal, 8)
            .headerCell(width: Self.widths["actions"]!, theme: theme, trailingRule: false)
        }
        .background(theme.colors.surface)
    }

    private func headerButton(_ title: String, glyph: BUIGlyph, sortKey: RecordsSort.Key?, width: CGFloat? = nil) -> some View {
        Button {
            openConfig(title)
        } label: {
            HStack(spacing: 8) {
                BUIIcon(glyph, size: 15, lineWidth: 1.8).foregroundStyle(theme.colors.ink3)
                Text(title).lineLimit(1)
                Spacer(minLength: 0)
                if let sortKey { sortMark(sortKey) }
            }
            .padding(.horizontal, 12)
            .headerCell(width: width ?? Self.widths[title] ?? 200, theme: theme)
            .foregroundStyle(configColumn == title ? theme.colors.accentInk : theme.colors.ink2)
            .contentShape(.rect)
        }
        .buttonStyle(PressHighlightStyle(radius: 0, color: theme.colors.hover))
        .popover(isPresented: Binding(get: { configColumn == title }, set: { if !$0 { configColumn = nil } })) {
            PropertyConfig(title: title, prompt: title == aiColumn?.title ? aiColumn?.prompt : Self.prompts[title], sortKey: sortKey, sort: $sort) {
                configColumn = nil
                calc = (title, 0)
            }
            .presentationCompactAdaptation(.popover)
        }
    }

    @ViewBuilder
    private func sortMark(_ key: RecordsSort.Key) -> some View {
        if sort.key == key && sort != RecordsSort() {
            BUIIcon(BUIGlyph("M12 5v14M6 13l6 6 6-6"), size: 12, lineWidth: 2)
                .rotationEffect(.degrees(sort.ascending ? 180 : 0))
                .foregroundStyle(theme.colors.ink3)
                .onTapGesture { withAnimation(BUIMotion.standard) { sort.ascending.toggle() } }
        }
    }

    private func dataRow(_ row: RecordItem, index: Int) -> some View {
        let isSelected = selected.contains(row.id)
        let fill = isSelected ? theme.colors.accent.mix(with: theme.colors.surface, by: 0.93) : theme.colors.surface
        return HStack(spacing: 0) {
            sticky(
                HStack(spacing: 4) {
                    ZStack {
                        if isSelected {
                            RecordCheckbox(state: .on) { selected.remove(row.id) }
                        } else {
                            Text("\(index + 1)")
                                .buiFont(BUITextStyle(size: 11.5))
                                .monospacedDigit()
                                .foregroundStyle(theme.colors.ink3)
                                .frame(width: 24, height: 24)
                                .contentShape(.rect)
                                .onTapGesture { selected.insert(row.id) }
                        }
                    }
                    .frame(width: 24, height: 24)
                    Text(row.name.prefix(1).uppercased())
                        .buiFont(BUITextStyle(size: 10, weight: .medium))
                        .foregroundStyle(theme.colors.ink2)
                        .frame(width: 20, height: 20)
                        .background(theme.colors.field, in: .rect(cornerRadius: 6, style: .continuous))
                    Text(row.name)
                        .foregroundStyle(theme.colors.ink)
                        .lineLimit(1)
                    Spacer(minLength: 0)
                }
                .bodyCell(width: Self.widths["Company"]!, fill: fill, theme: theme, leading: 6)
            )
            calcOr("Categories", index) {
                TagRow(tags: row.tags, tints: tagTints)
            }
            .bodyCell(width: Self.widths["Categories"]!, fill: fill, theme: theme)
            calcOr("Last interaction", index) {
                Text(row.lastInteraction)
                    .foregroundStyle(row.lastInteraction == "No contact" ? theme.colors.ink3 : theme.colors.ink)
            }
            .bodyCell(width: Self.widths["Last interaction"]!, fill: fill, theme: theme)
            calcOr("Connection strength", index) {
                HStack(spacing: 8) {
                    Circle().fill(row.strength.tint(theme.colors)).frame(width: 8, height: 8)
                    Text(row.strength.label).foregroundStyle(theme.colors.ink2)
                }
            }
            .bodyCell(width: Self.widths["Connection strength"]!, fill: fill, theme: theme)
            calcOr("Links", index) {
                if let website = row.website {
                    HStack(spacing: 5) {
                        Text(website)
                            .underline(color: theme.colors.accentInk.opacity(0.35))
                            .lineLimit(1)
                        BUIIcon(BUIGlyph("M14 5h5v5M19 5l-8 8"), size: 12, lineWidth: 1.8)
                    }
                    .foregroundStyle(theme.colors.accentInk)
                } else {
                    Text("—").foregroundStyle(theme.colors.ink3)
                }
            }
            .bodyCell(width: Self.widths["Links"]!, fill: fill, theme: theme)
            if aiAdded, let aiColumn {
                Group {
                    if let calc, calc.column == aiColumn.title {
                        if index < calc.resolved { Text(aiColumn.value(row, index)) } else { CalculatingCell() }
                    } else if aiDone {
                        Text(aiColumn.value(row, index))
                    } else {
                        Text("—").foregroundStyle(theme.colors.ink3)
                    }
                }
                .lineLimit(1)
                .bodyCell(width: Self.widths["ai"]!, fill: fill, theme: theme)
            }
            Color.clear.bodyCell(width: Self.widths["actions"]!, fill: fill, theme: theme, trailingRule: false)
        }
        .buiFont(BUITextStyle(size: 13, weight: .medium, lineHeight: 1.19))
        .foregroundStyle(theme.colors.ink)
    }

    @ViewBuilder
    private func calcOr<V: View>(_ column: String, _ index: Int, @ViewBuilder content: () -> V) -> some View {
        if let calc, calc.column == column, index >= calc.resolved {
            CalculatingCell()
        } else {
            content()
        }
    }

    private func footerRow(_ rows: [RecordItem]) -> some View {
        // In steps: as one expression the visionOS build gives up type-checking it.
        let total: Int = rows.map(\.strength.rawValue).reduce(0, +)
        let mean: Double = Double(total) / Double(max(rows.count, 1))
        let average = Int((mean / 3 * 100).rounded())
        return HStack(spacing: 0) {
            sticky(
                HStack(spacing: 4) {
                    Text("\(rows.count)").foregroundStyle(theme.colors.ink)
                    Text("count").foregroundStyle(theme.colors.ink3)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 8)
                .footerCell(width: Self.widths["Company"]!, theme: theme)
            )
            HStack(spacing: 6) {
                BUIIcon(.plus, size: 15, lineWidth: 1.8)
                Text("Add calculation")
                Spacer(minLength: 0)
            }
            .foregroundStyle(theme.colors.ink3)
            .padding(.horizontal, 8)
            .footerCell(width: Self.widths["Categories"]!, theme: theme)
            Text("—").foregroundStyle(theme.colors.ink3).padding(.horizontal, 8).footerCell(width: Self.widths["Last interaction"]!, theme: theme)
            HStack(spacing: 8) {
                Circle().fill(theme.colors.orange).frame(width: 8, height: 8)
                Text("\(average)% average").foregroundStyle(theme.colors.ink2)
            }
            .padding(.horizontal, 8)
            .footerCell(width: Self.widths["Connection strength"]!, theme: theme)
            Text("\(rows.filter { $0.website != nil }.count) links").foregroundStyle(theme.colors.ink3).padding(.horizontal, 8)
                .footerCell(width: Self.widths["Links"]!, theme: theme)
            if aiAdded {
                Text(aiDone ? "\(rows.count) filled" : "—").foregroundStyle(theme.colors.ink3).padding(.horizontal, 8)
                    .footerCell(width: Self.widths["ai"]!, theme: theme)
            }
            Color.clear.footerCell(width: Self.widths["actions"]!, theme: theme, trailingRule: false)
        }
        .buiFont(BUITextStyle(size: 12.5, weight: .medium, lineHeight: 1.19))
        .background(theme.colors.inset)
        .overlay(alignment: .top) { Rectangle().fill(theme.colors.line).frame(height: 1) }
    }

    // MARK: Actions

    private func openConfig(_ column: String) {
        configColumn = column
    }

    private func addAIColumn() {
        guard let aiColumn, !aiAdded else { return }
        withAnimation(BUIMotion.standard) { aiAdded = true }
        // Like the original, bring the new column into view.
        withAnimation(BUICurve.outStrong.animation(duration: 0.4)) { position.scrollTo(x: aiColumnOffset) }
        calc = (aiColumn.title, 0)
    }

    /// Resolves one row every 110 ms, like the original.
    private func runCalculation() async {
        guard let column = calc?.column else { return }
        while let current = calc, current.column == column, current.resolved <= records.count {
            try? await Task.sleep(for: .milliseconds(110))
            guard !Task.isCancelled else { return }
            calc?.resolved += 1
        }
        if column == aiColumn?.title { aiDone = true }
        calc = nil
    }

    // MARK: Data

    static func typeGlyph(_ type: String) -> BUIGlyph {
        switch type {
        case "Multi select": BUIGlyph("M11 6h9M11 12h9M11 18h9M4 6l1.5 1.5L8 5M4 12l1.5 1.5L8 11M4 18l1.5 1.5L8 17")
        case "Date": BUIGlyph(BUIGlyph.rect(3, 5, 18, 16, rx: 2.5) + "M8 3v4M16 3v4M3 10h18")
        case "Single select": BUIGlyph(BUIGlyph.circle(12, 12, 9) + "m8.5 12 2.4 2.4 4.6-4.9")
        case "URL": BUIGlyph("M10 13a5 5 0 0 0 7.1.1l2-2a5 5 0 0 0-7.1-7.1l-1.1 1.1M14 11a5 5 0 0 0-7.1-.1l-2 2A5 5 0 0 0 12 20l1.1-1.1")
        default: BUIGlyph("M4 6h16M4 12h10M4 18h7")
        }
    }

    static let prompts: [String: String] = [
        "Categories": "Tag each Company with its market categories.",
        "Connection strength": "Score the relationship from Last interaction.",
        "Links": "Find the website for Company.",
    ]
}

private extension View {
    func headerCell(width: CGFloat, theme: BUITheme, trailingRule: Bool = true) -> some View {
        buiFont(BUITextStyle(size: 12.5, weight: .medium))
            .foregroundStyle(theme.colors.ink2)
            .frame(width: width, height: RecordsTable.rowHeight)
            .background(theme.colors.surface)
            .overlay(alignment: .trailing) { if trailingRule { Rectangle().fill(theme.colors.line.opacity(0.78)).frame(width: 1) } }
            .overlay(alignment: .bottom) { Rectangle().fill(theme.colors.line.opacity(0.78)).frame(height: 1) }
    }

    func bodyCell(width: CGFloat, fill: Color, theme: BUITheme, trailingRule: Bool = true, leading: CGFloat = 8) -> some View {
        padding(.leading, leading)
            .padding(.trailing, 8)
            .frame(width: width, height: RecordsTable.rowHeight, alignment: .leading)
            .background(fill)
            .overlay(alignment: .trailing) { if trailingRule { Rectangle().fill(theme.colors.line.opacity(0.78)).frame(width: 1) } }
            .overlay(alignment: .bottom) { Rectangle().fill(theme.colors.line.opacity(0.78)).frame(height: 1) }
    }

    func footerCell(width: CGFloat, theme: BUITheme, trailingRule: Bool = true) -> some View {
        frame(width: width, height: RecordsTable.rowHeight, alignment: .leading)
            .background(theme.colors.inset)
            .overlay(alignment: .trailing) { if trailingRule { Rectangle().fill(theme.colors.line.opacity(0.78)).frame(width: 1) } }
    }
}

/// 18 pt checkbox with an indeterminate dash.
private struct RecordCheckbox: View {
    enum State { case off, on, mixed }
    var state: State
    var action: () -> Void
    @Environment(\.buiTheme) private var theme

    var body: some View {
        Button(action: action) {
            RoundedRectangle(cornerRadius: 6, style: .continuous)
                .fill(state == .off ? Color(light: 0xFFFFFF, dark: 0x343A41) : theme.colors.accent)
                .overlay {
                    switch state {
                    case .off:
                        RoundedRectangle(cornerRadius: 6, style: .continuous).strokeBorder(Color(light: 0xC7CDD3, dark: 0x4F565F), lineWidth: 1)
                    case .on:
                        BUIIcon(BUIGlyph("m5 12 4 4L19 6"), size: 12, lineWidth: 1.8).foregroundStyle(.white)
                    case .mixed:
                        Capsule().fill(.white).frame(width: 8, height: 1.5)
                    }
                }
                .frame(width: 18, height: 18)
                .frame(width: 24, height: 24)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(state == .on ? .isSelected : [])
    }
}

/// Tag chips that fit the cell, then "+N" for the rest.
private struct TagRow: View {
    var tags: [String]
    var tints: [String: Color]
    @Environment(\.buiTheme) private var theme

    var body: some View {
        ViewThatFits(in: .horizontal) {
            ForEach((1...max(tags.count, 1)).reversed(), id: \.self) { visible in
                HStack(spacing: 4) {
                    ForEach(tags.prefix(visible), id: \.self) { tag in chip(tag) }
                    if visible < tags.count {
                        Text("+\(tags.count - visible)")
                            .buiFont(BUITextStyle(size: 11, weight: .medium))
                            .foregroundStyle(theme.colors.ink3)
                            .padding(.horizontal, 7)
                            .frame(height: 23)
                            .background(theme.colors.inset, in: .rect(cornerRadius: 6, style: .continuous))
                            .overlay(RoundedRectangle(cornerRadius: 6, style: .continuous).strokeBorder(theme.colors.lineStrong, lineWidth: 1))
                    }
                }
                .fixedSize()
            }
        }
    }

    /// `.records-tag`: base hue mixed into the surface for fill and ring, into ink for text.
    private func chip(_ tag: String) -> some View {
        let base = tints[tag] ?? theme.colors.ink3
        return Text(tag)
            .buiFont(BUITextStyle(size: 13, weight: .medium))
            .foregroundStyle(base.mix(with: theme.colors.ink, by: 0.08))
            .lineLimit(1)
            .padding(.horizontal, 7)
            .frame(maxWidth: 115)
            .frame(height: 23)
            .background(base.mix(with: theme.colors.surface, by: 0.82), in: .rect(cornerRadius: 8, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(base.mix(with: theme.colors.surface, by: 0.68), lineWidth: 1))
            .fixedSize()
    }
}

/// "Calculating…" with the pulsing accent dot (`records-pulse`, 1.1 s).
private struct CalculatingCell: View {
    @Environment(\.buiTheme) private var theme
    @State private var on = false

    var body: some View {
        HStack {
            Text("Calculating…").foregroundStyle(theme.colors.ink3)
            Spacer(minLength: 8)
            Circle()
                .fill(theme.colors.accent)
                .frame(width: 8, height: 8)
                .scaleEffect(on ? 1 : 0.8)
                .opacity(on ? 1 : 0.35)
                .onAppear {
                    withAnimation(.easeInOut(duration: 0.55).repeatForever(autoreverses: true)) { on = true }
                }
        }
    }
}

/// The property popover: what the column is, where its values come from, and how it sorts.
private struct PropertyConfig: View {
    var title: String
    var prompt: String?
    var sortKey: RecordsSort.Key?
    @Binding var sort: RecordsSort
    var onRun: () -> Void
    @Environment(\.buiTheme) private var theme

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title)
                .buiFont(.title)
                .foregroundStyle(theme.colors.ink)
            if let prompt {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Prompt")
                        .buiFont(BUITextStyle(size: 11, weight: .medium))
                        .foregroundStyle(theme.colors.ink3)
                    Text(prompt)
                        .buiFont(BUITextStyle(size: 12.5, lineHeight: 1.5))
                        .foregroundStyle(theme.colors.ink2)
                        .padding(8)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(theme.colors.field, in: .rect(cornerRadius: theme.radii.control, style: .continuous))
                }
            } else {
                Text("Entered by people, not calculated.")
                    .buiFont(.caption)
                    .foregroundStyle(theme.colors.ink3)
            }
            HStack(spacing: 8) {
                if let sortKey {
                    Button("Sort ↑") { sort = RecordsSort(sortKey, ascending: true) }
                        .buttonStyle(.bui(.secondary, fontSize: 12.5, horizontalPadding: 10, isActive: sort == RecordsSort(sortKey, ascending: true)))
                    Button("Sort ↓") { sort = RecordsSort(sortKey, ascending: false) }
                        .buttonStyle(.bui(.secondary, fontSize: 12.5, horizontalPadding: 10, isActive: sort == RecordsSort(sortKey, ascending: false)))
                }
                Spacer(minLength: 0)
                if prompt != nil {
                    Button("Run", action: onRun)
                        .buttonStyle(.bui(.accent, fontSize: 12.5))
                }
            }
        }
        .padding(12)
        .frame(width: 300)
        .background(theme.colors.surface)
    }
}
