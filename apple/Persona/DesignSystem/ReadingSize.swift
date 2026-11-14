import SwiftUI

/// The interface is capped at `Theme.interfaceLargestTypeSize` (xxxLarge), the largest size that is not an accessibility
/// size; reading content grows on to AX5 (`docs/design/DESIGN_SYSTEM.md`, "Dynamic Type"). `RootView` sets the cap on
/// `\.dynamicTypeSize` and keeps the size the reader chose (the system's, moved by Small / Standard / Large) here.
private struct ReadingTypeSizeKey: EnvironmentKey {
    static let defaultValue: DynamicTypeSize? = nil
}

extension EnvironmentValues {
    /// The text size reading content draws at: not capped. nil where no `RootView` is above (a preview).
    var readingTypeSize: DynamicTypeSize? {
        get { self[ReadingTypeSizeKey.self] }
        set { self[ReadingTypeSizeKey.self] = newValue }
    }
}

extension Theme {
    /// The size the interface (buttons, bars, menus, row labels, headers, badges) stops growing at.
    static let interfaceLargestTypeSize: DynamicTypeSize = .xxxLarge

    /// The interface's size for a chosen one.
    static func interfaceTypeSize(_ chosen: DynamicTypeSize) -> DynamicTypeSize { min(chosen, interfaceLargestTypeSize) }
}

extension View {
    /// Reading content: message bodies, Inbox and file bodies, plans and minutes, what is being typed. It keeps growing
    /// past the interface's cap, up to AX5. The one way to lift the cap; `scripts/test/test_apple_reading_content.py`
    /// lists the files that may use it.
    func readingContent() -> some View { modifier(ReadingContent()) }
}

private struct ReadingContent: ViewModifier {
    @Environment(\.readingTypeSize) private var reading
    @Environment(\.dynamicTypeSize) private var size

    func body(content: Content) -> some View { content.dynamicTypeSize(reading ?? size) }
}
