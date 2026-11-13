import SwiftUI

/// A group of buttons side by side in equal widths while every label fits its share whole, and one above the other,
/// the first on top and each across the row, when one doesn't (#6151; `docs/design/DESIGN_SYSTEM.md` 1.2 rule 3 (c)).
/// It goes by the measured widths, not by the text size: a short label stays beside its neighbor at sizes where a
/// longer one, or a longer translation, has to stack. A button tells how narrow it can be with its words whole through
/// its label's `buttonLabelLine()`; one whose label doesn't is taken to fit. A single button takes the row's width.
struct StackingButtonRow: Layout {
    var spacing: CGFloat = Theme.Space.m
    var stackedSpacing: CGFloat = Theme.Space.s

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard !subviews.isEmpty else { return .zero }
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? idealWidth(subviews)
        if sideBySide(width, subviews) {
            let offer = ProposedViewSize(width: share(width, subviews.count), height: proposal.height)
            return CGSize(width: width, height: subviews.map { $0.sizeThatFits(offer).height }.max() ?? 0)
        }
        let offer = ProposedViewSize(width: width, height: nil)
        let heights = subviews.map { $0.sizeThatFits(offer).height }
        return CGSize(width: width, height: heights.reduce(0, +) + stackedSpacing * CGFloat(subviews.count - 1))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        if sideBySide(bounds.width, subviews) {
            // As an HStack places them: equal shares, centered on the row's middle.
            let each = share(bounds.width, subviews.count)
            var x = bounds.minX
            for s in subviews {
                s.place(at: CGPoint(x: x, y: bounds.midY), anchor: .leading,
                        proposal: ProposedViewSize(width: each, height: proposal.height))
                x += each + spacing
            }
            return
        }
        var y = bounds.minY
        for s in subviews {
            let height = s.sizeThatFits(ProposedViewSize(width: bounds.width, height: nil)).height
            s.place(at: CGPoint(x: bounds.minX, y: y), proposal: ProposedViewSize(width: bounds.width, height: height))
            y += height + stackedSpacing
        }
    }

    private func share(_ width: CGFloat, _ count: Int) -> CGFloat {
        (width - spacing * CGFloat(count - 1)) / CGFloat(count)
    }

    /// A button's size for a width of zero is the narrowest it shows its words whole (`buttonLabelLine()`).
    private func sideBySide(_ width: CGFloat, _ subviews: Subviews) -> Bool {
        let each = share(width, subviews.count)
        return subviews.allSatisfy { $0.sizeThatFits(ProposedViewSize(width: 0, height: nil)).width <= each }
    }

    private func idealWidth(_ subviews: Subviews) -> CGFloat {
        let widest = subviews.map { $0.sizeThatFits(.unspecified).width }.max() ?? 0
        return widest * CGFloat(subviews.count) + spacing * CGFloat(subviews.count - 1)
    }
}

extension Text {
    /// A button's words on one line (`docs/design/DESIGN_SYSTEM.md` 1.2 rule 3): they may shrink to `minimumScale` for
    /// a small overflow and are never cut. Asked for its narrowest (a width of zero), the line answers with its whole
    /// width at that scale, so a `StackingButtonRow` stacks its buttons before a label would be cut. 0.8 is the
    /// shrink the app's buttons have always allowed.
    func buttonLabelLine(minimumScale: CGFloat = 0.8) -> some View {
        WholeLine(minimumScale: minimumScale) {
            lineLimit(1).minimumScaleFactor(minimumScale)
        }
    }
}

private struct WholeLine: Layout {
    let minimumScale: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let line = subviews.first else { return .zero }
        guard proposal.width == 0 else { return line.sizeThatFits(proposal) }
        let whole = line.sizeThatFits(.unspecified)
        return CGSize(width: (whole.width * minimumScale).rounded(.up), height: whole.height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(bounds.size))
    }
}
