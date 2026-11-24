import Foundation
import CoreGraphics

extension Theme {
    /// Every sidebar row's metrics (the Persona sidebar on iPhone and iPad, its list on the Mac, the Mac's sections
    /// column): a minimum height and padding, not the text's own height, so a row is as tall in every language.
    /// iOS: the system sidebar's 44 pt row (`List(.sidebar)`, the smallest touch target). macOS: the system's
    /// Sidebar icon size (General settings: small / medium / large rows of 24 / 28 / 32 pt, `NSTableViewDefaultSizeMode`).
    /// Sizes go through `scaledFrame`, so they still follow the text size.
    nonisolated enum Sidebar {
        /// A one-line row's height.
        static var row: CGFloat { metrics.row }
        /// The least space above and below a row's text (a two-line row, a larger text size, a taller script).
        static var rowPadding: CGFloat { metrics.padding }
        /// The box a row's symbol is drawn in, and the column it sits in (wide symbols scale down into the box).
        static var glyph: CGFloat { metrics.glyph }
        static var glyphColumn: CGFloat { metrics.column }
        /// Between a row's symbol and its words.
        static var gap: CGFloat { metrics.gap }
        /// A conversation's round mark.
        static var mark: CGFloat { metrics.mark }
        /// The selected or hovered row's rounded shade.
        static var radius: CGFloat { metrics.radius }

        private struct Metrics {
            var row: CGFloat, padding: CGFloat, glyph: CGFloat, column: CGFloat, gap: CGFloat, mark: CGFloat, radius: CGFloat
        }

        private static let metrics = Metrics(row: 44, padding: 4, glyph: 22, column: 24, gap: 16, mark: 32, radius: 10)
    }
}
