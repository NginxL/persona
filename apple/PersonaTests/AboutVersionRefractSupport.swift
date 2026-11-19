import SwiftUI

/// The ordinary SwiftUI declarations consumed by the shared AboutVersionRow source.
/// This file belongs only to PersonaTests; no runtime or application initialization exists.
protocol RefractModel {}

/// iOS's mapping of the shared read-only row is the native control used by SettingsAboutPage.
/// The Android `SettingsReadOnlyRow` -> `JRow` mapping remains platform-specific in refract.json.
struct SettingsReadOnlyRow: View {
    let title: String
    let value: String

    var body: some View {
        // Map only the shared source's reviewed resource key to the existing Apple catalog entry.
        // Do not reinterpret Android's JRow or expose an untranslated resource-key as user-facing text.
        LabeledContent(title == "settings.about.version" ? "Version" : LocalizedStringKey(title), value: value)
    }
}

/// An application-owned immutable Version snapshot. Bundle access belongs to the adapter factory,
/// before the view is presented; neither the shared view nor its model reads app metadata.
struct AboutVersionSnapshot: Equatable {
    let shortVersion: String
    let buildNumber: String

    /// The existing SettingsAboutPage presentation, including its empty-missing-field behavior.
    var displayVersion: String { "\(shortVersion) (\(buildNumber))" }

    init(shortVersion: String, buildNumber: String) {
        self.shortVersion = shortVersion
        self.buildNumber = buildNumber
    }

    init(bundle: Bundle = .main) {
        self.init(
            shortVersion: bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "",
            buildNumber: bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "")
    }
}

/// The immutable implementation of the shared model. The shared protocol remains a read-only
/// interface in both consumers; this adapter has no observation, writer or global state.
struct ImmutableAboutVersionModel: AboutVersionModel {
    let versionName: String

    init(snapshot: AboutVersionSnapshot) {
        versionName = snapshot.displayVersion
    }
}
