import SwiftUI

nonisolated extension Theme {
    /// Intake colors are shared by the app and widgets. Their hues stay apart from Apple's Activity rings.
    enum Health {
        static let waterHex = HealthIntakePalette.waterHex
        static let proteinHex = HealthIntakePalette.proteinHex
        static let water = HealthIntakePalette.water
        static let protein = HealthIntakePalette.protein
        // Apple HIG Activity ring label colors, always on black, never themed or dimmed.
        static let activityBackground = Color.black
        static let move = Color(hex: 0xFA114F)
        static let exercise = Color(hex: 0xA6FF00)
        static let stand = Color(hex: 0x00FFF6)
        static let activitySide = Theme.Size.box160
        static let ringMargin = Theme.Space.s
    }
}
