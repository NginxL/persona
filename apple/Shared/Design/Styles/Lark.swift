import SwiftUI

nonisolated extension ComponentSpec {
    /// Lark: flat white cards on light gray in every color, hairline borders, bordered buttons, like the Lark app.
    static let lark: ComponentSpec = {
        let stroke = Color(light: 0xDEE0E3, dark: 0x373A40)
        return ComponentSpec(
            title: String(localized: "Lark [Inbox Event Views 7e4419]"), subtitle: String(localized: "Like Lark"),
            surface: .hairline, buttons: .bordered, fontDesign: .default, previewCorner: 10, components: .persona,
            canvas: .neutral(page: Color(light: 0xF5F6F7, dark: 0x141414), card: Color(light: 0xFFFFFF, dark: 0x1F1F1F)),
            cardStroke: stroke,
            // The Lark gateway's --message-incoming and its card chrome.
            larkOtherBubble: StyleSpec.larkGrayBubble, larkCard: Color(light: 0xFFFFFF, dark: 0x1E1F23), colors: .lark)
    }()
}

nonisolated extension ColorSpec {
    /// Lark's blue (Lark's own); Lark's pages keep the Lark gateway's --message-outgoing bubble.
    static let lark = ColorSpec(
        title: String(localized: "Lark Blue"), subtitle: String(localized: "Lark [Inbox Event Views 7e4419]"),
        accent: Pair(light: 0x3370FF, dark: 0x4C88FF),
        bubble: Color(light: 0xD1E3FF, dark: 0x133063), bubbleText: .primary, deepensBubble: false, larksOwn: true)
}
