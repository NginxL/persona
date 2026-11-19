import SwiftUI

/// An immutable, application-owned snapshot. The native adapter resolves a missing version to the localized
/// "Not provided" resource before creating this model; generated UI never reads Android package metadata.
protocol AboutVersionModel: RefractModel {
    var versionName: String { get }
}

/// One Android About row, evaluated beside the unchanged handwritten screen. No navigation, runtime or model lookup.
struct AboutVersionRow: View {
    let model: AboutVersionModel

    var body: some View {
        SettingsReadOnlyRow(title: "settings.about.version", value: model.versionName)
    }
}
