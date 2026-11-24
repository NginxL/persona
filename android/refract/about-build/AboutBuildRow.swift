import SwiftUI

/// The supported optional scalar input preserves an absent build number without a fake empty or zero value.
/// The application owns package metadata; generated code only renders the supplied immutable value.
struct AboutBuildRow: View {
    let buildNumber: String?

    var body: some View {
        if let value = buildNumber {
            SettingsBuildRow(title: "settings.about.build") {
                SettingsBuildValue(text: value, style: DesignTypography.micro, color: DesignColors.secondary)
            }
        } else {
            SettingsBuildRow(title: "settings.about.build") {
                SettingsBuildFallback(title: "settings.not.provided", style: DesignTypography.micro, color: DesignColors.secondary)
            }
        }
    }
}
