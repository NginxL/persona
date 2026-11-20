import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// UI version 8 on iOS (#9158, docs/extensions/extensions.md "UI version 8"): the schema's examples read with every new
/// field; how many columns a width holds (the page's grid and a section's rows), by the width and the text size, never
/// the device; the lines of cards a columns view makes, a section that spans taking a line of its own; a section's runs of
/// rows; the list's heads and rows side by side; the stream's section link; and the versions the app now reports.
@MainActor
struct ExtensionColumnsTests {
    private static let examples = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples")

    private static func view(_ name: String) throws -> ExtensionView {
        try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: examples.appending(path: name)))
    }

    private static func row(_ key: String) -> ExtensionComponent {
        .row(ExtensionRowComponent(key: key, title: key))
    }

    private static func text(_ key: String) -> ExtensionComponent {
        .text(ExtensionTextComponent(key: key, text: key))
    }

    // MARK: Reading

    @Test func theExamplesReadWithEveryNewField() throws {
        let today = try Self.view("View.v8-today.json")
        #expect(today.layout == ExtensionLayout.columns)
        let needs = try #require(today.sections.first)
        #expect(needs.span == ExtensionSectionSpan.full && needs.columns == 2)
        #expect(needs.icon == "bell" && needs.iconColor == "orange")
        #expect(needs.link?.title == "All" && needs.link?.action.id.isEmpty == false)
        #expect(today.sections.dropFirst().allSatisfy { $0.span == nil && $0.columns == nil && $0.link != nil })

        let header = try Self.view("View.v8-section-header.json")
        #expect(header.layout == nil)
        let s = try #require(header.sections.first)
        #expect(s.icon == "chart" && s.iconColor == nil)
        #expect(s.link?.title == "History" && s.link?.action.id == "history" && s.link?.action.values == ["range": "month"])
    }

    // MARK: How many columns

    @Test func columnsFollowTheWidthAsTheCardGridsCountThem() {
        let min = Theme.Size.cardColumn, gap = Theme.Space.m
        // floor((width + gap) / (min + gap)), at least one, at most three.
        #expect(ExtensionColumns.count(width: 0, minimum: min, gap: gap, most: 3) == 1)
        #expect(ExtensionColumns.count(width: .infinity, minimum: min, gap: gap, most: 3) == 1, "no width offered: one")
        #expect(ExtensionColumns.count(width: 2 * min + gap - 1, minimum: min, gap: gap, most: 3) == 1)
        #expect(ExtensionColumns.count(width: 2 * min + gap, minimum: min, gap: gap, most: 3) == 2)
        #expect(ExtensionColumns.count(width: 3 * min + 2 * gap - 1, minimum: min, gap: gap, most: 3) == 2)
        #expect(ExtensionColumns.count(width: 3 * min + 2 * gap, minimum: min, gap: gap, most: 3) == 3)
        #expect(ExtensionColumns.count(width: 4000, minimum: min, gap: gap, most: 3) == 3, "never more than three")
        #expect(ExtensionColumns.count(width: 4000, minimum: min, gap: gap, most: 2) == 2, "a section's own columns")
        #expect(ExtensionColumns.count(width: 4000, minimum: min, gap: gap, most: 0) == 1)
    }

    @Test func aPageIsAGridByItsWidthNotItsDevice() throws {
        let today = try Self.view("View.v8-today.json")
        let large = DynamicTypeSize.large
        // An iPhone upright (402 pt), and an iPad's narrow window in Split View (507 pt): the stack.
        #expect(ExtensionColumns.pageColumns(today, pageWidth: 402, typeSize: large) == 1)
        #expect(ExtensionColumns.pageColumns(today, pageWidth: 507, typeSize: large) == 1)
        // Two once the content inside the page's margins holds two columns: an iPhone on its side, an iPad's detail column.
        let two = 2 * Theme.Size.cardColumn + Theme.Space.m + 2 * Theme.Space.page
        #expect(ExtensionColumns.pageColumns(today, pageWidth: two - 1, typeSize: large) == 1)
        #expect(ExtensionColumns.pageColumns(today, pageWidth: two, typeSize: large) == 2)
        #expect(ExtensionColumns.pageColumns(today, pageWidth: 874, typeSize: large) == 2)
        #expect(ExtensionColumns.pageColumns(today, pageWidth: 976, typeSize: large) == 2)
        // A 13-inch iPad's whole width on its side: three.
        #expect(ExtensionColumns.pageColumns(today, pageWidth: 1376, typeSize: large) == 3)
        // Any other layout is the page's list, however wide.
        var stack = today; stack.layout = nil
        #expect(ExtensionColumns.pageColumns(stack, pageWidth: 1376, typeSize: large) == 1)
        stack.layout = ExtensionLayout.split
        #expect(ExtensionColumns.pageColumns(stack, pageWidth: 1376, typeSize: large) == 1)
    }

    @Test func largerTextNeedsWiderColumns() throws {
        let today = try Self.view("View.v8-today.json")
        #expect(ExtensionColumns.minimum(.large) == Theme.Size.cardColumn)
        #expect(ExtensionColumns.minimum(.xxxLarge) > ExtensionColumns.minimum(.large))
        #expect(ExtensionColumns.minimum(.small) < ExtensionColumns.minimum(.large))
        // The interface stops growing at xxxLarge (`Theme.interfaceLargestTypeSize`): there a 13-inch iPad's detail
        // column on its side (976 pt) still holds two columns, a window that just holds two at the default size one.
        let xxxLarge = Theme.interfaceLargestTypeSize
        #expect(ExtensionColumns.pageColumns(today, pageWidth: 976, typeSize: xxxLarge) == 2)
        let justTwo = 2 * Theme.Size.cardColumn + Theme.Space.m + 2 * Theme.Space.page
        #expect(ExtensionColumns.pageColumns(today, pageWidth: justTwo, typeSize: .large) == 2)
        #expect(ExtensionColumns.pageColumns(today, pageWidth: justTwo, typeSize: xxxLarge) == 1)
    }

    // MARK: Lines of cards

    @Test func cardsGoInReadingOrderAndASpanningOneTakesALine() throws {
        let today = try Self.view("View.v8-today.json")
        #expect(ExtensionColumns.lines(today.sections, columns: 2) == [
            .init(sections: [0], full: true), .init(sections: [1, 2], full: false),
        ])
        let five = [ExtensionSection(key: "needs", span: "full")] + (1...4).map { ExtensionSection(key: "c\($0)") }
        #expect(ExtensionColumns.lines(five, columns: 2).map(\.sections) == [[0], [1, 2], [3, 4]])
        #expect(ExtensionColumns.lines(five, columns: 3).map(\.sections) == [[0], [1, 2, 3], [4]], "a short last line")
        #expect(ExtensionColumns.lines(five, columns: 1).map(\.sections) == [[0], [1], [2], [3], [4]])
        // One that spans in the middle ends the line before it, short, rather than moving a card past it.
        let middle = [ExtensionSection(key: "a"), ExtensionSection(key: "wide", span: "full"), ExtensionSection(key: "b"),
                      ExtensionSection(key: "c"), ExtensionSection(key: "d")]
        #expect(ExtensionColumns.lines(middle, columns: 2) == [
            .init(sections: [0], full: false), .init(sections: [1], full: true),
            .init(sections: [2, 3], full: false), .init(sections: [4], full: false),
        ])
        // A span this app does not know is one column.
        #expect(ExtensionColumns.lines([ExtensionSection(span: "half"), ExtensionSection()], columns: 2).map(\.full) == [false])
    }

    // MARK: Rows side by side

    @Test func aSectionsRunsOfRowsGoSideBySide() {
        let items = [Self.text("summary"), Self.row("a"), Self.row("b"), Self.row("c"), .divider(ExtensionDividerComponent()),
                     Self.row("d"), Self.text("note"), Self.row("e")]
        #expect(ExtensionColumns.blocks(items, columns: 2) == [.one(0), .rows([1, 2, 3]), .one(4), .rows([5]), .one(6), .rows([7])])
        // Without `columns` every component is drawn as before, one block each.
        #expect(ExtensionColumns.blocks(items, columns: 1) == items.indices.map { .one($0) })
        #expect(ExtensionColumns.rowColumns(ExtensionSection()) == 1)
        #expect(ExtensionColumns.rowColumns(ExtensionSection(columns: 2)) == 2)
        #expect(ExtensionColumns.rowColumns(ExtensionSection(columns: 9)) == 3 && ExtensionColumns.rowColumns(ExtensionSection(columns: 0)) == 1)
    }

    /// The layout's own size for a width: lines of as many as fit, each as tall as its tallest, one stacked when narrow.
    @Test func theLayoutLinesUpByTheWidthOffered() {
        func height(_ width: CGFloat, most: Int = 2, minimum: CGFloat? = 100) -> CGFloat {
            let layout = ExtensionColumnsLayout(most: most, minimum: minimum, spacing: 10) {
                Color.clear.frame(height: 10)
                Color.clear.frame(height: 30)
                Color.clear.frame(height: 20)
            }
            return UIHostingController(rootView: layout).sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude)).height
        }
        // Two to a line from 210 (100 + 10 + 100): [10, 30] then [20].
        let two = height(210), one = height(209), three = height(320, most: 3), capped = height(320, most: 2)
        let fixed = height(50, most: 2, minimum: nil)
        // To a hundredth of a point: the hosting view adds its float error to the sum.
        func near(_ a: CGFloat, _ b: CGFloat) -> Bool { abs(a - b) < 0.01 }
        // Named and typed outside #expect: Xcode 26.0 times out inferring the literal sums inside the macro (#9497).
        let twoLines: CGFloat = 30 + 10 + 20
        let threeLines: CGFloat = 10 + 30 + 20 + 2 * 10
        let oneLine: CGFloat = 30
        #expect(near(two, twoLines), "two to a line: \(two)")
        #expect(near(one, threeLines), "narrower, one to a line: \(one)")
        #expect(near(three, oneLine), "three to a line: \(three)")
        #expect(near(capped, twoLines), "at most its columns: \(capped)")
        #expect(near(fixed, twoLines), "fixed columns, whatever the width: \(fixed)")
    }

    // MARK: The list

    @Test func theListKeepsPlainSectionsAndHeadsOnlyTheNewOnes() throws {
        let header = try Self.view("View.v8-section-header.json")
        let parts = ExtensionParts.of(header)
        #expect(parts.count == 1 && parts[0].head?.key == "today" && parts[0].title == "This week" && parts[0].columns == 1)

        let today = try Self.view("View.v8-today.json")
        let drawn = ExtensionParts.of(today)
        #expect(drawn.map { $0.head != nil } == [true, true, true])
        #expect(drawn.map(\.columns) == [2, 1, 1])

        // A section with neither an icon nor a link keeps its plain title, and one split by a divider heads only its first part.
        let plain = ExtensionView(sections: [
            ExtensionSection(title: "Plain", items: [Self.row("a")]),
            ExtensionSection(title: "Linked", items: [Self.row("b"), .divider(ExtensionDividerComponent()), Self.row("c")],
                             link: ExtensionSectionLink(title: "All", action: ExtensionAction(id: "all"))),
        ])
        let split = ExtensionParts.of(plain)
        #expect(split.map(\.title) == ["Plain", "Linked", nil])
        #expect(split.map { $0.head?.title } == [nil, "Linked", nil])
        #expect(!ExtensionSectionHead.wanted(ExtensionSection(title: "x", icon: "")))
        #expect(ExtensionSectionHead.wanted(ExtensionSection(icon: "bell")))
    }

    @Test func onlyAViewWithTheNewLayoutAsksForThePagesWidth() throws {
        #expect(try Self.view("View.v8-today.json").wantsPageWidth)
        #expect(!(try Self.view("View.v8-section-header.json").wantsPageWidth))
        #expect(ExtensionView(sections: [ExtensionSection(columns: 2)]).wantsPageWidth)
        #expect(!ExtensionView(sections: [ExtensionSection(columns: 1)]).wantsPageWidth)
    }

    // MARK: The stream

    /// A section's link in a native stream (UI version 8 keeps it): pressed, it goes back as its section's key, a press.
    @Test func aStreamSectionsLinkIsItsSectionsPress() throws {
        let snapshot = try ExtensionStreamJSON.parse(Data(#"""
        {"protocol":1,"session":"session-a","epoch":"epoch-a","version":1,"root_key":"page","view":{"title":"Today",
         "layout":"columns","sections":[{"key":"calendar","title":"Calendar","icon":"calendar","icon_color":"red",
         "span":"full","columns":2,"link":{"title":"Calendar","action":{"id":"open-calendar"}},
         "items":[{"type":"row","key":"sync","title":"Weekly sync","action":{"id":"open-sync"}}]}]}}
        """#.utf8))
        let tree = try ExtensionStreamTree(snapshot: snapshot, components: ExtensionPageStream.components)
        let view = try tree.nativeView
        #expect(view.layout == ExtensionLayout.columns && view.sections[0].link?.action.id == "open-calendar")
        let slots = tree.slots(action: "open-calendar")
        #expect(slots.count == 1 && slots.first?.key == "calendar" && slots.first?.event == "press")
        #expect(tree.slots(action: "open-sync").first?.key == "sync")
    }

    // MARK: Versions

    @Test func pagesAndCardsSay10WhileTheStreamStaysAt8() {
        #expect(ExtensionUIContext.pageVersion == 10 && ExtensionUIContext.cardVersion == 10)
        #expect(ExtensionUIContext.here().forPage().uiVersion == 10)
        #expect(ExtensionUIContext.here(size: "compact").drawnByApp().uiVersion == 10)
        #expect(ExtensionPageStream.uiVersion == 8)
        // Widgets, the watch and the system extensions draw none of it.
        #expect(ExtensionUIContext.here().uiVersion == 3)
    }
}
