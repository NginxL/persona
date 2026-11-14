import SwiftUI
#if os(iOS) || os(visionOS)
import UIKit
#endif

extension View {
    /// Leaves this List row's cell unclipped. An inset-grouped list rounds its cells (a 26 pt corner configuration) and
    /// clips them, so a row that draws up to the cell's edge loses whatever falls outside those corners: the Knowledge
    /// page's first kind pills lost a corner (#7735, docs/lessons/ios.md). Only for a row with a clear background
    /// (`listRowBackground(Color.clear)`): a row with one would show its cell's square corners.
    func unclippedListRow() -> some View {
        #if os(iOS) || os(visionOS)
        background(UnclippedListCell())
        #else
        self
        #endif
    }
}

#if os(iOS) || os(visionOS)
/// Finds the collection-view cell it sits in and turns the cell's clipping off, keeping it off when the list sets it
/// again (it does when it configures the cell), and turns it back on once it leaves the cell (the cell is reused).
/// The Agents header's search field does the same for its own row (`AgentsHeader.Holder`).
private struct UnclippedListCell: UIViewRepresentable {
    func makeUIView(context: Context) -> Probe { Probe(frame: .zero) }

    func updateUIView(_ view: Probe, context: Context) {}

    final class Probe: UIView {
        private weak var cell: UICollectionViewCell?
        private var clipping: NSKeyValueObservation?

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
            isAccessibilityElement = false
        }

        required init?(coder: NSCoder) { nil }

        override func layoutSubviews() {
            super.layoutSubviews()
            unclipCell()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if window == nil {
                clipping = nil
                cell?.clipsToBounds = true
                cell = nil
            } else {
                unclipCell()
            }
        }

        private func unclipCell() {
            if cell == nil {
                var v = superview
                while let x = v, !(x is UICollectionViewCell) { v = x.superview }
                cell = v as? UICollectionViewCell
            }
            guard let cell else { return }
            if cell.clipsToBounds { cell.clipsToBounds = false }
            if clipping == nil {
                clipping = cell.layer.observe(\.masksToBounds, options: [.new]) { layer, _ in
                    if layer.masksToBounds { layer.masksToBounds = false } // on the main thread, where the list set it
                }
            }
        }
    }
}
#endif
