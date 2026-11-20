import SwiftUI

/// A standalone test process: the native and shared rows coexist in one ordinary SwiftUI host.
/// It has no production route, Hub, mutable model or accessibility override on either row.
@main
struct AboutVersionAccessibilityHostApp: App {
    var body: some Scene {
        WindowGroup {
            AboutVersionAccessibilityHostView()
        }
    }
}

private struct AboutVersionAccessibilityHostView: View {
    private let snapshot = AboutVersionSnapshot(shortVersion: "1.42.0", buildNumber: "37")
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @AppStorage("aboutAccessibility.appearance") private var appearance = "light"
    @AppStorage("aboutAccessibility.language") private var language = "en"
    @AppStorage("aboutAccessibility.textSize") private var textSize = "standard"

    private var colorScheme: ColorScheme { appearance == "dark" ? .dark : .light }
    private var dynamicTypeSize: DynamicTypeSize { textSize == "xxxlarge" ? .xxxLarge : .large }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    LabeledContent("Version", value: snapshot.displayVersion)
                        .accessibilityIdentifier("about.native.version")
                    AboutVersionRow(model: ImmutableAboutVersionModel(snapshot: snapshot))
                        .accessibilityIdentifier("about.shared.version")
                }
            }
            .navigationTitle("About Version Test")
            #if DEBUG
            .safeAreaInset(edge: .bottom) {
                Text(horizontalSizeClass == .regular ? "regular" : "compact")
                    .accessibilityIdentifier("about.host.size-class")
            }
            #endif
        }
        .environment(\.locale, Locale(identifier: language))
        .environment(\.colorScheme, colorScheme)
        .environment(\.dynamicTypeSize, dynamicTypeSize)
    }
}
