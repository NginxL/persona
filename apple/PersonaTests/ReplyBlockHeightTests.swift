#if os(iOS)
import MarkdownUI
import Observation
import PersonaKit
import SwiftUI
import Testing
@testable import Persona

/// A reply's table and code blocks are as tall as their rows and lines (#10012): a table was cut under a row, the
/// paragraph after it drawn whole right under the cut. Offered less height than they need, they keep it all, as the
/// theme's paragraphs keep their lines; their columns fitted again after they were first drawn (the width they get
/// changes, a streamed reply's table grows), they grow and shrink with them. Each is compared with the same reply drawn
/// once, as it ends up, from the start. The views are hosted in a window, as `AnswerPartsLookTests` does: the table
/// fits its columns after a first layout, and MarkdownUI spaces its blocks after one.
@MainActor
struct ReplyBlockHeightTests {
    /// Three columns, empty cells, a first column that fits on one line where there is room and wraps where there is
    /// not, and a paragraph after the table: the shape of the table the bug was reported on, in made-up words.
    static let table = """
    先看总时长，按几种做法分开算：

    | 做法 | 拿到第一版 | 全部完成 |
    | --- | --- | --- |
    | 自己做，熟手 | 约 20 天 | |
    | 自己做，一般 | 约 26 天 | 约 35 天 |
    | 自己做，新手 | 约 33 天 | |
    | 请人帮忙，约 $22 | | 约 23 天 |
    | 全部外包，约 $88 | 约 6 天 | 约 15 天 |
    | 混合做，核心部分自己做 | 约 12 天 | |

    表格后的这一段从最后一行下面开始。
    """

    static let code = """
    改完重启一次：

    ```yaml
    service:
      name: example-builder
      env:
        EXAMPLE_TOKEN_FILE: /etc/example/token
      restart: on-failure
    ```

    代码块后的这一段从最后一行下面开始。
    """

    /// The phone's width for a reply, and one with room for every column on one line.
    static let narrow: CGFloat = 330
    static let wide: CGFloat = 640

    @Observable final class Shown {
        var width: CGFloat
        var text: String
        /// The height offered to the reply; nil: what it needs.
        var offer: CGFloat?

        init(width: CGFloat, text: String, offer: CGFloat? = nil) {
            self.width = width
            self.text = text
            self.offer = offer
        }
    }

    /// Offers the reply `height` (nil: the height it is offered) and is as tall as the reply then is, more included.
    private struct Offered: Layout {
        var height: CGFloat?

        func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
            subviews.first?.sizeThatFits(ProposedViewSize(width: proposal.width, height: height ?? proposal.height)) ?? .zero
        }

        func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
            subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(width: bounds.width, height: height ?? bounds.height))
        }
    }

    private struct Host: View {
        let shown: Shown
        let box: ReplyHeightBox

        var body: some View {
            Offered(height: shown.offer) {
                Markdown(MarkdownMemo.content(shown.text))
                    .markdownTheme(.persona)
                    .markdownSoftBreakMode(.lineBreak)
            }
            .frame(width: shown.width, alignment: .topLeading)
            .fixedSize(horizontal: false, vertical: true)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { box.height = $0 }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        }
    }

    /// The reply's height in a window once it has settled; `change` runs once it has, and it settles again.
    private func height(_ shown: Shown, change: (() -> Void)? = nil) async throws -> CGFloat {
        let box = ReplyHeightBox()
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        let window = scene.map { UIWindow(windowScene: $0) } ?? UIWindow()
        window.frame = CGRect(x: 0, y: 0, width: Self.wide + 60, height: 3000)
        window.windowLevel = .alert + 1
        window.rootViewController = UIHostingController(rootView: Host(shown: shown, box: box))
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        try await settle(window, box)
        if let change {
            change()
            try await settle(window, box)
        }
        return box.height
    }

    /// Until the height has held for a quarter of a second (the table fits its columns a layout after the first).
    private func settle(_ window: UIWindow, _ box: ReplyHeightBox) async throws {
        var last: CGFloat = -1, steady = 0
        for _ in 0..<60 where steady < 5 {
            try await Task.sleep(for: .milliseconds(50))
            window.layoutIfNeeded()
            steady = box.height == last && box.height > 0 ? steady + 1 : 0
            last = box.height
        }
    }

    /// Offered 60 pt less than it needs, a table keeps all its rows (it gave the shortfall up and was cut).
    @Test func tableOfferedLessKeepsItsRows() async throws {
        let free = try await height(Shown(width: Self.narrow, text: Self.table))
        let offered = try await height(Shown(width: Self.narrow, text: Self.table, offer: free - 60))
        #expect(abs(offered - free) <= 1, "offered \(free - 60) pt: \(offered) pt, as tall as it needs \(free) pt")
    }

    /// The same for a code block's lines.
    @Test func codeBlockOfferedLessKeepsItsLines() async throws {
        let free = try await height(Shown(width: Self.narrow, text: Self.code))
        let offered = try await height(Shown(width: Self.narrow, text: Self.code, offer: free - 60))
        #expect(abs(offered - free) <= 1, "offered \(free - 60) pt: \(offered) pt, as tall as it needs \(free) pt")
    }

    /// Room for every column on one line, then a phone's width: the first column wraps and the table grows.
    @Test func narrowedTableGrowsWithItsRows() async throws {
        let fresh = try await height(Shown(width: Self.narrow, text: Self.table))
        let wide = try await height(Shown(width: Self.wide, text: Self.table))
        #expect(fresh > wide, "the narrow table has no wrapped rows: \(fresh) pt, wide \(wide) pt")
        let shown = Shown(width: Self.wide, text: Self.table)
        let moved = try await height(shown) { shown.width = Self.narrow }
        #expect(abs(moved - fresh) <= 1, "wide, then narrow: \(moved) pt, drawn narrow from the start \(fresh) pt")
    }

    /// A phone's width, then room for every column: the wrapped cells unwrap and the table is no taller than its rows.
    @Test func widenedTableShrinksToItsRows() async throws {
        let fresh = try await height(Shown(width: Self.wide, text: Self.table))
        let shown = Shown(width: Self.narrow, text: Self.table)
        let moved = try await height(shown) { shown.width = Self.wide }
        #expect(abs(moved - fresh) <= 1, "narrow, then wide: \(moved) pt, drawn wide from the start \(fresh) pt")
    }

    /// A streamed reply: the table with its short rows first, then the rows whose first cells wrap.
    @Test func grownTableGrowsWithItsRows() async throws {
        let lines = Self.table.components(separatedBy: "\n")
        let partial = (lines.prefix(7) + ["", lines.last ?? ""]).joined(separator: "\n")
        let fresh = try await height(Shown(width: Self.narrow, text: Self.table))
        let shown = Shown(width: Self.narrow, text: partial)
        let moved = try await height(shown) { shown.text = Self.table }
        #expect(abs(moved - fresh) <= 1, "rows added: \(moved) pt, drawn whole from the start \(fresh) pt")
    }
}

/// The drawn height, written by the view's geometry callback without redrawing anything.
private final class ReplyHeightBox {
    var height: CGFloat = 0
}
#endif
