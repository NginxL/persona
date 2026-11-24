import SwiftUI

/// The same intake colors in the app and the widget extension, which does not build Theme.
nonisolated enum HealthIntakePalette {
    static let waterHex: (light: UInt32, dark: UInt32) = (0x2457D6, 0x719BFF)
    static let proteinHex: (light: UInt32, dark: UInt32) = (0x9A5900, 0xF6B650)
    static let water = Color(light: waterHex.light, dark: waterHex.dark)
    static let protein = Color(light: proteinHex.light, dark: proteinHex.dark)
}
