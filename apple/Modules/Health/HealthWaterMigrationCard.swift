import SwiftUI

/// The existing notice surface, above Today on iPhone and iPad. No migration happens until its button is tapped.
struct HealthWaterMigrationCard: View {
    let page: HealthModel

    var body: some View {
        NoticeBanner(text: String(localized: "Move the water extension's records and settings into Health and turn off the extension. Its water page on Android will disappear."),
                     tone: .neutral, lineLimit: nil) {
            Button { Task { await page.importWaterExtension() } } label: {
                Text(page.waterImportBusy ? String(localized: "Moving into Health…") : String(localized: "Move into Health"))
                    .buttonLabelLine()
                    .scaledFrame(minHeight: Theme.Size.tapTarget)
            }
            .disabled(page.intakeBusy)
            .accessibilityIdentifier("health-water-import")
        }
        .accessibilityIdentifier("health-water-migration")
    }
}
