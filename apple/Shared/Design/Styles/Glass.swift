import SwiftUI

nonisolated extension ComponentSpec {
    /// The picker's line under Liquid Glass: the system's own look.
    private static var glassSubtitle: String { String(localized: "Native iOS, simple") }

    /// Liquid Glass: iOS 26's own look, Liquid Glass on the system's pages.
    static let glass = ComponentSpec(
        title: String(localized: "Liquid Glass"), subtitle: Self.glassSubtitle,
        surface: .glass, buttons: .glass, fontDesign: .default, previewCorner: 20, components: .persona, workList: .timeline,
        canvas: .system,
        larkOtherBubble: StyleSpec.larkGrayBubble, colors: .blue)
}

nonisolated extension ColorSpec {
    /// System blue (Liquid Glass's own).
    static let blue = ColorSpec(
        title: String(localized: "System Blue"), subtitle: String(localized: "Liquid Glass"),
        accent: Pair(light: 0x007AFF, dark: 0x0A84FF),
        // A soft tint: a solid system blue was too heavy.
        bubble: Color(light: 0xDCEAFF, dark: 0x1D3557), bubbleText: .primary, deepensBubble: false)
}
